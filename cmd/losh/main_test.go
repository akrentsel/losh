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

func TestRemotePrelude(t *testing.T) {
	if got := remotePrelude(""); got != "cd -- \"$HOME\" || exit $?\n" {
		t.Fatalf("unexpected home prelude: %q", got)
	}
	if got := remotePrelude("/srv/it's here"); got != "cd -- '/srv/it'\"'\"'s here' || exit $?\n" {
		t.Fatalf("unexpected rooted prelude: %q", got)
	}
}
