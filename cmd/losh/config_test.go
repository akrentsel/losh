package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfiguredHarnessDefaultsAndPersists(t *testing.T) {
	t.Setenv("LOSH_HOME", t.TempDir())

	got, err := configuredHarness()
	if err != nil {
		t.Fatal(err)
	}
	if got != harnessClaude {
		t.Fatalf("default harness = %q, want claude", got)
	}

	if err := saveConfiguredHarness(harnessClaude); err != nil {
		t.Fatal(err)
	}
	got, err = configuredHarness()
	if err != nil {
		t.Fatal(err)
	}
	if got != harnessClaude {
		t.Fatalf("saved harness = %q, want claude", got)
	}
	info, err := os.Stat(configPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestRunSetupExplainsAndSavesSelection(t *testing.T) {
	t.Setenv("LOSH_HOME", t.TempDir())
	t.Setenv("PATH", "")
	var output strings.Builder
	if err := runSetup(strings.NewReader("invalid\n2\ny\n"), &output); err != nil {
		t.Fatal(err)
	}
	got, err := configuredHarness()
	if err != nil {
		t.Fatal(err)
	}
	if got != harnessClaude {
		t.Fatalf("configured harness = %q, want claude", got)
	}
	dangerous, err := configuredSkipPermissions()
	if err != nil {
		t.Fatal(err)
	}
	if !dangerous {
		t.Fatal("dangerously_skip_permissions was not saved")
	}
	for _, want := range []string{
		"credentials, and conversation state",
		"target does not need the coding agent",
		"Please enter 1 for Codex or 2 for Claude Code.",
		"Claude Code is now the default",
		"--yolo",
		"--dangerously-skip-permissions",
		"Native permission prompts will be skipped",
		"not currently on PATH",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("setup output missing %q:\n%s", want, output.String())
		}
	}
}

func TestConfiguredHarnessRejectsInvalidConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOSH_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"default_harness":"other"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := configuredHarness(); err == nil {
		t.Fatal("expected invalid default_harness to fail")
	}
}
