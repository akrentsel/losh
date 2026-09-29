package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const version = "0.1.1"

type session struct {
	ID         string    `json:"id"`
	Target     string    `json:"target"`
	RemoteRoot string    `json:"remote_root"`
	Workspace  string    `json:"workspace"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
}

type hookInput struct {
	ToolName  string         `json:"tool_name"`
	ToolUseID string         `json:"tool_use_id"`
	ToolInput map[string]any `json:"tool_input"`
}

type options struct {
	target    string
	root      string
	resume    bool
	skipProbe bool
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		var exitErr exitCodeError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.code)
		}
		fmt.Fprintln(os.Stderr, "losh:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "__hook":
			if len(args) != 2 {
				return errors.New("invalid hook invocation")
			}
			return runHook(args[1], os.Stdin, os.Stdout)
		case "__exec-file":
			if len(args) != 3 {
				return errors.New("invalid remote execution invocation")
			}
			return runCommandFile(args[1], args[2])
		case "sessions":
			return listSessions(os.Stdout)
		case "version", "--version", "-v":
			fmt.Fprintln(os.Stdout, version)
			return nil
		case "help", "--help", "-h":
			usage(os.Stdout)
			return nil
		}
	}

	opts, codexArgs, err := parseArgs(args)
	if err != nil {
		usage(os.Stderr)
		return err
	}
	return start(opts.target, opts.root, opts.resume, opts.skipProbe, codexArgs)
}

func parseArgs(args []string) (options, []string, error) {
	var out options
	var codexArgs []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--":
			codexArgs = append(codexArgs, args[i+1:]...)
			i = len(args)
		case "--resume", "-r":
			out.resume = true
		case "--skip-probe":
			out.skipProbe = true
		case "--root":
			if i+1 >= len(args) {
				return out, nil, errors.New("--root requires a path")
			}
			i++
			out.root = args[i]
		default:
			if strings.HasPrefix(args[i], "-") {
				return out, nil, fmt.Errorf("unknown option %q (put Codex options after --)", args[i])
			}
			if out.target != "" {
				return out, nil, errors.New("only one SSH target may be specified")
			}
			out.target, out.root = splitTarget(args[i], out.root)
		}
	}
	if out.target == "" {
		return out, nil, errors.New("missing SSH target")
	}
	return out, codexArgs, nil
}

func splitTarget(value, explicitRoot string) (string, string) {
	if explicitRoot != "" {
		return value, explicitRoot
	}
	if i := strings.Index(value, ":/"); i >= 0 {
		return value[:i], value[i+1:]
	}
	return value, ""
}

func start(target, remoteRoot string, resume, skipProbe bool, codexArgs []string) error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return errors.New("OpenSSH client not found in PATH")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		return errors.New("Codex CLI not found in PATH; install and authenticate Codex first")
	}

	s, err := ensureSession(target, remoteRoot)
	if err != nil {
		return err
	}
	if err := materializeWorkspace(s); err != nil {
		return err
	}
	if !skipProbe {
		fmt.Fprintf(os.Stderr, "Connecting to %s...\n", target)
		if err := probe(s); err != nil {
			return fmt.Errorf("SSH probe failed: %w", err)
		}
	}

	args := codexBaseArgs()
	if resume {
		if len(codexArgs) > 0 && codexArgs[0] == "exec" {
			// codex exec keeps options before the resume subcommand and
			// accepts the last positional argument as the resumed prompt.
			args = append(args, "exec")
			remaining := codexArgs[1:]
			if len(remaining) > 0 {
				args = append(args, remaining[:len(remaining)-1]...)
				args = append(args, "resume", "--last", remaining[len(remaining)-1])
			} else {
				args = append(args, "resume", "--last")
			}
		} else {
			// Global Codex options precede the interactive resume subcommand.
			args = append(args, codexArgs...)
			args = append(args, "resume", "--last")
		}
	} else {
		args = append(args, codexArgs...)
	}
	cmd := exec.Command("codex", args...)
	cmd.Dir = s.Workspace
	cmd.Env = append(os.Environ(), "LOSH_SESSION="+s.ID, "LOSH_TARGET="+s.Target)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if len(codexArgs) == 0 || codexArgs[0] != "exec" {
		defer fmt.Fprintf(os.Stderr, "\nlosh: Codex's `codex resume` hint refers to the underlying harness.\nlosh: resume this remote session with:\n  %s\n", resumeHint(s))
	}
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitCodeError{code: exitErr.ExitCode()}
		}
		return err
	}
	return nil
}

func codexBaseArgs() []string {
	return []string{"--sandbox", "workspace-write", "-c", "sandbox_workspace_write.network_access=true"}
}

func resumeHint(s session) string {
	parts := []string{"losh", shellQuote(s.Target)}
	if s.RemoteRoot != "" {
		parts = append(parts, "--root", shellQuote(s.RemoteRoot))
	}
	parts = append(parts, "--resume")
	return strings.Join(parts, " ")
}

func probe(s session) error {
	args := append(sshBaseArgs(s), s.Target, "sh -c 'command -v sh >/dev/null && printf LOSH_OK'")
	cmd := exec.Command("ssh", args...)
	cmd.Stdin = os.Stdin
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	if string(out) != "LOSH_OK" {
		return fmt.Errorf("unexpected probe response %q", string(out))
	}
	return nil
}

func runHook(sessionID string, in io.Reader, out io.Writer) error {
	var input hookInput
	if err := json.NewDecoder(in).Decode(&input); err != nil {
		return fmt.Errorf("decode hook input: %w", err)
	}

	switch input.ToolName {
	case "Bash":
		command, ok := input.ToolInput["command"].(string)
		if !ok || command == "" {
			return errors.New("Bash hook did not contain a command")
		}
		callID := safeName(input.ToolUseID)
		if callID == "" {
			callID = shortHash(command + time.Now().UTC().String())
		}
		if err := saveCall(sessionID, callID, command); err != nil {
			return err
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		input.ToolInput["command"] = shellQuote(exe) + " __exec-file " + shellQuote(sessionID) + " " + shellQuote(callID)
		return json.NewEncoder(out).Encode(map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":      "PreToolUse",
				"permissionDecision": "allow",
				"updatedInput":       input.ToolInput,
			},
		})
	case "apply_patch":
		return json.NewEncoder(out).Encode(map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":            "PreToolUse",
				"permissionDecision":       "deny",
				"permissionDecisionReason": "This is a losh remote session. apply_patch would edit the local session workspace. Make the edit with a remote shell command instead.",
			},
		})
	default:
		return nil
	}
}

func runCommandFile(sessionID, callID string) error {
	s, err := loadSession(sessionID)
	if err != nil {
		return err
	}
	callPath := filepath.Join(sessionDir(sessionID), "calls", safeName(callID)+".txt")
	data, err := os.ReadFile(callPath)
	if err != nil {
		return fmt.Errorf("read pending command: %w", err)
	}
	defer os.Remove(callPath)

	script := remotePrelude(s.RemoteRoot) + string(data)
	remoteCommand := "sh -c " + shellQuote(script)
	args := append(sshBaseArgs(s), s.Target, remoteCommand)
	cmd := exec.Command("ssh", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = cmd.Run()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitCodeError{code: exitErr.ExitCode()}
	}
	return err
}

func remotePrelude(root string) string {
	if root == "" {
		return "cd -- \"$HOME\" || exit $?\n"
	}
	return "cd -- " + shellQuote(root) + " || exit $?\n"
}

func sshBaseArgs(s session) []string {
	control := filepath.Join(stateRoot(), "control", s.ID[:16])
	return []string{"-o", "ControlMaster=auto", "-o", "ControlPersist=10m", "-o", "ControlPath=" + control}
}

func ensureSession(target, root string) (session, error) {
	id := shortHash(target + "\x00" + root)
	path := filepath.Join(sessionDir(id), "session.json")
	if data, err := os.ReadFile(path); err == nil {
		var s session
		if err := json.Unmarshal(data, &s); err != nil {
			return s, err
		}
		s.LastUsedAt = time.Now().UTC()
		return s, saveSession(s)
	} else if !errors.Is(err, os.ErrNotExist) {
		return session{}, err
	}

	now := time.Now().UTC()
	s := session{ID: id, Target: target, RemoteRoot: root, Workspace: filepath.Join(stateRoot(), "workspaces", id), CreatedAt: now, LastUsedAt: now}
	return s, saveSession(s)
}

func loadSession(id string) (session, error) {
	var s session
	data, err := os.ReadFile(filepath.Join(sessionDir(safeName(id)), "session.json"))
	if err != nil {
		return s, fmt.Errorf("load session %s: %w", id, err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, err
	}
	return s, nil
}

func saveSession(s session) error {
	dir := sessionDir(s.ID)
	if err := os.MkdirAll(filepath.Join(dir, "calls"), 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(stateRoot(), "control"), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, "session.json"), append(data, '\n'), 0600)
}

func saveCall(sessionID, callID, command string) error {
	dir := filepath.Join(sessionDir(safeName(sessionID)), "calls")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, safeName(callID)+".txt"), []byte(command), 0600)
}

func materializeWorkspace(s session) error {
	if err := os.MkdirAll(filepath.Join(s.Workspace, ".codex"), 0700); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	hooks := map[string]any{
		"description": "Route Codex tool use through the active losh SSH session.",
		"hooks": map[string]any{
			"PreToolUse": []any{map[string]any{
				"matcher": "^(Bash|apply_patch)$",
				"hooks":   []any{map[string]any{"type": "command", "command": shellQuote(exe) + " __hook " + shellQuote(s.ID), "timeout": 30, "statusMessage": "Routing tool call through losh"}}}},
		},
	}
	hookData, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(s.Workspace, ".codex", "hooks.json"), append(hookData, '\n'), 0600); err != nil {
		return err
	}

	rootDescription := "$HOME"
	if s.RemoteRoot != "" {
		rootDescription = s.RemoteRoot
	}
	instructions := fmt.Sprintf(`# losh remote workspace

This Codex session operates on the remote SSH target %q, rooted at %q.

- Treat the remote target as the computer you are working on.
- Shell commands are transparently executed on the remote target by a trusted PreToolUse hook.
- Every shell command begins in the remote root. Use absolute paths or include an explicit cd when needed.
- Do not inspect or modify this small local workspace; it exists only to hold losh and Codex session metadata.
- The built-in apply_patch tool is intentionally blocked because it would edit local files. Make remote edits through shell commands. Prefer safe, atomic writes and inspect a file again before replacing it.
- Do not run ssh yourself. losh already owns the authenticated connection.
- Never print secrets merely to inspect them. Redact secret values from explanations and logs.
- State the target hostname before consequential service, package, privilege, or destructive operations.
`, s.Target, rootDescription)
	return atomicWrite(filepath.Join(s.Workspace, "AGENTS.md"), []byte(instructions), 0600)
}

func listSessions(out io.Writer) error {
	entries, err := os.ReadDir(filepath.Join(stateRoot(), "sessions"))
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(out, "No losh sessions.")
		return nil
	}
	if err != nil {
		return err
	}
	var sessions []session
	for _, entry := range entries {
		if entry.IsDir() {
			if s, err := loadSession(entry.Name()); err == nil {
				sessions = append(sessions, s)
			}
		}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].LastUsedAt.After(sessions[j].LastUsedAt) })
	fmt.Fprintln(out, "ID\tTARGET\tROOT\tLAST USED")
	for _, s := range sessions {
		root := s.RemoteRoot
		if root == "" {
			root = "$HOME"
		}
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", s.ID, s.Target, root, s.LastUsedAt.Local().Format(time.RFC3339))
	}
	return nil
}

func stateRoot() string {
	if value := os.Getenv("LOSH_HOME"); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".losh"
	}
	return filepath.Join(home, ".losh")
}

func sessionDir(id string) string { return filepath.Join(stateRoot(), "sessions", id) }

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:12])
}

func safeName(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".losh-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func usage(out io.Writer) {
	fmt.Fprintln(out, `Usage:
  losh [user@]host[:/remote/root] [--resume] [-- CODEX_OPTIONS...]
  losh sessions
  losh version

Options:
  -r, --resume       Resume Codex's most recent conversation for this target/root
      --root PATH    Set the remote working root (useful for ambiguous targets)
      --skip-probe   Skip the initial SSH connectivity and shell probe

Examples:
  losh akrentsel@fuzz.foo.com
  losh prod:/srv/api --resume
  losh prod --root /srv/api -- --model gpt-5.6-sol`)
}

type exitCodeError struct{ code int }

func (e exitCodeError) Error() string { return fmt.Sprintf("process exited with status %d", e.code) }
