package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseClaudeHarness(t *testing.T) {
	opts, args, err := parseArgs([]string{"prod:/srv/api", "--harness", "claude", "--", "--model", "sonnet"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.harness != harnessClaude || !opts.harnessSet || opts.target != "prod" || opts.root != "/srv/api" {
		t.Fatalf("unexpected options: %#v", opts)
	}
	if strings.Join(args, " ") != "--model sonnet" {
		t.Fatalf("unexpected harness args: %#v", args)
	}

	defaults, _, err := parseArgs([]string{"prod"})
	if err != nil {
		t.Fatal(err)
	}
	if defaults.harness != harnessClaude {
		t.Fatalf("default harness = %q, want claude", defaults.harness)
	}
	if _, _, err := parseArgs([]string{"prod", "--harness", "unknown"}); err == nil {
		t.Fatal("expected unsupported harness to fail")
	}
}

func TestClaudeLaunchArgs(t *testing.T) {
	s := session{Workspace: "/tmp/losh workspace"}
	got, err := harnessLaunchArgs(harnessClaude, true, "session-name", false, []string{"--model", "sonnet"}, s)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--settings", "/tmp/losh workspace/.claude/settings.json", "--model", "sonnet", "--resume", "session-name", "--mcp-config", "/tmp/losh workspace/.claude/mcp.json"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("Claude args = %#v, want %#v", got, want)
	}
	dangerous, err := harnessLaunchArgs(harnessClaude, false, "", true, nil, s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(dangerous, " "), "--dangerously-skip-permissions") {
		t.Fatalf("Claude dangerous args = %#v", dangerous)
	}
	promptArgs, err := harnessLaunchArgs(harnessClaude, false, "", false, []string{"initial prompt"}, s)
	if err != nil {
		t.Fatal(err)
	}
	if got := promptArgs[len(promptArgs)-3:]; strings.Join(got, "|") != "initial prompt|--mcp-config|/tmp/losh workspace/.claude/mcp.json" {
		t.Fatalf("Claude prompt/MCP args = %#v", promptArgs)
	}
	if _, err := harnessLaunchArgs(harnessClaude, false, "", false, []string{"--settings", "unsafe.json"}, s); err == nil {
		t.Fatal("expected Claude settings override to fail")
	}
}

func TestClaudeSessionStartAndResumeHint(t *testing.T) {
	t.Setenv("LOSH_HOME", t.TempDir())
	s, err := ensureSession("worker@example.com", "/srv/app")
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	input := `{"session_id":"claude-session-123","hook_event_name":"SessionStart","source":"startup"}`
	if err := runHook(s.ID, harnessClaude, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	saved, err := loadSession(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ClaudeSessionID != "claude-session-123" || saved.CodexSessionID != "" {
		t.Fatalf("unexpected session IDs: %#v", saved)
	}
	want := "losh 'worker@example.com' --root '/srv/app' --harness claude --resume 'claude-session-123'"
	if got := resumeHint(saved, harnessClaude); got != want {
		t.Fatalf("resumeHint() = %q, want %q", got, want)
	}
}

func TestMaterializeClaudeWorkspace(t *testing.T) {
	t.Setenv("LOSH_HOME", t.TempDir())
	s, err := ensureSession("worker@example.com", "/srv/app")
	if err != nil {
		t.Fatal(err)
	}
	if err := materializeWorkspace(s, harnessClaude); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chmod(s.Workspace, 0700)
		_ = os.Chmod(filepath.Join(s.Workspace, ".claude"), 0700)
		_ = os.Chmod(filepath.Join(s.Workspace, ".claude", "settings.json"), 0600)
		_ = os.Chmod(filepath.Join(s.Workspace, ".claude", "mcp.json"), 0600)
		_ = os.Chmod(filepath.Join(s.Workspace, "CLAUDE.md"), 0600)
	}()
	data, err := os.ReadFile(filepath.Join(s.Workspace, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	mcpData, err := os.ReadFile(filepath.Join(s.Workspace, ".claude", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mcpData), "__mcp") || !strings.Contains(string(mcpData), s.ID) {
		t.Fatalf("unexpected MCP config: %s", mcpData)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok || hooks["SessionStart"] == nil || hooks["PreToolUse"] == nil {
		t.Fatalf("generated settings missing hooks: %#v", settings)
	}
	instructions, err := os.ReadFile(filepath.Join(s.Workspace, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(instructions), "/srv/app") || !strings.Contains(string(instructions), "Speak from that machine's perspective") {
		t.Fatalf("unexpected CLAUDE.md: %s", instructions)
	}
}

func TestClaudeBlockedLocalTool(t *testing.T) {
	t.Setenv("LOSH_HOME", t.TempDir())
	s, err := ensureSession("worker@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	input := `{"session_id":"claude-1","hook_event_name":"PreToolUse","tool_name":"Glob","tool_use_id":"tool-1","tool_input":{"pattern":"**/*.go"}}`
	if err := runHook(s.ID, harnessClaude, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"permissionDecision":"deny"`) || !strings.Contains(output.String(), "Use Bash") {
		t.Fatalf("unexpected hook output: %s", output.String())
	}
}

func TestLimitClaudeHookResult(t *testing.T) {
	short := "small"
	if got := limitClaudeHookResult(short); got != short {
		t.Fatalf("short result = %q", got)
	}
	large := strings.Repeat("é", 8000)
	got := limitClaudeHookResult(large)
	if !strings.Contains(got, "output truncated") {
		t.Fatalf("large result did not contain truncation notice")
	}
	if len([]rune(got)) != 7500 {
		t.Fatalf("limited result has %d runes, want 7500", len([]rune(got)))
	}
	if strings.ContainsRune(got, '�') {
		t.Fatal("limiter split a Unicode code point")
	}
}

func TestRemoteToolPath(t *testing.T) {
	s := session{Workspace: "/local/workspace", RemoteRoot: "/srv/app"}
	tests := map[string]string{
		"relative/file.go":             "relative/file.go",
		"/local/workspace/src/main.go": "src/main.go",
		"/srv/app/internal/service.go": "internal/service.go",
	}
	for input, want := range tests {
		got, err := remoteToolPath(s, input)
		if err != nil {
			t.Fatalf("remoteToolPath(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("remoteToolPath(%q) = %q, want %q", input, got, want)
		}
	}
	for _, input := range []string{"../escape", "/etc/passwd", "/local/workspace"} {
		if _, err := remoteToolPath(s, input); err == nil {
			t.Fatalf("remoteToolPath(%q) unexpectedly succeeded", input)
		}
	}
}

func TestApplyClaudeFileOperations(t *testing.T) {
	root := t.TempDir()
	opDir := filepath.Join(t.TempDir(), "operation")
	if err := os.MkdirAll(opDir, 0700); err != nil {
		t.Fatal(err)
	}
	writePayload := marshalClaudeOperation(t, claudeFileOperation{Path: "nested/file.txt", Content: "alpha\nbeta\ngamma\n"})
	result, err := applyClaudeFileOperation(root, "fs-write", writePayload, opDir)
	if err != nil {
		t.Fatal(err)
	}
	if result != "wrote nested/file.txt" {
		t.Fatalf("write result = %q", result)
	}

	readPayload := marshalClaudeOperation(t, claudeFileOperation{Path: "nested/file.txt", Offset: 2, Limit: 1})
	result, err = applyClaudeFileOperation(root, "fs-read", readPayload, opDir)
	if err != nil {
		t.Fatal(err)
	}
	if result != "     2→beta\n" {
		t.Fatalf("read result = %q", result)
	}

	editPayload := marshalClaudeOperation(t, claudeFileOperation{Path: "nested/file.txt", OldString: "beta", NewString: "BETA"})
	result, err = applyClaudeFileOperation(root, "fs-edit", editPayload, opDir)
	if err != nil {
		t.Fatal(err)
	}
	if result != "edited nested/file.txt" {
		t.Fatalf("edit result = %q", result)
	}
	assertTestFile(t, filepath.Join(root, "nested", "file.txt"), "alpha\nBETA\ngamma\n")

	ambiguous := marshalClaudeOperation(t, claudeFileOperation{Path: "nested/file.txt", OldString: "a", NewString: "x"})
	if _, err := applyClaudeFileOperation(root, "fs-edit", ambiguous, opDir); err == nil {
		t.Fatal("expected ambiguous edit to fail")
	}
	assertTestFile(t, filepath.Join(root, "nested", "file.txt"), "alpha\nBETA\ngamma\n")
}

func marshalClaudeOperation(t *testing.T, operation claudeFileOperation) string {
	t.Helper()
	data, err := json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestClaudeResourceListAndRawRead(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "secret"), []byte("hidden"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "database.db"), []byte("SQLite format 3\x00binary"), 0600); err != nil {
		t.Fatal(err)
	}
	listPayload := marshalClaudeOperation(t, claudeFileOperation{MaxEntries: 100})
	result, err := applyClaudeFileOperation(root, "fs-list", listPayload, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var listing mcpResourceList
	if err := json.Unmarshal([]byte(result), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Files) != 1 || listing.Files[0].Path != "src/main.go" {
		t.Fatalf("unexpected resource list: %#v", listing)
	}

	readPayload := marshalClaudeOperation(t, claudeFileOperation{Path: "src/main.go", Raw: true})
	result, err = applyClaudeFileOperation(root, "fs-read", readPayload, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if result != "package main\n" {
		t.Fatalf("raw resource = %q", result)
	}
}

func TestClaudeResourceURI(t *testing.T) {
	uri := claudeResourceURI("src/a file.go")
	if uri != "losh://workspace/src/a%20file.go" {
		t.Fatalf("resource URI = %q", uri)
	}
	path, err := claudeResourcePath(uri)
	if err != nil || path != filepath.Join("src", "a file.go") {
		t.Fatalf("resource path = %q, %v", path, err)
	}
	for _, invalid := range []string{"file:///etc/passwd", "losh://workspace/../secret", "losh://other/file"} {
		if _, err := claudeResourcePath(invalid); err == nil {
			t.Fatalf("claudeResourcePath(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestClaudeMCPTranscript(t *testing.T) {
	t.Setenv("LOSH_HOME", t.TempDir())
	s, err := ensureSession("worker@example.com", "/srv/app")
	if err != nil {
		t.Fatal(err)
	}
	previous := mcpRemoteFileOperation
	mcpRemoteFileOperation = func(_ session, _ string, kind, payload string) (string, error) {
		switch kind {
		case "fs-list":
			return `{"files":[{"path":"src/main.go","size":13}]}`, nil
		case "fs-read":
			var op claudeFileOperation
			if err := json.Unmarshal([]byte(payload), &op); err != nil {
				return "", err
			}
			if op.Path != "src/main.go" || !op.Raw {
				t.Fatalf("unexpected read operation: %#v", op)
			}
			return "package main\n", nil
		default:
			t.Fatalf("unexpected operation kind %q", kind)
			return "", nil
		}
	}
	defer func() { mcpRemoteFileOperation = previous }()

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/read","params":{"uri":"losh://workspace/src/main.go"}}`,
	}, "\n")
	var output strings.Builder
	if err := runClaudeMCP(s.ID, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, want := range []string{"2025-06-18", "losh://workspace/src/main.go", "package main\\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("MCP output missing %q: %s", want, got)
		}
	}
}
