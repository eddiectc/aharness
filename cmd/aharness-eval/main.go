// Command aharness-eval measures an aharness binary against a task suite.
//
// It is development tooling, not part of the shipped runtime. It runs each
// task with a fresh workspace and state directory, executes the task's setup
// and verification scripts, extracts metrics from the resulting session JSONL,
// and writes per-run records plus a paired A/B summary.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	defaultTaskTimeout = 300 // seconds
	defaultRepeats     = 3
	defaultSeed        = 1
	tailLimit          = 4 << 10
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// arm names one harness binary under comparison.
type arm struct {
	Name string
	Path string
}

// config is the parsed command line.
type config struct {
	TasksDir       string
	Arms           []arm
	Model          string
	ThinkingEffort string
	Repeats        int
	Seed           int64
	PricesPath     string
	OutDir         string
}

// armsFlag collects repeated -arm name=path flags.
type armsFlag []string

func (a *armsFlag) String() string { return strings.Join(*a, ",") }

func (a *armsFlag) Set(value string) error {
	*a = append(*a, value)
	return nil
}

func parseConfig(args []string, stderr io.Writer) (config, error) {
	var (
		cfg  config
		arms armsFlag
	)
	fs := flag.NewFlagSet("aharness-eval", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&cfg.TasksDir, "tasks", "eval/tasks", "task suite directory")
	fs.Var(&arms, "arm", "arm as name=path; repeat for each binary (required)")
	fs.StringVar(&cfg.Model, "model", "", "model id passed to each arm (required)")
	fs.StringVar(&cfg.ThinkingEffort, "thinking-effort", "default", "thinking effort passed to each arm")
	fs.IntVar(&cfg.Repeats, "repeats", defaultRepeats, "repetitions per task and arm")
	fs.Int64Var(&cfg.Seed, "seed", defaultSeed, "seed for arm rotation and bootstrap resampling")
	fs.StringVar(&cfg.PricesPath, "prices", "eval/prices.json", "price file; empty disables it and relies on run_start pricing")
	fs.StringVar(&cfg.OutDir, "out", "eval/results/latest", "output directory for results.jsonl, summary.json, and summary.md")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	for _, raw := range arms {
		name, path, ok := strings.Cut(raw, "=")
		if !ok || name == "" || path == "" {
			return config{}, fmt.Errorf("invalid -arm %q: want name=path", raw)
		}
		cfg.Arms = append(cfg.Arms, arm{Name: name, Path: path})
	}
	if err := validateConfig(&cfg); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func validateConfig(cfg *config) error {
	if cfg.TasksDir == "" {
		return errors.New("tasks directory is required")
	}
	if cfg.Model == "" {
		return errors.New("model is required")
	}
	if cfg.ThinkingEffort == "" {
		return errors.New("thinking effort must not be empty (use \"default\" to clear)")
	}
	if cfg.Repeats <= 0 {
		return fmt.Errorf("repeats must be positive, got %d", cfg.Repeats)
	}
	if cfg.OutDir == "" {
		return errors.New("output directory is required")
	}
	if len(cfg.Arms) == 0 {
		return errors.New("at least one -arm name=path is required")
	}
	seen := make(map[string]bool, len(cfg.Arms))
	for i := range cfg.Arms {
		a := &cfg.Arms[i]
		if seen[a.Name] {
			return fmt.Errorf("duplicate arm name %q", a.Name)
		}
		seen[a.Name] = true
		abs, err := filepath.Abs(a.Path)
		if err != nil {
			return fmt.Errorf("resolve arm %q path %s: %w", a.Name, a.Path, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return fmt.Errorf("arm %q: %w", a.Name, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("arm %q path %s is not a regular file", a.Name, abs)
		}
		if info.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("arm %q path %s is not executable", a.Name, abs)
		}
		a.Path = abs
	}
	return nil
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	cfg, err := parseConfig(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "aharness-eval: %v\n", err)
		return 2
	}
	tasks, err := loadTasks(cfg.TasksDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness-eval: %v\n", err)
		return 1
	}
	prices, err := loadPriceFile(cfg.PricesPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness-eval: %v\n", err)
		return 1
	}
	records, err := execute(ctx, cfg, tasks, prices, stderr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness-eval: %v\n", err)
		return 1
	}
	doc := summarize(records, tasks, cfg, prices)
	if err := writeOutputs(cfg.OutDir, records, doc); err != nil {
		_, _ = fmt.Fprintf(stderr, "aharness-eval: %v\n", err)
		return 1
	}
	for _, a := range doc.Arms {
		_, _ = fmt.Fprintf(stdout, "%s: %d runs, %d solved, solve rate %s\n",
			a.Name, a.Runs, a.Solved, formatPercent(a.SolveRate))
	}
	_, _ = fmt.Fprintf(stdout, "wrote %s, %s, %s\n",
		filepath.Join(cfg.OutDir, "results.jsonl"),
		filepath.Join(cfg.OutDir, "summary.json"),
		filepath.Join(cfg.OutDir, "summary.md"))
	return 0
}
