package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitTarget(t *testing.T) {
	tests := []struct {
		name, value, explicitRoot, wantTarget, wantRoot string
	}{
		{name: "host only", value: "user@example.com", wantTarget: "user@example.com"},
		{name: "absolute root", value: "prod:/srv/api", wantTarget: "prod", wantRoot: "/srv/api"},
		{name: "explicit root wins", value: "prod:/ignored", explicitRoot: "/chosen", wantTarget: "prod:/ignored", wantRoot: "/chosen"},
		{name: "colon without slash is untouched", value: "example.com:2222", wantTarget: "example.com:2222"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, root := splitTarget(tt.value, tt.explicitRoot)
			if target != tt.wantTarget || root != tt.wantRoot {
				t.Fatalf("splitTarget(%q, %q) = (%q, %q), want (%q, %q)", tt.value, tt.explicitRoot, target, root, tt.wantTarget, tt.wantRoot)
			}
		})
	}
}

func TestShellQuote(t *testing.T) {
	tests := map[string]string{"": "''", "plain": "'plain'", "two words": "'two words'", "it's remote": "'it'\"'\"'s remote'"}
	for input, want := range tests {
		if got := shellQuote(input); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestWorkspacePath(t *testing.T) {
	tests := []struct {
		name, target, want string
	}{
		{name: "host", target: "loshy.exe.xyz", want: "loshy.exe.xyz--sessionid"},
		{name: "user and host", target: "exedev@loshy.exe.xyz", want: "exedev@loshy.exe.xyz--sessionid"},
		{name: "IPv6", target: "[2001:db8::1]", want: "2001_db8_1--sessionid"},
		{name: "path characters", target: "../../odd host/name", want: "odd_host_name--sessionid"},
		{name: "empty after sanitizing", target: "////", want: "host--sessionid"},
		{name: "truncated", target: strings.Repeat("a", 60), want: strings.Repeat("a", 48) + "--sessionid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := filepath.Base(workspacePath(tt.target, "sessionid")); got != tt.want {
				t.Fatalf("workspacePath(%q) basename = %q, want %q", tt.target, got, tt.want)
			}
		})
	}
}

func TestSSHControlPathSupportsSandboxReconnect(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOSH_HOME", home)
	s := session{ID: strings.Repeat("a", 24)}

	args, err := sshBaseArgs(s)
	if err != nil {
		t.Fatal(err)
	}
	controlPath := ""
	for _, arg := range args {
		if strings.HasPrefix(arg, "ControlPath=") {
			controlPath = strings.TrimPrefix(arg, "ControlPath=")
		}
	}
	if controlPath == "" {
		t.Fatalf("SSH args have no ControlPath: %#v", args)
	}
	if strings.HasPrefix(controlPath, home) {
		t.Fatalf("control path %q is inside sandbox-inaccessible LOSH_HOME", controlPath)
	}
	if !strings.HasPrefix(controlPath, "/tmp/losh-") {
		t.Fatalf("control path %q is not in the private runtime directory", controlPath)
	}
	if len(controlPath)+18 >= 104 {
		t.Fatalf("control path %q leaves no room for OpenSSH's temporary suffix", controlPath)
	}
	dir := filepath.Dir(controlPath)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("control directory permissions = %o, want 700", info.Mode().Perm())
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := sshBaseArgs(s); err != nil {
		t.Fatalf("recreate control directory after loss: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"ServerAliveInterval=15", "ServerAliveCountMax=2", "ConnectTimeout=10"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("SSH args missing %q: %s", want, joined)
		}
	}
}

func TestEnsureSessionMigratesWorkspaceName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("LOSH_HOME", home)
	target := "exedev@loshy.exe.xyz"
	root := "/srv/app"
	id := shortHash(target + "\x00" + root)
	oldWorkspace := filepath.Join(home, "workspaces", id)
	if err := saveSession(session{
		ID:         id,
		Target:     target,
		RemoteRoot: root,
		Workspace:  oldWorkspace,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := ensureSession(target, root)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "workspaces", "exedev@loshy.exe.xyz--"+id)
	if got.Workspace != want {
		t.Fatalf("ensureSession() workspace = %q, want %q", got.Workspace, want)
	}
}

