package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
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
	"syscall"
	"time"
)

const version = "0.4.0"

const (
	harnessCodex  = "codex"
	harnessClaude = "claude"
)

type session struct {
	ID              string    `json:"id"`
	Target          string    `json:"target"`
	RemoteRoot      string    `json:"remote_root"`
	Workspace       string    `json:"workspace"`
	CodexSessionID  string    `json:"codex_session_id,omitempty"`
	ClaudeSessionID string    `json:"claude_session_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	LastUsedAt      time.Time `json:"last_used_at"`
}

type hookInput struct {
	SessionID     string         `json:"session_id"`
	HookEventName string         `json:"hook_event_name"`
	ToolName      string         `json:"tool_name"`
	ToolUseID     string         `json:"tool_use_id"`
	ToolInput     map[string]any `json:"tool_input"`
}

type options struct {
	target                     string
	root                       string
	resume                     bool
	resumeSession              string
	skipProbe                  bool
	noInstall                  bool
	harness                    string
	harnessSet                 bool
	dangerouslySkipPermissions bool
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
	if len(args) == 0 {
		return runSetup(os.Stdin, os.Stdout)
	}
	if len(args) > 0 {
		switch args[0] {
		case "__server", "__server-worker":
			return runServer(args, os.Stdin, os.Stdout)
		case "__hook":
			if len(args) != 2 && len(args) != 3 {
				return errors.New("invalid hook invocation")
			}
			harness := harnessCodex
			if len(args) == 3 {
				harness = args[2]
			}
			if err := runHook(args[1], harness, os.Stdin, os.Stdout); err != nil {
				if harness == harnessClaude {
					fmt.Fprintln(os.Stderr, "losh hook:", err)
					return exitCodeError{code: 2}
				}
				return err
			}
			return nil
		case "__exec-file":
			if len(args) != 3 {
				return errors.New("invalid remote execution invocation")
			}
			return runCommandFile(args[1], args[2])
		case "sessions":
			return listSessions(os.Stdout)
		case "setup":
			if len(args) != 1 {
				return errors.New("setup does not accept arguments")
			}
			return runSetup(os.Stdin, os.Stdout)
		case "version", "--version", "-v":
			fmt.Fprintln(os.Stdout, version)
			return nil
		case "help", "--help", "-h":
			usage(os.Stdout)
			return nil
		}
	}

	opts, harnessArgs, err := parseArgs(args)
	if err != nil {
		usage(os.Stderr)
		return err
	}
	if !opts.harnessSet {
		opts.harness, err = configuredHarness()
		if err != nil {
			return fmt.Errorf("load configuration: %w (run 'losh setup' to repair it)", err)
		}
	}
	opts.dangerouslySkipPermissions, err = configuredSkipPermissions()
	if err != nil {
		return fmt.Errorf("load configuration: %w (run 'losh setup' to repair it)", err)
	}
	return start(opts, harnessArgs)
}

func parseArgs(args []string) (options, []string, error) {
	out := options{harness: harnessCodex}
	var harnessArgs []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--":
			harnessArgs = append(harnessArgs, args[i+1:]...)
			i = len(args)
		case "--resume", "-r":
			out.resume = true
			if out.target != "" && i+1 < len(args) && args[i+1] != "--" && !strings.HasPrefix(args[i+1], "-") {
				i++
				out.resumeSession = args[i]
			}
		case "--skip-probe":
			out.skipProbe = true
		case "--no-install":
			out.noInstall = true
		case "--harness":
			if i+1 >= len(args) {
				return out, nil, errors.New("--harness requires codex or claude")
			}
			i++
			out.harness = args[i]
			out.harnessSet = true
		case "--root":
			if i+1 >= len(args) {
				return out, nil, errors.New("--root requires a path")
			}
			i++
			out.root = args[i]
		default:
			if strings.HasPrefix(args[i], "--resume=") {
				out.resume = true
				out.resumeSession = strings.TrimPrefix(args[i], "--resume=")
				if out.resumeSession == "" {
					return out, nil, errors.New("--resume= requires a session name or ID")
				}
				continue
			}
			if strings.HasPrefix(args[i], "-") {
				return out, nil, fmt.Errorf("unknown option %q (put harness options after --)", args[i])
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
	if out.harness != harnessCodex && out.harness != harnessClaude {
		return out, nil, fmt.Errorf("unsupported harness %q (choose codex or claude)", out.harness)
	}
	return out, harnessArgs, nil
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

func start(opts options, harnessArgs []string) error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return errors.New("OpenSSH client not found in PATH")
	}
	if _, err := exec.LookPath(opts.harness); err != nil {
		return fmt.Errorf("%s CLI not found in PATH; install and authenticate %s first", harnessDisplayName(opts.harness), harnessDisplayName(opts.harness))
	}

	s, err := ensureSession(opts.target, opts.root)
	if err != nil {
		return err
	}
	if err := materializeWorkspace(s, opts.harness); err != nil {
		return err
	}
	if !opts.skipProbe {
		fmt.Fprintf(os.Stderr, "Connecting to %s...\n", opts.target)
		if err := probe(s); err != nil {
			return fmt.Errorf("SSH probe failed: %w", err)
		}
	}
	if err := ensureRemoteServer(s, !opts.noInstall); err != nil {
		return err
	}

	args, err := harnessLaunchArgs(opts.harness, opts.resume, opts.resumeSession, opts.dangerouslySkipPermissions, harnessArgs, s)
	if err != nil {
		return err
	}
	cmd := exec.Command(opts.harness, args...)
	cmd.Dir = s.Workspace
	cmd.Env = append(os.Environ(), "LOSH_SESSION="+s.ID, "LOSH_TARGET="+s.Target, "LOSH_HARNESS="+opts.harness)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if !isNonInteractive(opts.harness, harnessArgs) {
		defer func() {
			latest := s
			if saved, err := loadSession(s.ID); err == nil {
				latest = saved
			}
			fmt.Fprintf(os.Stderr, "\nlosh: resume this remote session with:\n  %s\n", resumeHint(latest, opts.harness))
		}()
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

func codexBaseArgs(dangerouslySkipPermissions bool) []string {
	args := []string{"--sandbox", "workspace-write", "-c", "sandbox_workspace_write.network_access=true"}
	if dangerouslySkipPermissions {
		args = append(args, "--yolo")
	}
	return args
}

func codexLaunchArgs(resume bool, resumeSession string, dangerouslySkipPermissions bool, codexArgs []string) ([]string, error) {
	args := codexBaseArgs(dangerouslySkipPermissions)
	if !resume {
		return append(args, codexArgs...), nil
	}

	if len(codexArgs) > 0 && codexArgs[0] == "exec" {
		if resumeSession == "" {
			return nil, errors.New("--resume with codex exec requires a session name or ID")
		}
		args = append(args, "exec")
		remaining := codexArgs[1:]
		if len(remaining) > 0 {
			args = append(args, remaining[:len(remaining)-1]...)
			args = append(args, "resume", resumeSession, remaining[len(remaining)-1])
		} else {
			args = append(args, "resume", resumeSession)
		}
		return args, nil
	}

	args = append(args, codexArgs...)
	args = append(args, "resume")
	if resumeSession != "" {
		args = append(args, resumeSession)
	}
	return args, nil
}

func resumeHint(s session, harness string) string {
	parts := []string{"losh", shellQuote(s.Target)}
	if s.RemoteRoot != "" {
		parts = append(parts, "--root", shellQuote(s.RemoteRoot))
	}
	if harness != harnessCodex {
		parts = append(parts, "--harness", harness)
	}
	parts = append(parts, "--resume")
	sessionID := s.CodexSessionID
	if harness == harnessClaude {
		sessionID = s.ClaudeSessionID
	}
	if sessionID != "" {
		parts = append(parts, shellQuote(sessionID))
	}
	return strings.Join(parts, " ")
}

func probe(s session) error {
	baseArgs, err := sshBaseArgs(s)
	if err != nil {
		return err
	}
	args := append(baseArgs, s.Target, "sh -c 'command -v sh >/dev/null && printf LOSH_OK'")
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

func runHook(sessionID, harness string, in io.Reader, out io.Writer) error {
	var input hookInput
	if err := json.NewDecoder(in).Decode(&input); err != nil {
		return fmt.Errorf("decode hook input: %w", err)
	}

	if input.SessionID != "" {
		s, err := loadSession(sessionID)
		if err != nil {
			return err
		}
		changed := false
		if harness == harnessClaude && s.ClaudeSessionID != input.SessionID {
			s.ClaudeSessionID = input.SessionID
			changed = true
		} else if harness == harnessCodex && s.CodexSessionID != input.SessionID {
			s.CodexSessionID = input.SessionID
			changed = true
		}
		if changed {
			if err := saveSession(s); err != nil {
				return err
			}
		}
	}

	switch input.ToolName {
	case "Bash":
		command, ok := input.ToolInput["command"].(string)
		if !ok || command == "" {
			return errors.New("Bash hook did not contain a command")
		}
		callID := safeName(input.SessionID + "-" + input.ToolUseID)
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
	case "node_repl", "node_repl.js":
		if harness != harnessCodex {
			return nil
		}
		code, ok := input.ToolInput["code"].(string)
		if !ok || code == "" {
			return errors.New("node_repl hook did not contain code")
		}
		callID := safeName(input.SessionID + "-" + input.ToolUseID)
		if callID == "" {
			callID = shortHash(code + time.Now().UTC().String())
		}
		s, err := loadSession(sessionID)
		if err != nil {
			return err
		}
		stdout, stderr, err := remoteNodeREPLRunner(s, callID, code)
		if err != nil {
			reason := "losh could not run node_repl on the remote workspace: " + err.Error()
			if detail := strings.TrimSpace(string(stderr)); detail != "" {
				reason += "\n" + detail
			}
			return denyTool(out, reason)
		}
		replacement, err := nodeREPLDisplayCode(stdout, stderr)
		if err != nil {
			return denyTool(out, "losh received an invalid remote node_repl result: "+err.Error())
		}
		input.ToolInput["code"] = replacement
		input.ToolInput["title"] = "Remote node_repl result"
		return json.NewEncoder(out).Encode(map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":      "PreToolUse",
				"permissionDecision": "allow",
				"updatedInput":       input.ToolInput,
			},
		})
	case "apply_patch":
		patchText, ok := input.ToolInput["command"].(string)
		if !ok || patchText == "" {
			return errors.New("apply_patch hook did not contain a patch")
		}
		callID := safeName(input.SessionID + "-" + input.ToolUseID)
		if callID == "" {
			callID = shortHash(patchText + time.Now().UTC().String())
		}
		s, err := loadSession(sessionID)
		if err != nil {
			return err
		}
		result, patchErr := runRemotePatch(s, callID, patchText)
		reason := "losh-server applied this patch to the workspace successfully. Treat the patch as completed. " + result
		if patchErr != nil {
			reason = "losh-server did not apply this patch: " + patchErr.Error()
		}
		return json.NewEncoder(out).Encode(map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":            "PreToolUse",
				"permissionDecision":       "deny",
				"permissionDecisionReason": reason,
			},
		})
	case "Write", "Edit", "Read":
		if harness != harnessClaude {
			return nil
		}
		return runClaudeFileHook(sessionID, input, out)
	case "Glob", "Grep", "NotebookEdit":
		if harness != harnessClaude {
			return nil
		}
		return denyClaudeTool(out, "losh blocked this local workspace tool. Use Bash with a remote command instead; losh routes Bash to the target machine.")
	default:
		return nil
	}
}

const remoteNodeValueMarker = "__LOSH_NODE_VALUE__"

var remoteNodeREPLRunner = runRemoteNodeREPL

func denyTool(out io.Writer, reason string) error {
	return json.NewEncoder(out).Encode(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		},
	})
}

func remoteNodeScript(code string) string {
	return "const os = require(\"os\");\n" +
		"globalThis.nodeRepl = Object.freeze({\n" +
		"  cwd: process.cwd(),\n" +
		"  homeDir: os.homedir(),\n" +
		"  write(value) {\n" +
		"    const json = JSON.stringify(value === undefined ? null : value);\n" +
		"    process.stdout.write(" + fmt.Sprintf("%q", remoteNodeValueMarker) + " + Buffer.from(json).toString(\"base64\") + \"\\n\");\n" +
		"  }\n" +
		"});\n" +
		"(async () => {\n" + code + "\n" +
		"})().catch((error) => {\n" +
		"  console.error(error && error.stack ? error.stack : String(error));\n" +
		"  process.exitCode = 1;\n" +
		"});\n"
}

func runRemoteNodeREPL(s session, callID, code string) ([]byte, []byte, error) {
	command := "command -v node >/dev/null 2>&1 || { echo 'node is not installed on the remote target' >&2; exit 127; }\n" +
		"exec node -e " + shellQuote(remoteNodeScript(code))
	response, err := runRemoteOperation(s, callID, "exec", command)
	if err != nil {
		return response.Stdout, response.Stderr, err
	}
	if response.State == "uncertain" {
		return response.Stdout, response.Stderr, errors.New("remote node_repl outcome is uncertain; losh will not run it again automatically")
	}
	if response.State == "failed" {
		return response.Stdout, response.Stderr, fmt.Errorf("remote node_repl failed: %s", response.Result)
	}
	if response.ExitCode != 0 {
		return response.Stdout, response.Stderr, fmt.Errorf("remote node_repl exited with status %d", response.ExitCode)
	}
	return response.Stdout, response.Stderr, nil
}

func nodeREPLDisplayCode(stdout, stderr []byte) (string, error) {
	var value json.RawMessage
	var ordinary bytes.Buffer
	for _, line := range bytes.Split(stdout, []byte{'\n'}) {
		if bytes.HasPrefix(line, []byte(remoteNodeValueMarker)) {
			encoded := strings.TrimPrefix(string(line), remoteNodeValueMarker)
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return "", fmt.Errorf("decode nodeRepl.write value: %w", err)
			}
			if !json.Valid(decoded) {
				return "", errors.New("nodeRepl.write returned invalid JSON")
			}
			value = append(value[:0], decoded...)
			continue
		}
		if len(line) > 0 {
			ordinary.Write(line)
			ordinary.WriteByte('\n')
		}
	}

	logs := strings.TrimSpace(ordinary.String())
	errText := strings.TrimSpace(string(stderr))
	if len(value) > 0 && logs == "" && errText == "" {
		return "nodeRepl.write(" + string(value) + ")", nil
	}
	result := map[string]any{"remote": true}
	if len(value) > 0 {
		result["value"] = value
	}
	if logs != "" {
		result["stdout"] = logs
	}
	if errText != "" {
		result["stderr"] = errText
	}
	data, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return "nodeRepl.write(" + string(data) + ")", nil
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
	err = runRemoteCommand(s, callID, string(data), os.Stdout, os.Stderr)
	if err == nil {
		_ = os.Remove(callPath)
		return nil
	}
	return err
}

func remotePrelude(root string) string {
	if root == "" {
		return "cd -- \"$HOME\" || exit $?\n"
	}
	return "cd -- " + shellQuote(root) + " || exit $?\n"
}

func sshBaseArgs(s session) ([]string, error) {
	dir, err := ensureControlDirectory()
	if err != nil {
		return nil, fmt.Errorf("prepare SSH control directory: %w", err)
	}
	id := safeName(s.ID)
	if len(id) < 16 {
		return nil, errors.New("invalid session ID for SSH control socket")
	}
	control := filepath.Join(dir, id[:16])
	return []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPersist=10m",
		"-o", "ControlPath=" + control,
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=2",
		"-o", "ConnectTimeout=10",
		"-o", "ConnectionAttempts=1",
	}, nil
}

func sshCommandArgs(s session, trailing ...string) ([]string, error) {
	base, err := sshBaseArgs(s)
	if err != nil {
		return nil, err
	}
	return append(base, trailing...), nil
}

func ensureControlDirectory() (string, error) {
	// Hook commands run inside the harness sandbox. That sandbox can reuse a
	// socket under ~/.losh, but it cannot create a replacement there after a
	// sleeping laptop drops the original SSH master. A private, short-lived
	// directory under /tmp remains writable for reconnects and keeps Unix socket
	// paths comfortably below the macOS length limit.
	rootHash := shortHash(stateRoot())[:8]
	dir := filepath.Join("/tmp", fmt.Sprintf("losh-%d-%s", os.Getuid(), rootHash))
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is not a private directory", dir)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return "", fmt.Errorf("%s is not owned by the current user", dir)
	}
	if info.Mode().Perm() != 0700 {
		if err := os.Chmod(dir, 0700); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func ensureSession(target, root string) (session, error) {
	id := shortHash(target + "\x00" + root)
	path := filepath.Join(sessionDir(id), "session.json")
	if data, err := os.ReadFile(path); err == nil {
		var s session
		if err := json.Unmarshal(data, &s); err != nil {
			return s, err
		}
		s.Workspace = workspacePath(target, id)
		s.LastUsedAt = time.Now().UTC()
		return s, saveSession(s)
	} else if !errors.Is(err, os.ErrNotExist) {
		return session{}, err
	}

	now := time.Now().UTC()
	s := session{ID: id, Target: target, RemoteRoot: root, Workspace: workspacePath(target, id), CreatedAt: now, LastUsedAt: now}
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

func materializeWorkspace(s session, harness string) error {
	if err := os.MkdirAll(s.Workspace, 0700); err != nil {
		return err
	}
	if err := os.Chmod(s.Workspace, 0700); err != nil {
		return err
	}
	configDir := filepath.Join(s.Workspace, "."+harness)
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(configDir, 0700); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if harness == harnessClaude {
		return materializeClaudeWorkspace(s, exe, configDir)
	}
	hookCommand := shellQuote(exe) + " __hook " + shellQuote(s.ID) + " " + shellQuote(harnessCodex)
	hooks := map[string]any{
		"description": "Route Codex tool use through the active losh SSH session.",
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{
				"matcher": "^(startup|resume|clear|compact)$",
				"hooks":   []any{map[string]any{"type": "command", "command": hookCommand, "timeout": 10}}}},
			"PreToolUse": []any{map[string]any{
				"matcher": "^(Bash|apply_patch|node_repl(?:[.]js)?)$",
				"hooks":   []any{map[string]any{"type": "command", "command": hookCommand, "timeout": 600, "statusMessage": "Routing workspace operation through losh"}}}},
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
	instructions := fmt.Sprintf(`# losh workspace

- The workspace root is %q. Shell commands begin there.
- Use shell commands and apply_patch normally; losh routes their effects to the workspace.
- Do not inspect the harness metadata directory directly.
- Do not run ssh yourself. losh owns the workspace connection.
- Never print secrets merely to inspect them. Redact secret values from explanations and logs.
- Confirm consequential service, package, privilege, or destructive operations with the user.
`, rootDescription)
	if err := atomicWrite(filepath.Join(s.Workspace, "AGENTS.md"), []byte(instructions), 0400); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Join(s.Workspace, ".codex", "hooks.json"), 0400); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Join(s.Workspace, ".codex"), 0500); err != nil {
		return err
	}
	return os.Chmod(s.Workspace, 0500)
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

func workspacePath(target, id string) string {
	const maxLabelLength = 48
	var b strings.Builder
	lastReplacement := false
	for _, r := range target {
		if b.Len() >= maxLabelLength {
			break
		}
		allowed := r >= 'a' && r <= 'z' ||
			r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' ||
			r == '.' || r == '@' || r == '-' || r == '_'
		if allowed {
			b.WriteRune(r)
			lastReplacement = false
			continue
		}
		if !lastReplacement {
			b.WriteByte('_')
			lastReplacement = true
		}
	}
	label := strings.Trim(b.String(), ".@-_")
	if label == "" {
		label = "host"
	}
	name := label + "--" + id
	return filepath.Join(stateRoot(), "workspaces", name)
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
  losh
  losh [user@]host[:/remote/root] [--harness codex|claude] [--resume [CHAT]] [-- HARNESS_OPTIONS...]
  losh setup
  losh sessions
  losh version

Options:
  -r, --resume [CHAT]  Open the harness picker, or resume CHAT by name or ID
      --harness NAME   Override the configured harness with codex or claude
      --root PATH      Set the remote working root (useful for ambiguous targets)
      --skip-probe     Skip the initial SSH connectivity and shell probe
      --no-install     Fail instead of installing a missing/incompatible losh-server

Examples:
  losh                          # choose the default coding agent
  losh akrentsel@fuzz.foo.com
  losh prod:/srv/api --resume
  losh prod:/srv/api --resume fix-login
  losh prod:/srv/api --harness claude
  losh prod --root /srv/api -- --model gpt-5.6-sol`)
}

type exitCodeError struct{ code int }

func (e exitCodeError) Error() string { return fmt.Sprintf("process exited with status %d", e.code) }
