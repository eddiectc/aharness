package tools

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/eddiectc/aharness/model"
)

type schemaDoc struct {
	Type       string                     `json:"type"`
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
}

func TestSchemasMatchArgumentStructs(t *testing.T) {
	schemas := make(map[string]schemaDoc)
	for _, s := range Schemas() {
		var doc schemaDoc
		if err := jsonv2.Unmarshal(s.Parameters, &doc); err != nil {
			t.Fatalf("unmarshal %s schema: %v", s.Name, err)
		}
		schemas[s.Name] = doc
	}

	tests := []struct {
		name   string
		args   any
		sample string
	}{
		{
			name:   "read_file",
			args:   readFileArgs{},
			sample: `{"intent":"read a","path":"a.txt","offset":2,"limit":10}`,
		},
		{
			name:   "edit_file",
			args:   editFileArgs{},
			sample: `{"intent":"edit a","path":"a.txt","old_string":"a","new_string":"b","replace_all":true}`,
		},
		{
			name:   "search",
			args:   searchArgs{},
			sample: `{"intent":"search a","pattern":"x","path":".","glob":"*.go","case_sensitive":true,"max_results":5}`,
		},
		{
			name:   "run_command",
			args:   runCommandArgs{},
			sample: `{"intent":"run tests","command":"true","cwd":".","timeout_ms":1000}`,
		},
		{
			name:   "web_search",
			args:   webSearchArgs{},
			sample: `{"intent":"search the web","query":"aharness","max_results":5,"categories":"general","language":"en","time_range":"day"}`,
		},
		{
			name:   "web_fetch",
			args:   webFetchArgs{},
			sample: `{"intent":"fetch a page","url":"https://example.com/","offset":0}`,
		},
		{
			name:   "todo",
			args:   todoArgs{},
			sample: `{"intent":"plan the work","items":[{"content":"read the docs","status":"pending"},{"content":"write the tool","status":"in_progress"}]}`,
		},
		{
			name:   "delegate",
			args:   DelegateArgs{},
			sample: `{"intent":"delegate the search","tasks":[{"agent":"explore","task":"find the projection","session":""}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc, ok := schemas[tc.name]
			if !ok {
				t.Fatalf("schema %q is missing", tc.name)
			}
			if doc.Type != "object" {
				t.Errorf("schema type = %q, want object", doc.Type)
			}
			fields := jsonFieldNames(reflect.TypeOf(tc.args))
			for name := range doc.Properties {
				if !fields[name] {
					t.Errorf("schema property %q has no argument-struct field", name)
				}
			}
			for name := range fields {
				if _, ok := doc.Properties[name]; !ok {
					t.Errorf("argument-struct field %q is missing from the schema", name)
				}
			}
			for _, required := range doc.Required {
				if _, ok := doc.Properties[required]; !ok {
					t.Errorf("required name %q is not a schema property", required)
				}
				if !fields[required] {
					t.Errorf("required name %q has no argument-struct field", required)
				}
			}

			target := reflect.New(reflect.TypeOf(tc.args)).Interface()
			if err := jsonv2.Unmarshal([]byte(tc.sample), target, jsonv2.RejectUnknownMembers(true)); err != nil {
				t.Errorf("schema sample does not decode into %s args: %v", tc.name, err)
			}
			target = reflect.New(reflect.TypeOf(tc.args)).Interface()
			if err := jsonv2.Unmarshal([]byte(`{"unknown_member":true}`), target, jsonv2.RejectUnknownMembers(true)); err == nil {
				t.Errorf("%s args accepted an unknown member with RejectUnknownMembers(true)", tc.name)
			}
		})
	}
}

func TestSchemaRequiredFields(t *testing.T) {
	want := map[string][]string{
		"read_file":   {"intent", "path"},
		"edit_file":   {"intent", "path", "old_string", "new_string"},
		"search":      {"intent", "pattern"},
		"run_command": {"intent", "command"},
		"web_search":  {"intent", "query"},
		"web_fetch":   {"intent", "url"},
		"todo":        {"intent"},
		"delegate":    {"intent", "tasks"},
	}
	for _, s := range Schemas() {
		var doc schemaDoc
		if err := jsonv2.Unmarshal(s.Parameters, &doc); err != nil {
			t.Fatalf("unmarshal %s schema: %v", s.Name, err)
		}
		got := strings.Join(doc.Required, ",")
		if want := strings.Join(want[s.Name], ","); got != want {
			t.Errorf("%s required = %q, want %q", s.Name, got, want)
		}
	}
}

func TestEveryCurrentToolDeclaresAnIntent(t *testing.T) {
	for _, s := range Schemas() {
		var doc schemaDoc
		if err := jsonv2.Unmarshal(s.Parameters, &doc); err != nil {
			t.Fatalf("unmarshal %s schema: %v", s.Name, err)
		}
		raw, ok := doc.Properties["intent"]
		if !ok {
			t.Errorf("%s schema does not declare intent", s.Name)
			continue
		}
		var property struct {
			Type      string `json:"type"`
			MaxLength int    `json:"maxLength"`
		}
		if err := jsonv2.Unmarshal(raw, &property); err != nil {
			t.Errorf("%s intent property: %v", s.Name, err)
		}
		if property.Type != "string" || property.MaxLength != MaxIntentBytes {
			t.Errorf("%s intent = %+v, want a string capped at %d", s.Name, property, MaxIntentBytes)
		}
		if !slices.Contains(doc.Required, "intent") {
			t.Errorf("%s schema does not require intent", s.Name)
		}
	}

	// toolset-v0 is frozen prompt material and predates the shared argument.
	for _, s := range SchemasForMust(t, "toolset-v0") {
		if strings.Contains(string(s.Parameters), `"intent"`) {
			t.Errorf("frozen toolset-v0 %s declares intent", s.Name)
		}
	}
}

func TestNamesForVersionAndExclusions(t *testing.T) {
	for _, version := range []string{"toolset-v0", "toolset-v1", Version} {
		got, err := NamesFor(version)
		if err != nil {
			t.Fatalf("NamesFor(%q): %v", version, err)
		}
		schemas, err := SchemasFor(version)
		if err != nil {
			t.Fatalf("SchemasFor(%q): %v", version, err)
		}
		want := make([]string, len(schemas))
		for i, schema := range schemas {
			want[i] = schema.Name
		}
		if !slices.Equal(got, want) {
			t.Fatalf("NamesFor(%q) = %v, want %v", version, got, want)
		}
	}
	got, err := NamesFor(Version, NameDelegate, NameTodo)
	if err != nil {
		t.Fatalf("NamesFor exclusions: %v", err)
	}
	for _, name := range got {
		if name == NameDelegate || name == NameTodo {
			t.Fatalf("exclusion kept %q in %v", name, got)
		}
	}
	if _, err := NamesFor("toolset-future"); err == nil {
		t.Fatal("NamesFor accepted an unknown version")
	}
	without, err := NamesFor(Version, NameDelegate)
	if err != nil {
		t.Fatalf("NamesFor: %v", err)
	}
	if !slices.Equal(NamesWithout(NameDelegate), without) {
		t.Fatalf("NamesWithout = %v, NamesFor = %v", NamesWithout(NameDelegate), without)
	}
	if NameReadFile == "" || NameDelegate != "delegate" || NameRunCommand != "run_command" {
		t.Fatal("stable tool-name constants changed")
	}
}

func TestSchemasForToolsetVersions(t *testing.T) {
	if Version != "toolset-v2" {
		t.Fatalf("Version = %q, want toolset-v2", Version)
	}
	if !SupportsVersion("toolset-v0") || !SupportsVersion("toolset-v1") || !SupportsVersion("toolset-v2") {
		t.Errorf("SupportsVersion rejects a replayable version")
	}
	if SupportsVersion("toolset-v9") || SupportsVersion("") {
		t.Errorf("SupportsVersion accepts an unknown version")
	}
	if _, err := SchemasFor("toolset-v9"); err == nil {
		t.Errorf("SchemasFor(unknown) error = nil, want error")
	}

	v0, err := SchemasFor("toolset-v0")
	if err != nil {
		t.Fatalf("SchemasFor(toolset-v0): %v", err)
	}
	v1, err := SchemasFor("toolset-v1")
	if err != nil {
		t.Fatalf("SchemasFor(toolset-v1): %v", err)
	}
	v2, err := SchemasFor("toolset-v2")
	if err != nil {
		t.Fatalf("SchemasFor(toolset-v2): %v", err)
	}
	if got := Schemas(); !reflect.DeepEqual(got, v2) {
		t.Errorf("Schemas() does not return the v2 set")
	}
	if got, want := names(v2), names(v1)+",delegate"; got != want {
		t.Errorf("toolset-v2 names = %q, want %q", got, want)
	}
	if got, want := names(v0), "read_file,edit_file,search,run_command"; got != want {
		t.Errorf("toolset-v0 names = %q, want the frozen %q", got, want)
	}
	v1Names := strings.Split(names(v1), ",")
	for _, want := range []string{"read_file", "edit_file", "search", "run_command", "web_search", "web_fetch", "todo"} {
		if !slices.Contains(v1Names, want) {
			t.Errorf("toolset-v1 is missing %q: %v", want, v1Names)
		}
	}

	v0Read := findSchema(t, v0, "read_file")
	v1Read := findSchema(t, v1, "read_file")
	if !strings.Contains(v0Read.Description, "defaults to 100") ||
		!strings.Contains(string(v0Read.Parameters), `"default": 100`) ||
		!strings.Contains(string(v0Read.Parameters), `"maximum": 500`) {
		t.Errorf("toolset-v0 read_file is not the frozen 100/500 declaration")
	}
	for _, want := range []string{"defaults to 2000", "capped at 4000", "60 KiB", "continue with offset=N"} {
		if !strings.Contains(v1Read.Description, want) {
			t.Errorf("toolset-v1 read_file description missing %q: %q", want, v1Read.Description)
		}
	}
	for _, want := range []string{`"default": 2000`, `"maximum": 4000`} {
		if !strings.Contains(string(v1Read.Parameters), want) {
			t.Errorf("toolset-v1 read_file parameters missing %s", want)
		}
	}

	v1Search := findSchema(t, v1, "search")
	if !strings.Contains(v1Search.Description, "**") || !strings.Contains(v1Search.Description, "60 KiB") {
		t.Errorf("toolset-v1 search description missing ** or the 60 KiB cap")
	}
	if !strings.Contains(string(v1Search.Parameters), "** matches") {
		t.Errorf("toolset-v1 search glob parameter does not document **")
	}
	v1Command := findSchema(t, v1, "run_command")
	if !strings.Contains(v1Command.Description, "64 KiB") {
		t.Errorf("toolset-v1 run_command description missing the inline cap")
	}
	v1WebSearch := findSchema(t, v1, "web_search")
	if !strings.Contains(v1WebSearch.Description, "untrusted data") {
		t.Errorf("toolset-v1 web_search description missing the untrusted-data warning")
	}
	if !strings.Contains(string(v1WebSearch.Parameters), `"maximum": 10`) {
		t.Errorf("toolset-v1 web_search parameters missing the result cap")
	}
	v1WebFetch := findSchema(t, v1, "web_fetch")
	if !strings.Contains(v1WebFetch.Description, "untrusted data") {
		t.Errorf("toolset-v1 web_fetch description missing the untrusted-data warning")
	}
	if !strings.Contains(string(v1WebFetch.Parameters), `"required": ["intent", "url"]`) {
		t.Errorf("toolset-v1 web_fetch parameters missing the required url")
	}
	if strings.Contains(string(v1WebFetch.Parameters), "userinfo") {
		t.Errorf("toolset-v1 web_fetch schema documents provider-only fields")
	}
	v1Todo := findSchema(t, v1, "todo")
	for _, want := range []string{"replace the whole list", "omit items", "in_progress"} {
		if !strings.Contains(v1Todo.Description, want) {
			t.Errorf("toolset-v1 todo description missing %q: %q", want, v1Todo.Description)
		}
	}
	for _, want := range []string{`"maxItems": 50`, `"required": ["intent"]`, `"enum": ["pending", "in_progress", "completed"]`} {
		if !strings.Contains(string(v1Todo.Parameters), want) {
			t.Errorf("toolset-v1 todo parameters missing %s", want)
		}
	}

	v0[0].Name = "mutated"
	if SchemasForMust(t, "toolset-v0")[0].Name != "read_file" {
		t.Errorf("SchemasFor shares its backing slice with callers")
	}
	v1[0].Name = "mutated"
	if Schemas()[0].Name != "read_file" {
		t.Errorf("Schemas shares its backing slice with callers")
	}
}

func SchemasForMust(t *testing.T, version string) []model.ToolSchema {
	t.Helper()
	schemas, err := SchemasFor(version)
	if err != nil {
		t.Fatalf("SchemasFor(%q): %v", version, err)
	}
	return schemas
}

func findSchema(t *testing.T, schemas []model.ToolSchema, name string) model.ToolSchema {
	t.Helper()
	for _, schema := range schemas {
		if schema.Name == name {
			return schema
		}
	}
	t.Fatalf("schema %q is missing", name)
	return model.ToolSchema{}
}

func names(schemas []model.ToolSchema) string {
	out := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		out = append(out, schema.Name)
	}
	return strings.Join(out, ",")
}

func jsonFieldNames(t reflect.Type) map[string]bool {
	out := make(map[string]bool, t.NumField())
	for i := range t.NumField() {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name != "" {
			out[name] = true
		}
	}
	return out
}
