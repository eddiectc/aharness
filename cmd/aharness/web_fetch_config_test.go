package main

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"
	"time"
)

func TestConfigWebFetchPrecedence(t *testing.T) {
	workspace := t.TempDir()
	path := writeConfigFile(t, `{
		"model": "file-model",
		"workspace": "`+workspace+`",
		"web_timeout": "45s",
		"web_fetch_provider": "crawl4ai",
		"web_fetch_url": "http://127.0.0.1:11235/base/",
		"web_fetch_key": "file-secret"
	}`)
	args := []string{"-task", "fetch", "-state-dir", t.TempDir(), "-config", path}

	t.Run("defaults", func(t *testing.T) {
		cfg := cfgTestParseOK(t, cfgTestBaseArgs(t), nil)
		if cfg.WebTimeout != 30*time.Second {
			t.Errorf("WebTimeout = %s, want 30s", cfg.WebTimeout)
		}
		if cfg.WebFetchProvider != "builtin" {
			t.Errorf("WebFetchProvider = %q, want builtin", cfg.WebFetchProvider)
		}
		if cfg.WebFetchURL != "" || cfg.WebFetchKey != "" {
			t.Errorf("WebFetchURL/key = %q/%q, want empty", cfg.WebFetchURL, cfg.WebFetchKey)
		}
	})

	t.Run("file", func(t *testing.T) {
		cfg := cfgTestParseOK(t, args, nil)
		if cfg.WebTimeout != 45*time.Second {
			t.Errorf("WebTimeout = %s, want 45s", cfg.WebTimeout)
		}
		if cfg.WebFetchProvider != "crawl4ai" || cfg.WebFetchURL != "http://127.0.0.1:11235/base" {
			t.Errorf("provider/url = %q/%q", cfg.WebFetchProvider, cfg.WebFetchURL)
		}
		if cfg.WebFetchKey != "file-secret" {
			t.Errorf("WebFetchKey = %q", cfg.WebFetchKey)
		}
	})

	t.Run("environment wins", func(t *testing.T) {
		cfg := cfgTestParseOK(t, args, map[string]string{
			"AHARNESS_WEB_TIMEOUT":        "10s",
			"AHARNESS_WEB_FETCH_PROVIDER": "crawl4ai",
			"AHARNESS_WEB_FETCH_URL":      "http://127.0.0.1:9999/",
			"AHARNESS_WEB_FETCH_KEY":      "env-secret",
		})
		if cfg.WebTimeout != 10*time.Second || cfg.WebFetchURL != "http://127.0.0.1:9999" || cfg.WebFetchKey != "env-secret" {
			t.Errorf("environment did not win: %+v", cfg)
		}
	})

	t.Run("flag wins", func(t *testing.T) {
		cfg := cfgTestParseOK(t, append(args,
			"-web-timeout", "5s",
			"-web-fetch-provider", "builtin",
			"-web-fetch-url", "",
			"-web-fetch-key", "flag-secret",
		), map[string]string{"AHARNESS_WEB_TIMEOUT": "10s"})
		if cfg.WebTimeout != 5*time.Second {
			t.Errorf("WebTimeout = %s, want 5s", cfg.WebTimeout)
		}
		if cfg.WebFetchProvider != "builtin" || cfg.WebFetchURL != "" {
			t.Errorf("provider/url = %q/%q, want builtin and empty", cfg.WebFetchProvider, cfg.WebFetchURL)
		}
		if cfg.WebFetchKey != "flag-secret" {
			t.Errorf("WebFetchKey = %q", cfg.WebFetchKey)
		}
	})

	t.Run("empty environment means unset", func(t *testing.T) {
		cfg := cfgTestParseOK(t, args, map[string]string{
			"AHARNESS_WEB_TIMEOUT":        "",
			"AHARNESS_WEB_FETCH_PROVIDER": "",
			"AHARNESS_WEB_FETCH_URL":      "",
		})
		if cfg.WebTimeout != 45*time.Second || cfg.WebFetchProvider != "crawl4ai" {
			t.Errorf("empty environment masked the file layer: %+v", cfg)
		}
	})

	t.Run("flag disables the provider", func(t *testing.T) {
		cfg := cfgTestParseOK(t, append(args, "-web-fetch-provider", "builtin", "-web-fetch-url", ""), nil)
		if cfg.WebFetchProvider != "builtin" || cfg.WebFetchURL != "" || cfg.WebFetchKey != "file-secret" {
			t.Errorf("provider config = %+v", cfg)
		}
	})
}

