package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestPatternsFromEnv_DefaultsWhenUnset(t *testing.T) {
	t.Setenv("MULTICA_GC_ARTIFACT_PATTERNS", "")
	defaults := []string{"node_modules", ".next", ".turbo"}
	got := patternsFromEnv("MULTICA_GC_ARTIFACT_PATTERNS", defaults)
	if !reflect.DeepEqual(got, defaults) {
		t.Fatalf("expected defaults %v, got %v", defaults, got)
	}
	// Ensure callers get a copy, not a shared backing array.
	got[0] = "mutated"
	if defaults[0] == "mutated" {
		t.Fatal("patternsFromEnv must not return a slice aliased with defaults")
	}
}

func TestPatternsFromEnv_DropsSeparatorBearingEntries(t *testing.T) {
	t.Setenv("MULTICA_GC_ARTIFACT_PATTERNS", "node_modules, .next ,foo/bar, ../etc, ,target")
	got := patternsFromEnv("MULTICA_GC_ARTIFACT_PATTERNS", nil)
	want := []string{"node_modules", ".next", "target"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestLoadConfigDetectsDroid(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable fixture is POSIX-only")
	}

	home := t.TempDir()
	binDir := t.TempDir()
	droidPath := filepath.Join(binDir, "droid")
	if err := os.WriteFile(droidPath, []byte("#!/bin/sh\nprintf '0.104.0\\n'\n"), 0o755); err != nil {
		t.Fatalf("write droid fixture: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", binDir)
	t.Setenv("MULTICA_DROID_MODEL", "claude-opus-4-7")
	t.Setenv("MULTICA_DROID_ARGS", `--auto low --reasoning-effort "medium"`)

	cfg, err := LoadConfig(Overrides{Profile: "multica67-test"})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	entry, ok := cfg.Agents["droid"]
	if !ok {
		t.Fatalf("expected droid agent in config, got %#v", cfg.Agents)
	}
	if entry.Path != "droid" {
		t.Fatalf("expected default droid path, got %q", entry.Path)
	}
	if entry.Model != "claude-opus-4-7" {
		t.Fatalf("unexpected droid model %q", entry.Model)
	}
	if strings.Join(cfg.DroidArgs, "\x00") != strings.Join([]string{"--auto", "low", "--reasoning-effort", "medium"}, "\x00") {
		t.Fatalf("unexpected droid args: %#v", cfg.DroidArgs)
	}
}
