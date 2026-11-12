package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aharness/internal/model"
)

// writeConfigFile writes one JSON config file into a fresh state directory and
// returns its path.
func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	return path
}

// cfgWithFile parses minimal arguments with an explicit config file, letting
// the file supply the model and workspace.
func cfgWithFile(t *testing.T, body string, extra ...string) config {
	t.Helper()
	path := writeConfigFile(t, body)
	args := append([]string{"-task", "config file task", "-state-dir", t.TempDir(), "-config", path}, extra...)
	return cfgTestParseOK(t, args, nil)
}

func TestConfigPrecedenceFlagEnvFileDefault(t *testing.T) {
	envWorkspace := t.TempDir()
	flagWorkspace := t.TempDir()
	fileWorkspace := t.TempDir()

	// The file sets every layer-sensitive value; the environment overrides a
	// subset and the flag overrides one of those again.
	path := writeConfigFile(t, `{
		"model": "file-model",
		"workspace": "`+fileWorkspace+`",
		"max_turns": 9,
		"max_total_tokens": 9000,
		"max_output_tokens": 4096,
		"session_timeout": "45m",
		"model_timeout": "2m",
		"http_timeout": "30s",
		"catalog_timeout": "5s",
		"require_usage": true,
		"log_requests": true
	}`)
	env := map[string]string{
		"AHARNESS_MODEL":             "env-model",
		"AHARNESS_MAX_TOTAL_TOKENS":  "7000",
		"AHARNESS_SESSION_TIMEOUT":   "15m",
		"AHARNESS_REQUIRE_USAGE":     "false",
		"AHARNESS_PROVIDER_BASE_URL": "http://env.example/v1",
	}
	args := []string{
		"-task", "precedence",
		"-model", "flag-model",
		"-workspace", flagWorkspace,
		"-state-dir", t.TempDir(),
		"-config", path,
		"-max-turns", "3",
	}
	cfg := cfgTestParseOK(t, args, env)

	cases := []struct {
		name string
		got  any
		want any
	}{
		{"model flag wins", cfg.Model, "flag-model"},
		{"workspace flag wins", cfg.Workspace, flagWorkspace},
		{"max turns flag wins", cfg.MaxTurns, 3},
		{"max total tokens env wins", cfg.MaxTotalTokens, 7000},
		{"session timeout env wins", cfg.SessionTimeout, 15 * time.Minute},
		{"require usage env wins", cfg.RequireUsage, false},
		{"max output tokens from file", cfg.MaxOutputTokens, 4096},
		{"model timeout from file", cfg.ModelTimeout, 2 * time.Minute},
		{"http timeout from file", cfg.HTTPTimeout, 30 * time.Second},
		{"catalog timeout from file", cfg.CatalogTimeout, 5 * time.Second},
		{"log requests from file", cfg.LogRequests, true},
		{"base URL from env", cfg.BaseURL, "http://env.example/v1"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
	// A file-supplied value is explicit, so inheritance treats it like a flag.
	if !cfg.maxTurnsSet || !cfg.maxTotalTokensSet || !cfg.maxOutputSet ||
		!cfg.sessionTimeoutSet || !cfg.modelTimeoutSet || !cfg.httpTimeoutSet || !cfg.catalogTimeoutSet {
		t.Errorf("explicit flags after file resolution = turns %t tokens %t output %t session %t model %t http %t catalog %t, want all true",
			cfg.maxTurnsSet, cfg.maxTotalTokensSet, cfg.maxOutputSet,
			cfg.sessionTimeoutSet, cfg.modelTimeoutSet, cfg.httpTimeoutSet, cfg.catalogTimeoutSet)
	}
	if cfg.modelSet != true || !cfg.workspaceSet || !cfg.baseURLSet {
		t.Errorf("set bits = model %t workspace %t base URL %t, want all true",
			cfg.modelSet, cfg.workspaceSet, cfg.baseURLSet)
	}
	_ = envWorkspace
}

func TestConfigFileDefaultsWhenNothingElseSet(t *testing.T) {
	workspace := t.TempDir()
	cfg := cfgWithFile(t, `{
		"model": "file-model",
		"workspace": "`+workspace+`",
		"thinking_effort": "high",
		"max_context_tokens": 64000
	}`, "-task", "from file")

	if cfg.Model != "file-model" || cfg.Workspace != workspace {
		t.Fatalf("model/workspace = %q/%q, want the file values", cfg.Model, cfg.Workspace)
	}
	if cfg.ThinkingEffort != "high" || !cfg.thinkingEffortSet {
		t.Fatalf("thinking effort = %q set=%t, want file value", cfg.ThinkingEffort, cfg.thinkingEffortSet)
	}
	if cfg.MaxContextTokens != 64000 || cfg.maxContextValue != 64000 || !cfg.maxContextSet {
		t.Fatalf("context = %d value=%d set=%t, want the file 64000", cfg.MaxContextTokens, cfg.maxContextValue, cfg.maxContextSet)
	}
}

func TestConfigFileDefaultsApplyToBuiltInDefaults(t *testing.T) {
	// With no file and no overrides the documented defaults still hold.
	stateDir := t.TempDir()
	args := []string{
		"-task", "defaults",
		"-model", "deepseek-v4.1-flash",
		"-workspace", t.TempDir(),
		"-state-dir", stateDir,
	}
	cfg := cfgTestParseOK(t, args, nil)
	if cfg.HTTPTimeout != model.DefaultHTTPTimeout {
		t.Errorf("HTTPTimeout = %s, want %s", cfg.HTTPTimeout, model.DefaultHTTPTimeout)
	}
	if cfg.CatalogTimeout != model.CatalogRequestTimeout {
		t.Errorf("CatalogTimeout = %s, want %s", cfg.CatalogTimeout, model.CatalogRequestTimeout)
	}
	if cfg.httpTimeoutSet || cfg.catalogTimeoutSet {
		t.Errorf("timeout set bits = http %t catalog %t, want false", cfg.httpTimeoutSet, cfg.catalogTimeoutSet)
	}
	if want := filepath.Join(stateDir, "config.json"); cfg.ConfigPath != want {
		t.Errorf("ConfigPath = %q, want the implicit %q", cfg.ConfigPath, want)
	}
}

func TestConfigImplicitFileUnderStateDir(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	path := filepath.Join(stateDir, "config.json")
	if err := os.WriteFile(path, []byte(`{"model":"implicit-model","workspace":"`+workspace+`"}`), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	cfg := cfgTestParseOK(t, []string{"-task", "implicit", "-state-dir", stateDir}, nil)
	if cfg.Model != "implicit-model" || cfg.Workspace != workspace {
		t.Fatalf("model/workspace = %q/%q, want the implicit file values", cfg.Model, cfg.Workspace)
	}
	if cfg.ConfigPath != path {
		t.Fatalf("ConfigPath = %q, want %q", cfg.ConfigPath, path)
	}
}

func TestConfigEnvSelectsFile(t *testing.T) {
	path := writeConfigFile(t, `{"model":"env-file-model","workspace":"`+t.TempDir()+`"}`)
	cfg := cfgTestParseOK(t, []string{"-task", "env file", "-state-dir", t.TempDir()},
		map[string]string{"AHARNESS_CONFIG": path})
	if cfg.Model != "env-file-model" {
		t.Fatalf("Model = %q, want the environment-selected file value", cfg.Model)
	}
	if cfg.ConfigPath != path {
		t.Fatalf("ConfigPath = %q, want %q", cfg.ConfigPath, path)
	}
}

func TestConfigFlagOverridesEnvFileSelection(t *testing.T) {
	envPath := writeConfigFile(t, `{"model":"env-file-model"}`)
	flagPath := writeConfigFile(t, `{"model":"flag-file-model","workspace":"`+t.TempDir()+`"}`)
	cfg := cfgTestParseOK(t, []string{"-task", "flag file", "-state-dir", t.TempDir(), "-config", flagPath},
		map[string]string{"AHARNESS_CONFIG": envPath})
	if cfg.Model != "flag-file-model" {
		t.Fatalf("Model = %q, want the -config file value", cfg.Model)
	}
	if cfg.ConfigPath != flagPath {
		t.Fatalf("ConfigPath = %q, want %q", cfg.ConfigPath, flagPath)
	}
}

func TestConfigFileDisabledWithEmptyFlag(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	path := filepath.Join(stateDir, "config.json")
	if err := os.WriteFile(path, []byte(`{"model":"implicit-model"}`), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	cfg := cfgTestParseOK(t, []string{"-task", "disabled", "-state-dir", stateDir, "-config", "",
		"-model", "flag-model", "-workspace", workspace}, nil)
	if cfg.Model != "flag-model" {
		t.Fatalf("Model = %q, want the flag value with the file disabled", cfg.Model)
	}
	if cfg.ConfigPath != "" {
		t.Fatalf("ConfigPath = %q, want empty (disabled)", cfg.ConfigPath)
	}
}

func TestConfigFileErrors(t *testing.T) {
	stateDir := t.TempDir()
	base := []string{"-task", "task", "-model", "deepseek-v4.1-flash", "-workspace", t.TempDir(), "-state-dir", stateDir}

	t.Run("explicit missing file is an error", func(t *testing.T) {
		err := cfgTestParseErr(t, append(append([]string{}, base...), "-config", filepath.Join(t.TempDir(), "nope.json")), nil)
		if !strings.Contains(err.Error(), "config file") {
			t.Errorf("error = %q, want a config file error", err)
		}
	})

	t.Run("env missing file is an error", func(t *testing.T) {
		err := cfgTestParseErr(t, base, map[string]string{"AHARNESS_CONFIG": filepath.Join(t.TempDir(), "nope.json")})
		if !strings.Contains(err.Error(), "config file") {
			t.Errorf("error = %q, want a config file error", err)
		}
	})

	t.Run("unknown key is rejected", func(t *testing.T) {
		path := writeConfigFile(t, `{"max_turn": 5}`)
		err := cfgTestParseErr(t, append(append([]string{}, base...), "-config", path), nil)
		if !strings.Contains(err.Error(), "max_turn") {
			t.Errorf("error = %q, want the offending key named", err)
		}
	})

	t.Run("malformed duration names the key", func(t *testing.T) {
		path := writeConfigFile(t, `{"session_timeout": "forever"}`)
		err := cfgTestParseErr(t, append(append([]string{}, base...), "-config", path), nil)
		if !strings.Contains(err.Error(), "session_timeout") {
			t.Errorf("error = %q, want the offending key named", err)
		}
	})

	t.Run("numeric duration is rejected", func(t *testing.T) {
		path := writeConfigFile(t, `{"model_timeout": 5000000000}`)
		err := cfgTestParseErr(t, append(append([]string{}, base...), "-config", path), nil)
		if !strings.Contains(err.Error(), "model_timeout") {
			t.Errorf("error = %q, want the offending key named", err)
		}
	})

	t.Run("invalid values are rejected even when a flag overrides them", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			body string
			want string
		}{
			{"negative turns", `{"max_turns": -1}`, "max_turns"},
			{"negative total tokens", `{"max_total_tokens": -1}`, "max_total_tokens"},
			{"negative output tokens", `{"max_output_tokens": -1}`, "max_output_tokens"},
			{"zero context", `{"max_context_tokens": 0}`, "max_context_tokens"},
			{"negative timeout", `{"session_timeout": "-1s"}`, "session_timeout"},
			{"empty thinking effort", `{"thinking_effort": ""}`, "thinking_effort"},
			{"zero http timeout", `{"http_timeout": "0s"}`, "http_timeout"},
			{"zero catalog timeout", `{"catalog_timeout": "0s"}`, "catalog_timeout"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				path := writeConfigFile(t, tc.body)
				// -max-turns would mask a file-only bug if the file were not
				// validated on its own.
				args := append(append([]string{}, base...), "-config", path, "-max-turns", "4")
				err := cfgTestParseErr(t, args, nil)
				if !strings.Contains(err.Error(), tc.want) {
					t.Errorf("error = %q, want substring %q", err, tc.want)
				}
			})
		}
	})

	t.Run("oversized file is rejected", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "big.json")
		body := `{"model":"` + strings.Repeat("a", maxConfigFileBytes) + `"}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write config file: %v", err)
		}
		err := cfgTestParseErr(t, append(append([]string{}, base...), "-config", path), nil)
		if !strings.Contains(err.Error(), "exceeds") {
			t.Errorf("error = %q, want an oversize error", err)
		}
	})

	t.Run("version ignores a broken file", func(t *testing.T) {
		path := writeConfigFile(t, `{"max_turns": -1}`)
		var stdout, stderr bytes.Buffer
		if code := runArgs([]string{"-version", "-config", path}, &stdout, &stderr); code != 0 {
			t.Fatalf("runArgs -version exit = %d, want 0; stderr = %q", code, stderr.String())
		}
	})
}

func TestConfigHTTPTimeoutPrecedenceAndValidation(t *testing.T) {
	path := writeConfigFile(t, `{"http_timeout": "45s", "catalog_timeout": "9s"}`)

	cfg := cfgTestParseOK(t, append(cfgTestArgs(t), "-config", path,
		"-http-timeout", "20s", "-catalog-timeout", "3s"), nil)
	if cfg.HTTPTimeout != 20*time.Second || cfg.CatalogTimeout != 3*time.Second {
		t.Fatalf("timeouts = %s/%s, want the flag values 20s/3s", cfg.HTTPTimeout, cfg.CatalogTimeout)
	}

	cfg = cfgTestParseOK(t, append(cfgTestArgs(t), "-config", path),
		map[string]string{"AHARNESS_HTTP_TIMEOUT": "75s"})
	if cfg.HTTPTimeout != 75*time.Second {
		t.Fatalf("HTTPTimeout = %s, want the environment 75s", cfg.HTTPTimeout)
	}
	if cfg.CatalogTimeout != 9*time.Second {
		t.Fatalf("CatalogTimeout = %s, want the file 9s", cfg.CatalogTimeout)
	}

	// A non-positive value is a config error whichever layer supplied it.
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"flag zero", []string{"-http-timeout", "0s"}, nil, "HTTP timeout must be positive"},
		{"flag negative", []string{"-http-timeout", "-1s"}, nil, "HTTP timeout must be positive"},
		{"env zero", nil, map[string]string{"AHARNESS_HTTP_TIMEOUT": "0s"}, "HTTP timeout must be positive"},
		{"catalog env zero", nil, map[string]string{"AHARNESS_CATALOG_TIMEOUT": "0s"}, "catalog timeout must be positive"},
		{"catalog flag negative", []string{"-catalog-timeout", "-5s"}, nil, "catalog timeout must be positive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := cfgTestParseErr(t, append(cfgTestArgs(t), tc.args...), tc.env)
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want substring %q", err, tc.want)
			}
		})
	}
}

func TestConfigTimeoutFlagsReachTerminalActions(t *testing.T) {
	cfg := cfgTestParseOK(t, []string{"-list-models", "-state-dir", t.TempDir(),
		"-catalog-timeout", "4s", "-http-timeout", "11s"}, nil)
	if cfg.CatalogTimeout != 4*time.Second {
		t.Errorf("CatalogTimeout = %s, want 4s", cfg.CatalogTimeout)
	}
	if cfg.HTTPTimeout != 0 {
		t.Errorf("HTTPTimeout = %s, want 0 for a terminal action that never calls the provider", cfg.HTTPTimeout)
	}

	// A zero catalog timeout would silently select the default inside the
	// source, so the terminal path validates it too.
	err := cfgTestParseErr(t, []string{"-list-models", "-state-dir", t.TempDir(), "-catalog-timeout", "0s"}, nil)
	if !strings.Contains(err.Error(), "catalog timeout must be positive") {
		t.Errorf("error = %q, want a positive-catalog-timeout error", err)
	}
	err = cfgTestParseErr(t, []string{"-refresh-models", "-state-dir", t.TempDir()},
		map[string]string{"AHARNESS_CATALOG_TIMEOUT": "-1s"})
	if !strings.Contains(err.Error(), "catalog timeout must be positive") {
		t.Errorf("error = %q, want a positive-catalog-timeout error", err)
	}
}

func TestLayerResolution(t *testing.T) {
	fileValue := 5
	cases := []struct {
		name      string
		layer     layer[int]
		wantValue int
		wantSet   bool
	}{
		{"flag wins", layered(1, true, 2, true, &fileValue, 9), 1, true},
		{"env beats file", layered(1, false, 2, true, &fileValue, 9), 2, true},
		{"file beats default", layered(1, false, 2, false, &fileValue, 9), 5, true},
		{"default alone", layered(1, false, 2, false, nil, 9), 9, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.layer.value(); got != tc.wantValue {
				t.Errorf("value = %d, want %d", got, tc.wantValue)
			}
			if got := tc.layer.set(); got != tc.wantSet {
				t.Errorf("set = %t, want %t", got, tc.wantSet)
			}
		})
	}
}

func TestConfigFileRejectsMalformedInput(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"duplicate key", `{"model":"a","model":"b"}`, "duplicate"},
		{"type mismatch", `{"max_turns":"many"}`, "max_turns"},
		{"trailing value", `{"model":"a"} {"model":"b"}`, "after top-level value"},
		{"empty file", ``, "unexpected EOF"},
		{"not an object", `[1,2,3]`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, tc.body)
			_, err := loadFileConfig(path, true)
			if err == nil {
				t.Fatalf("loadFileConfig(%q) = nil error, want rejection", tc.body)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestConfigFileEmptyObjectIsValid(t *testing.T) {
	path := writeConfigFile(t, `{}`)
	cfg, err := loadFileConfig(path, true)
	if err != nil {
		t.Fatalf("loadFileConfig: %v", err)
	}
	if cfg.Model != nil || cfg.MaxTurns != nil || cfg.HTTPTimeout != nil {
		t.Fatalf("empty object decoded to %+v, want every key absent", cfg)
	}
}

func TestConfigFileAcceptsAbsentPath(t *testing.T) {
	cfg, err := loadFileConfig("", false)
	if err != nil {
		t.Fatalf("loadFileConfig(\"\"): %v", err)
	}
	if cfg != (fileConfig{}) {
		t.Fatalf("loadFileConfig(\"\") = %+v, want zero", cfg)
	}
	// An implicit missing file is allowed; an explicit one is not.
	if _, err := loadFileConfig(filepath.Join(t.TempDir(), "missing.json"), false); err != nil {
		t.Fatalf("implicit missing file: %v", err)
	}
	if _, err := loadFileConfig(filepath.Join(t.TempDir(), "missing.json"), true); err == nil {
		t.Fatal("explicit missing file accepted")
	}
}

func TestConfigFileHTTPTimeoutReachesAdapter(t *testing.T) {
	path := writeConfigFile(t, `{"http_timeout": "7m"}`)
	cfg := cfgTestParseOK(t, append(cfgTestArgs(t), "-config", path), nil)
	// The flag default is the built-in 15m, so a naive implementation would
	// treat it as an explicit flag and ignore the file.
	if cfg.HTTPTimeout != 7*time.Minute {
		t.Fatalf("HTTPTimeout = %s, want the file value 7m", cfg.HTTPTimeout)
	}
	if !cfg.httpTimeoutSet {
		t.Fatal("httpTimeoutSet = false, want true for a file-supplied timeout")
	}
}
