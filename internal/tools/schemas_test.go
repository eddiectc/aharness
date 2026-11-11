package tools

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"reflect"
	"strings"
	"testing"

	"aharness/internal/model"
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
			sample: `{"path":"a.txt","offset":2,"limit":10}`,
		},
		{
			name:   "edit_file",
			args:   editFileArgs{},
			sample: `{"path":"a.txt","old_string":"a","new_string":"b","replace_all":true}`,
		},
		{
			name:   "search",
			args:   searchArgs{},
			sample: `{"pattern":"x","path":".","glob":"*.go","case_sensitive":true,"max_results":5}`,
		},
		{
			name:   "run_command",
			args:   runCommandArgs{},
			sample: `{"command":"true","cwd":".","timeout_ms":1000}`,
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
		"read_file":   {"path"},
		"edit_file":   {"path", "old_string", "new_string"},
		"search":      {"pattern"},
		"run_command": {"command"},
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

func TestSchemasForToolsetVersions(t *testing.T) {
	if Version != "toolset-v1" {
		t.Fatalf("Version = %q, want toolset-v1", Version)
	}
	if !SupportsVersion("toolset-v0") || !SupportsVersion("toolset-v1") {
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
	if got := Schemas(); !reflect.DeepEqual(got, v1) {
		t.Errorf("Schemas() does not return the v1 set")
	}
	if names(v0) != names(v1) {
		t.Errorf("toolset names changed: v0 %q v1 %q", names(v0), names(v1))
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
