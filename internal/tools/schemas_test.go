package tools

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"reflect"
	"strings"
	"testing"
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
