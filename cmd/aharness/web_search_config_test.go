package main

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"
)

func TestConfigSearXNGURLPrecedence(t *testing.T) {
	workspace := t.TempDir()
	path := writeConfigFile(t, `{
		"model": "file-model",
		"workspace": "`+workspace+`",
		"searxng_url": "https://file.example/searxng/"
	}`)
	args := []string{"-task", "search", "-state-dir", t.TempDir(), "-config", path}

	t.Run("file", func(t *testing.T) {
		cfg := cfgTestParseOK(t, args, nil)
		if got, want := cfg.SearXNGURL, "https://file.example/searxng"; got != want {
			t.Errorf("SearXNGURL = %q, want %q", got, want)
		}
	})

	t.Run("environment wins and normalizes", func(t *testing.T) {
		cfg := cfgTestParseOK(t, args, map[string]string{"AHARNESS_SEARXNG_URL": "http://127.0.0.1:8080/"})
		if got, want := cfg.SearXNGURL, "http://127.0.0.1:8080"; got != want {
			t.Errorf("SearXNGURL = %q, want %q", got, want)
		}
	})

	t.Run("flag wins and normalizes", func(t *testing.T) {
		cfg := cfgTestParseOK(t, append(args, "-searxng-url", "https://flag.example/prefix/"),
			map[string]string{"AHARNESS_SEARXNG_URL": "https://env.example"})
		if got, want := cfg.SearXNGURL, "https://flag.example/prefix"; got != want {
			t.Errorf("SearXNGURL = %q, want %q", got, want)
		}
	})

	t.Run("empty environment means unset", func(t *testing.T) {
		cfg := cfgTestParseOK(t, args, map[string]string{"AHARNESS_SEARXNG_URL": ""})
		if got, want := cfg.SearXNGURL, "https://file.example/searxng"; got != want {
			t.Errorf("SearXNGURL = %q, want the file value %q", got, want)
		}
	})

	t.Run("explicit empty flag disables", func(t *testing.T) {
		cfg := cfgTestParseOK(t, append(args, "-searxng-url", ""), nil)
		if cfg.SearXNGURL != "" {
			t.Errorf("SearXNGURL = %q, want disabled", cfg.SearXNGURL)
		}
	})

	t.Run("explicit empty file disables", func(t *testing.T) {
		path := writeConfigFile(t, `{
			"model": "file-model",
			"workspace": "`+workspace+`",
			"searxng_url": ""
		}`)
		cfg := cfgTestParseOK(t, []string{"-task", "search", "-state-dir", t.TempDir(), "-config", path}, nil)
		if cfg.SearXNGURL != "" {
			t.Errorf("SearXNGURL = %q, want disabled", cfg.SearXNGURL)
		}
	})
}

func TestConfigSearXNGURLValidation(t *testing.T) {
	base := cfgTestBaseArgs(t)
	workspace := t.TempDir()

	t.Run("invalid flag", func(t *testing.T) {
		err := cfgTestParseErr(t, append(base, "-searxng-url", "http://public.example"), nil)
		if !strings.Contains(err.Error(), "-searxng-url") {
			t.Errorf("error = %v, want the flag source", err)
		}
	})

	t.Run("invalid environment", func(t *testing.T) {
		err := cfgTestParseErr(t, base, map[string]string{"AHARNESS_SEARXNG_URL": "ftp://search.example"})
		if !strings.Contains(err.Error(), "AHARNESS_SEARXNG_URL") {
			t.Errorf("error = %v, want the environment source", err)
		}
	})

	t.Run("invalid file", func(t *testing.T) {
		path := writeConfigFile(t, `{
			"model": "file-model",
			"workspace": "`+workspace+`",
			"searxng_url": "https://user@search.example"
		}`)
		err := cfgTestParseErr(t, []string{"-task", "search", "-state-dir", t.TempDir(), "-config", path}, nil)
		if !strings.Contains(err.Error(), "searxng_url") || !strings.Contains(err.Error(), path) {
			t.Errorf("error = %v, want the file source and key", err)
		}
	})

	t.Run("invalid environment is reported even when the flag wins", func(t *testing.T) {
		err := cfgTestParseErr(t, append(base, "-searxng-url", "https://good.example"),
			map[string]string{"AHARNESS_SEARXNG_URL": "http://public.example"})
		if !strings.Contains(err.Error(), "AHARNESS_SEARXNG_URL") {
			t.Errorf("error = %v, want the invalid environment layer", err)
		}
	})

	t.Run("invalid file is reported even when the flag wins", func(t *testing.T) {
		path := writeConfigFile(t, `{
			"model": "file-model",
			"workspace": "`+workspace+`",
			"searxng_url": "http://public.example"
		}`)
		err := cfgTestParseErr(t, []string{
			"-task", "search", "-state-dir", t.TempDir(), "-config", path,
			"-searxng-url", "https://good.example",
		}, nil)
		if !strings.Contains(err.Error(), "searxng_url") {
			t.Errorf("error = %v, want the invalid file layer", err)
		}
	})

	t.Run("non-string file value", func(t *testing.T) {
		path := writeConfigFile(t, `{
			"model": "file-model",
			"workspace": "`+workspace+`",
			"searxng_url": 5
		}`)
		err := cfgTestParseErr(t, []string{"-task", "search", "-state-dir", t.TempDir(), "-config", path}, nil)
		if !strings.Contains(err.Error(), "searxng_url") {
			t.Errorf("error = %v, want the offending key", err)
		}
	})

	for _, tc := range []struct {
		name string
		url  string
		want string
	}{
		{"scheme", "ftp://search.example", "scheme"},
		{"missing host", "https://", "host"},
		{"userinfo", "https://user@search.example", "userinfo"},
		{"query", "https://search.example/?q=1", "query"},
		{"fragment", "https://search.example/#frag", "fragment"},
		{"non-loopback http", "http://search.example", "loopback"},
		{"oversized", "https://search.example/" + strings.Repeat("a", 8<<10), "limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := cfgTestParseErr(t, append(base, "-searxng-url", tc.url), nil)
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestConfigSearXNGURLHelp(t *testing.T) {
	var out bytes.Buffer
	_, err := parseConfig([]string{"-h"}, cfgTestGetenv(nil), &out)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseConfig(-h) error = %v, want flag.ErrHelp", err)
	}
	help := out.String()
	for _, want := range []string{
		"-searxng-url",
		"AHARNESS_SEARXNG_URL",
		"precedence: flag > environment > config file > default",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("help missing %q:\n%s", want, help)
		}
	}
}
