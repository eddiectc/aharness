package tools

import (
	jsonv2 "encoding/json/v2"
	"path/filepath"
)

// FileArgumentPath returns the absolute lexical target path of a built-in file
// tool call. It recognizes read_file, edit_file, and search; search defaults to
// the workspace root when path is omitted. A relative path is joined to
// workspace. The result is cleaned but not symlink-resolved: the tool still
// performs the authoritative containment and symlink checks.
//
// An unrecognized tool, malformed arguments, or a missing or empty path yields
// "". The helper is for policy input, not argument validation: callers must not
// treat a non-empty path as proof that the call is valid.
func FileArgumentPath(workspace, toolName string, args []byte) string {
	var target string
	switch toolName {
	case "read_file":
		var a readFileArgs
		if err := jsonv2.Unmarshal(args, &a); err != nil {
			return ""
		}
		target = a.Path
	case "edit_file":
		var a editFileArgs
		if err := jsonv2.Unmarshal(args, &a); err != nil {
			return ""
		}
		target = a.Path
	case "search":
		var a searchArgs
		if err := jsonv2.Unmarshal(args, &a); err != nil {
			return ""
		}
		if a.Path == nil {
			target = "."
		} else {
			target = *a.Path
		}
	default:
		return ""
	}
	if target == "" {
		return ""
	}
	if filepath.IsAbs(target) || workspace == "" {
		return filepath.Clean(target)
	}
	return filepath.Join(workspace, target)
}
