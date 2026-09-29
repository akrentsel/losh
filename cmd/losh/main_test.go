package main

import (
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
	s := session{Target: "deploy@example.com", RemoteRoot: "/srv/it's here"}
	got := resumeHint(s)
	want := "losh 'deploy@example.com' --root '/srv/it'\"'\"'s here' --resume"
	if got != want {
		t.Fatalf("resumeHint() = %q, want %q", got, want)
	}
}

func TestResumeHintHome(t *testing.T) {
	got := resumeHint(session{Target: "example.com"})
	want := "losh 'example.com' --resume"
	if got != want {
		t.Fatalf("resumeHint() = %q, want %q", got, want)
	}
}
