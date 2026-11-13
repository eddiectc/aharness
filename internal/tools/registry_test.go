package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestRegistrySchemas(t *testing.T) {
	r, _ := newTestRegistry(t)
	got := r.Schemas()
	want := Schemas()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Registry.Schemas() = %v, want %v", got, want)
	}
	names := make([]string, 0, len(got))
	for _, schema := range got {
		names = append(names, schema.Name)
	}
	wantNames := []string{"read_file", "edit_file", "search", "run_command", "web_search", "web_fetch"}
	if strings.Join(names, ",") != strings.Join(wantNames, ",") {
		t.Errorf("schema names = %v, want %v", names, wantNames)
	}

	got[0].Name = "mutated"
	if Schemas()[0].Name != wantNames[0] {
		t.Errorf("Schemas() shares its backing slice with callers")
	}
	if r.Schemas()[0].Name != wantNames[0] {
		t.Errorf("Registry.Schemas() shares its backing slice with callers")
	}
}

func TestRegistryUnknownTool(t *testing.T) {
	r, _ := newTestRegistry(t)
	res := executeRaw(t, r, "no_such_tool", `{}`)
	if res.OK {
		t.Fatalf("unknown tool OK = true, want failure")
	}
	if res.Code != CodeUnknownTool {
		t.Errorf("code = %s, want %s", res.Code, CodeUnknownTool)
	}
	if !strings.Contains(res.Error, "no_such_tool") {
		t.Errorf("error = %q, want it to name the tool", res.Error)
	}
}

func TestRegistryInvalidArguments(t *testing.T) {
	r, _ := newTestRegistry(t)
	tests := []struct {
		name string
		tool string
		raw  string
	}{
		{"malformed json", "read_file", `{`},
		{"not an object", "read_file", `[]`},
		{"missing required", "read_file", `{}`},
		{"wrong type", "read_file", `{"path":5}`},
		{"unknown member", "read_file", `{"path":"x","bogus":true}`},
		{"bad bool", "edit_file", `{"path":"x","old_string":"a","new_string":"b","replace_all":"yes"}`},
		{"empty command", "run_command", `{"command":""}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := executeRaw(t, r, tc.tool, tc.raw)
			if res.OK || res.Code != CodeInvalidArgs {
				t.Errorf("%s(%s) = ok=%v code=%s, want invalid_args (error %q)", tc.tool, tc.raw, res.OK, res.Code, res.Error)
			}
			if !strings.Contains(res.Error, tc.tool) {
				t.Errorf("error = %q, want it to name %s", res.Error, tc.tool)
			}
		})
	}
}

func TestRegistryPanicRecovery(t *testing.T) {
	r, _ := newTestRegistry(t)
	r.fs["panic_tool"] = func(context.Context, json.RawMessage) Result {
		panic("boom")
	}
	res := executeRaw(t, r, "panic_tool", `{}`)
	if res.OK {
		t.Fatalf("panic_tool OK = true, want panic result")
	}
	if res.Code != CodePanic {
		t.Errorf("code = %s, want %s", res.Code, CodePanic)
	}
	if !strings.Contains(res.Error, "boom") {
		t.Errorf("error = %q, want panic value", res.Error)
	}
	if res.Output == "" {
		t.Fatalf("output is empty, want a capped stack")
	}
	if !strings.Contains(res.Output, "goroutine") {
		t.Errorf("output does not look like a stack: %q", res.Output)
	}
	if len(res.Output) > panicStackCap {
		t.Errorf("stack length = %d, want <= %d", len(res.Output), panicStackCap)
	}
}

func TestRegistryErrorMapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code Code
	}{
		{"outside workspace", fmt.Errorf("path: %w", ErrOutsideWorkspace), CodeOutsideWorkspace},
		{"git protected", fmt.Errorf("path: %w", ErrGitProtected), CodeGitProtected},
		{"edit ambiguous", fmt.Errorf("edit: %w", ErrEditAmbiguous), CodeAmbiguous},
		{"edit not found", fmt.Errorf("edit: %w", ErrEditNotFound), CodeNotFound},
		{"binary", fmt.Errorf("read: %w", ErrBinaryFile), CodeBinaryFile},
		{"too large", fmt.Errorf("read: %w", ErrTooLarge), CodeTooLarge},
		{"not exist", os.ErrNotExist, CodeNotFound},
		{"symlink", errSymlink, CodeOutsideWorkspace},
		{"not regular", errNotRegular, CodeInvalidArgs},
		{"not dir", errNotDir, CodeInvalidArgs},
		{"deadline", context.DeadlineExceeded, CodeTimeout},
		{"canceled", context.Canceled, CodeCanceled},
		{"other", errors.New("weird failure"), CodeInternal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := errorResult(tc.err)
			if res.OK {
				t.Fatalf("errorResult(%v) OK = true, want failure", tc.err)
			}
			if res.Code != tc.code {
				t.Errorf("code = %s, want %s", res.Code, tc.code)
			}
			if res.Error == "" {
				t.Errorf("Error is empty")
			}
		})
	}
}

func TestRegistrySchemasExecuteUnknownNames(t *testing.T) {
	r, _ := newTestRegistry(t)
	for _, schema := range r.Schemas() {
		if _, ok := r.fs[schema.Name]; !ok {
			t.Errorf("schema %q has no registered implementation", schema.Name)
		}
	}
}

func TestSchemasReturnsModelSchemas(t *testing.T) {
	schemas := Schemas()
	if len(schemas) != 6 {
		t.Fatalf("Schemas() length = %d, want 6", len(schemas))
	}
	for _, schema := range schemas {
		if schema.Name == "" || schema.Description == "" || len(schema.Parameters) == 0 {
			t.Errorf("incomplete schema: %+v", schema)
		}
	}
}