func TestConfigWebFetchValidation(t *testing.T) {
	base := cfgTestBaseArgs(t)
	workspace := t.TempDir()

	t.Run("invalid timeout flag", func(t *testing.T) {
		for _, raw := range []string{"0s", "-1s", "121s"} {
			err := cfgTestParseErr(t, append(base, "-web-timeout", raw), nil)
			if !strings.Contains(err.Error(), "-web-timeout") {
				t.Errorf("%s: error = %v, want the flag source", raw, err)
			}
		}
	})

	t.Run("invalid timeout environment", func(t *testing.T) {
		err := cfgTestParseErr(t, base, map[string]string{"AHARNESS_WEB_TIMEOUT": "bogus"})
		if !strings.Contains(err.Error(), "AHARNESS_WEB_TIMEOUT") {
			t.Errorf("error = %v, want the environment source", err)
		}
		err = cfgTestParseErr(t, base, map[string]string{"AHARNESS_WEB_TIMEOUT": "121s"})
		if !strings.Contains(err.Error(), "AHARNESS_WEB_TIMEOUT") {
			t.Errorf("error = %v, want the environment source", err)
		}
	})

	t.Run("invalid timeout file", func(t *testing.T) {
		for _, raw := range []string{"0s", "121s"} {
			path := writeConfigFile(t, `{
				"model": "file-model",
				"workspace": "`+workspace+`",
				"web_timeout": "`+raw+`"
			}`)
			err := cfgTestParseErr(t, []string{
				"-task", "fetch", "-state-dir", t.TempDir(), "-config", path,
				"-web-timeout", "10s",
			}, nil)
			if !strings.Contains(err.Error(), "web_timeout") {
				t.Errorf("%s: error = %v, want the file key", raw, err)
			}
		}
	})

	t.Run("invalid provider", func(t *testing.T) {
		err := cfgTestParseErr(t, append(base, "-web-fetch-provider", "firecrawl"), nil)
		if !strings.Contains(err.Error(), "-web-fetch-provider") {
			t.Errorf("error = %v, want the flag source", err)
		}
		err = cfgTestParseErr(t, base, map[string]string{"AHARNESS_WEB_FETCH_PROVIDER": "firecrawl"})
		if !strings.Contains(err.Error(), "AHARNESS_WEB_FETCH_PROVIDER") {
			t.Errorf("error = %v, want the environment source", err)
		}
	})

	t.Run("provider pairing", func(t *testing.T) {
		err := cfgTestParseErr(t, append(base, "-web-fetch-provider", "builtin", "-web-fetch-url", "http://127.0.0.1:1/"), nil)
		if !strings.Contains(err.Error(), "web_fetch_url") {
			t.Errorf("builtin with a URL: error = %v", err)
		}
		err = cfgTestParseErr(t, append(base, "-web-fetch-provider", "crawl4ai"), nil)
		if !strings.Contains(err.Error(), "web_fetch_url") {
			t.Errorf("crawl4ai without a URL: error = %v", err)
		}
	})

	t.Run("invalid provider URL", func(t *testing.T) {
		for _, raw := range []string{"ftp://provider.example", "http://user@provider.example", "https://provider.example/#frag", "https://"} {
			err := cfgTestParseErr(t, append(base, "-web-fetch-provider", "crawl4ai", "-web-fetch-url", raw), nil)
			if err == nil {
				t.Errorf("%s: error = nil", raw)
			}
		}
	})

	t.Run("provider URL accepts loopback http", func(t *testing.T) {
		cfg := cfgTestParseOK(t, append(base, "-web-fetch-provider", "crawl4ai", "-web-fetch-url", "http://127.0.0.1:11235/"), nil)
		if cfg.WebFetchURL != "http://127.0.0.1:11235" {
			t.Errorf("web fetch URL = %q", cfg.WebFetchURL)
		}
	})

	t.Run("invalid key", func(t *testing.T) {
		err := cfgTestParseErr(t, append(base, "-web-fetch-provider", "crawl4ai", "-web-fetch-url", "http://127.0.0.1:1/", "-web-fetch-key", "bad\nkey"), nil)
		if !strings.Contains(err.Error(), "-web-fetch-key") {
			t.Errorf("error = %v, want the flag source", err)
		}
		err = cfgTestParseErr(t, base, map[string]string{"AHARNESS_WEB_FETCH_KEY": "bad\nkey"})
		if !strings.Contains(err.Error(), "AHARNESS_WEB_FETCH_KEY") {
			t.Errorf("error = %v, want the environment source", err)
		}
	})

	t.Run("invalid environment is reported even when the flag wins", func(t *testing.T) {
		err := cfgTestParseErr(t, append(base, "-web-timeout", "10s"),
			map[string]string{"AHARNESS_WEB_TIMEOUT": "121s"})
		if !strings.Contains(err.Error(), "AHARNESS_WEB_TIMEOUT") {
			t.Errorf("error = %v, want the invalid environment layer", err)
		}
	})

	t.Run("invalid file is reported even when the flag wins", func(t *testing.T) {
		path := writeConfigFile(t, `{
			"model": "file-model",
			"workspace": "`+workspace+`",
			"web_fetch_provider": "firecrawl"
		}`)
		err := cfgTestParseErr(t, []string{
			"-task", "fetch", "-state-dir", t.TempDir(), "-config", path,
			"-web-fetch-provider", "builtin",
		}, nil)
		if !strings.Contains(err.Error(), "web_fetch_provider") {
			t.Errorf("error = %v, want the invalid file layer", err)
		}
	})
}

func TestConfigWebFetchHelp(t *testing.T) {
	var out bytes.Buffer
	_, err := parseConfig([]string{"-h"}, cfgTestGetenv(nil), &out)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseConfig(-h) error = %v, want flag.ErrHelp", err)
	}
	help := out.String()
	for _, want := range []string{
		"-web-timeout",
		"-web-fetch-provider",
		"-web-fetch-url",
		"-web-fetch-key",
		"AHARNESS_WEB_TIMEOUT",
		"AHARNESS_WEB_FETCH_PROVIDER",
		"AHARNESS_WEB_FETCH_URL",
		"AHARNESS_WEB_FETCH_KEY",
		"precedence: flag > environment > config file > default",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("help missing %q:\n%s", want, help)
		}
	}
}