func TestSessionStartHookCapturesCodexSessionID(t *testing.T) {
	t.Setenv("LOSH_HOME", t.TempDir())
	s, err := ensureSession("worker@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	input := `{"session_id":"01abc-session","hook_event_name":"SessionStart","source":"startup"}`
	var output strings.Builder
	if err := runHook(s.ID, harnessCodex, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	saved, err := loadSession(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.CodexSessionID != "01abc-session" {
		t.Fatalf("captured Codex session ID = %q, want %q", saved.CodexSessionID, "01abc-session")
	}
	if output.Len() != 0 {
		t.Fatalf("SessionStart hook output = %q, want no output", output.String())
	}
}

func TestParseArgs(t *testing.T) {
	opts, codexArgs, err := parseArgs([]string{"prod:/srv/api", "--resume", "--", "--model", "example"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.target != "prod" || opts.root != "/srv/api" || !opts.resume {
		t.Fatalf("unexpected options: %#v", opts)
	}
	if strings.Join(codexArgs, " ") != "--model example" {
		t.Fatalf("unexpected Codex args: %#v", codexArgs)
	}
}

func TestParseResumeForms(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantSession string
	}{
		{name: "picker", args: []string{"prod", "--resume"}},
		{name: "named", args: []string{"prod", "--resume", "fix-login"}, wantSession: "fix-login"},
		{name: "short named", args: []string{"prod", "-r", "01abc"}, wantSession: "01abc"},
		{name: "equals before target", args: []string{"--resume=fix-login", "prod"}, wantSession: "fix-login"},
		{name: "picker before target", args: []string{"--resume", "prod"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, _, err := parseArgs(tt.args)
			if err != nil {
				t.Fatal(err)
			}
			if !opts.resume || opts.resumeSession != tt.wantSession || opts.target != "prod" {
				t.Fatalf("unexpected options: %#v", opts)
			}
		})
	}

	if _, _, err := parseArgs([]string{"prod", "--resume="}); err == nil {
		t.Fatal("expected empty --resume= value to fail")
	}
}

func TestCodexLaunchArgs(t *testing.T) {
	tests := []struct {
		name          string
		resume        bool
		resumeSession string
		codexArgs     []string
		wantSuffix    string
	}{
		{name: "new conversation", codexArgs: []string{"--model", "example"}, wantSuffix: "--model example"},
		{name: "resume picker", resume: true, wantSuffix: "resume"},
		{name: "resume named", resume: true, resumeSession: "fix-login", wantSuffix: "resume fix-login"},
		{
			name:          "resume named with global option",
			resume:        true,
			resumeSession: "fix-login",
			codexArgs:     []string{"--model", "example"},
			wantSuffix:    "--model example resume fix-login",
		},
		{
			name:          "exec resume",
			resume:        true,
			resumeSession: "01abc",
			codexArgs:     []string{"exec", "--json", "continue working"},
			wantSuffix:    "exec --json resume 01abc continue working",
		},
	}
	base := strings.Join(codexBaseArgs(), " ")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := codexLaunchArgs(tt.resume, tt.resumeSession, tt.codexArgs)
			if err != nil {
				t.Fatal(err)
			}
			want := strings.TrimSpace(base + " " + tt.wantSuffix)
			if strings.Join(got, " ") != want {
				t.Fatalf("codexLaunchArgs() = %q, want %q", strings.Join(got, " "), want)
			}
			if strings.Contains(strings.Join(got, " "), "--last") {
				t.Fatal("resume unexpectedly used --last")
			}
		})
	}

	if _, err := codexLaunchArgs(true, "", []string{"exec", "prompt"}); err == nil {
		t.Fatal("expected picker-style exec resume to fail")
	}
}

func TestRemotePrelude(t *testing.T) {
	if got := remotePrelude(""); got != "cd -- \"$HOME\" || exit $?\n" {
		t.Fatalf("unexpected home prelude: %q", got)
	}
	if got := remotePrelude("/srv/it's here"); got != "cd -- '/srv/it'\"'\"'s here' || exit $?\n" {
		t.Fatalf("unexpected rooted prelude: %q", got)
	}
}

func TestCodexBaseArgsEnableNetwork(t *testing.T) {
	got := strings.Join(codexBaseArgs(), " ")
	want := "--sandbox workspace-write -c sandbox_workspace_write.network_access=true"
	if got != want {
		t.Fatalf("codexBaseArgs() = %q, want %q", got, want)
	}
}

func TestResumeHint(t *testing.T) {
	s := session{Target: "deploy@example.com", RemoteRoot: "/srv/it's here", CodexSessionID: "01abc-session"}
	got := resumeHint(s, harnessCodex)
	want := "losh 'deploy@example.com' --root '/srv/it'\"'\"'s here' --resume '01abc-session'"
	if got != want {
		t.Fatalf("resumeHint() = %q, want %q", got, want)
	}
}

func TestResumeHintHome(t *testing.T) {
	got := resumeHint(session{Target: "example.com"}, harnessCodex)
	want := "losh 'example.com' --resume"
	if got != want {
		t.Fatalf("resumeHint() = %q, want %q", got, want)
	}
}
