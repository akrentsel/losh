package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type config struct {
	DefaultHarness string `json:"default_harness"`
}

func configPath() string {
	return filepath.Join(stateRoot(), "config.json")
}

func configuredHarness() (string, error) {
	data, err := os.ReadFile(configPath())
	if errors.Is(err, os.ErrNotExist) {
		return harnessCodex, nil
	}
	if err != nil {
		return "", err
	}
	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("decode %s: %w", configPath(), err)
	}
	if cfg.DefaultHarness != harnessCodex && cfg.DefaultHarness != harnessClaude {
		return "", fmt.Errorf("%s has unsupported default_harness %q", configPath(), cfg.DefaultHarness)
	}
	return cfg.DefaultHarness, nil
}

func saveConfiguredHarness(harness string) error {
	if harness != harnessCodex && harness != harnessClaude {
		return fmt.Errorf("unsupported harness %q", harness)
	}
	data, err := json.MarshalIndent(config{DefaultHarness: harness}, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(configPath(), append(data, '\n'), 0600)
}

func runSetup(in io.Reader, out io.Writer) error {
	current, err := configuredHarness()
	if err != nil {
		fmt.Fprintf(out, "Warning: existing configuration is invalid: %v\nIt will be replaced when you make a selection.\n\n", err)
		current = harnessCodex
	}

	fmt.Fprintln(out, "losh setup")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "losh keeps the coding agent, credentials, and conversation state on this")
	fmt.Fprintln(out, "machine. It sends shell and file operations to the target over SSH; the")
	fmt.Fprintln(out, "target does not need the coding agent or your model credentials installed.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Choose the coding agent to use when --harness is omitted:")
	printHarnessChoice(out, "1", harnessCodex, current)
	printHarnessChoice(out, "2", harnessClaude, current)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "You can change this later with 'losh setup' or override one connection")
	fmt.Fprintln(out, "with '--harness codex' or '--harness claude'.")

	defaultChoice := "1"
	if current == harnessClaude {
		defaultChoice = "2"
	}
	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprintf(out, "\nDefault coding agent [%s]: ", defaultChoice)
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return err
			}
			return errors.New("setup canceled; no preference was saved")
		}
		answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
		if answer == "" {
			answer = defaultChoice
		}
		selected := ""
		switch answer {
		case "1", "codex":
			selected = harnessCodex
		case "2", "claude", "claude-code":
			selected = harnessClaude
		default:
			fmt.Fprintln(out, "Please enter 1 for Codex or 2 for Claude Code.")
			continue
		}
		if err := saveConfiguredHarness(selected); err != nil {
			return err
		}
		fmt.Fprintf(out, "\nSaved. %s is now the default.\n", harnessDisplayName(selected))
		if _, err := exec.LookPath(selected); err != nil {
			fmt.Fprintf(out, "Note: %s is not currently on PATH; install and authenticate it before connecting.\n", harnessDisplayName(selected))
		}
		fmt.Fprintln(out, "Connect with: losh user@host")
		return nil
	}
}

func printHarnessChoice(out io.Writer, number, harness, current string) {
	status := "not found on PATH"
	if _, err := exec.LookPath(harness); err == nil {
		status = "installed"
	}
	marker := ""
	if harness == current {
		marker = ", current default"
	}
	fmt.Fprintf(out, "  %s) %-11s (%s%s)\n", number, harnessDisplayName(harness), status, marker)
}
