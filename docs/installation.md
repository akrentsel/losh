# Installing losh

Install losh on the **client machine**, where Codex or Claude Code runs and where
you initiate SSH. On first use, the client automatically installs a small versioned
`losh-server` binary under the remote user's home directory.

## Topology

```text
client/login machine                           target machine
--------------------                           --------------
losh + authenticated Codex and/or Claude Code
~/.losh conversations/sessions  --- SSH --->   ~/.local/lib/losh/server/<version>/
bundled server binaries                        ~/.local/state/losh/ operation records
                                               project, services, logs
```

The target does not receive either harness, model credentials, conversation state, an
SSH agent, or a new public listening port. `losh-server` runs with the
permissions of the SSH account and needs no sudo or system service.

## Requirements

Client:

- macOS or Linux;
- system OpenSSH;
- at least one supported harness installed and authenticated: Codex or Claude Code;
- network access and SSH credentials for the target;
- Go 1.22+ only for source builds.

Target:

- a reachable SSH server;
- Linux or macOS on AMD64 or ARM64;
- `/bin/sh`;
- a writable home directory and permission to execute the uploaded helper;
- permission for the requested commands/files.

Verify the client and connection:

```sh
ssh -V
claude --version
claude auth status
codex --version
codex login status
ssh user@target 'command -v sh && uname -s && uname -m'
```

Authenticate only the harnesses you plan to use. Run `codex` and complete its
login flow for Codex, or run `claude auth login` for Claude Code. See the
[official OpenAI Codex quickstart](https://developers.openai.com/codex/quickstart)
and [Claude Code authentication docs](https://code.claude.com/docs/en/authentication).

## Recommended: Homebrew

```sh
brew install akrentsel/tap/losh
```

Homebrew adds the tap automatically. Existing installations can be upgraded:

```sh
brew update
brew upgrade akrentsel/tap/losh
```

The formula builds the local client and bundles server executables for
Linux/macOS AMD64/ARM64. Harnesses are separate client dependencies:

```sh
brew install --cask codex

Install Claude Code using Anthropic's [supported setup](https://code.claude.com/docs/en/setup).
```

## Build and install from source

```sh
git clone https://github.com/akrentsel/losh.git
cd losh
./scripts/install.sh
```

This builds and installs:

```text
$HOME/.local/bin/losh
$HOME/.local/libexec/losh/servers/
  linux-amd64/losh-server
  linux-arm64/losh-server
  darwin-amd64/losh-server
  darwin-arm64/losh-server
```

Choose another prefix with `--prefix`:

```sh
./scripts/install.sh --prefix /usr/local
```

The installer does not use sudo, modify shell startup files, or connect to any
target. If the bin directory is not in `PATH`, it prints the export line to add.

To install prebuilt client/server artifacts:

```sh
./scripts/install.sh \
  --binary ./bin/losh \
  --servers ./bin/servers
```

If only a client binary is supplied, same-OS/same-architecture targets can use
that binary as the helper. Cross-platform targets require the matching bundle.

## Choose the default coding agent

Run losh without a target after installation:

```sh
losh
```

The setup screen explains which state stays local and which operations run on
the SSH target. It detects whether Codex and Claude Code are on `PATH` and saves
the selection in `$LOSH_HOME/config.json` (normally `~/.losh/config.json`). Run
`losh` or `losh setup` again to change it. The default remains Codex when no
configuration exists.

The saved choice applies whenever `--harness` is omitted. Override it for one
connection with `--harness codex` or `--harness claude`.

Development builds:

```sh
make test
make build
./bin/losh version
./bin/losh __server version
```

## First connection and automatic remote installation

Run:

```sh
losh user@target
```

Expected first-use messages resemble:

```text
Connecting to user@target...
Installing losh-server 0.2.0 for linux/amd64...
Installed losh-server.
```

The client:

1. connects with normal OpenSSH and host-key verification;
2. detects `uname -s` and `uname -m`;
3. checks the installed server version;
4. selects the bundled binary for the target;
5. uploads it through the authenticated SSH connection to a private temporary
   file;
6. sets executable permissions and atomically renames it into the versioned
   install path;
7. verifies the remote server version before starting the selected local harness.

The target does not need internet access. A later client version installs into
a different versioned directory, so active operations do not lose their
executable. losh never silently falls back to weaker direct shell execution.

Use `--no-install` when you want a missing or incompatible helper to be an
error:

```sh
losh user@target --no-install
```

The current development release supports `LOSH_SERVER_BINARY=/path/to/binary`
for testing an explicit server artifact.

## Remote and local state

Client state defaults to `$HOME/.losh` and can be moved with `LOSH_HOME`:

```text
~/.losh/
├── config.json                  default local coding harness
├── sessions/<id>/
│   ├── session.json
│   └── calls/                   pending shell payloads
└── workspaces/<sanitized-target>--<id>/
    ├── AGENTS.md                generated Codex instructions, when used
    ├── .codex/hooks.json        generated Codex hooks, when used
    ├── CLAUDE.md                generated Claude instructions, when used
    └── .claude/settings.json    generated Claude hooks, when used
```

Remote state:
OpenSSH multiplexing sockets are ephemeral rather than durable state. losh
creates them in a private `0700` directory named
`/tmp/losh-<uid>-<state-hash>/`. This location is short enough for macOS Unix
socket limits and writable by the harness sandbox, allowing the next tool call
to replace a dead SSH master after laptop sleep.


```text
~/.local/lib/losh/server/<version>/losh-server
~/.local/state/losh/operations/<operation-id>/
  request.json
  status.json
  stdout
  stderr
```

Remote operation records let the client query the same operation after an SSH
disconnect instead of resubmitting it. Commands run in detached workers.
Terminal output is currently returned at command completion; streaming and
interactive PTYs are still future work.

## Start, choose a root, and resume

```sh
# Open setup and choose the default harness
losh

# Remote user's home with the configured default harness
losh user@target

# Explicit remote workspace
losh user@target:/srv/app
losh user@target --root /srv/app

# Open Codex's picker, or resume a name/ID directly
losh user@target:/srv/app --resume
losh user@target:/srv/app --resume fix-login

# Start Claude Code locally against the same remote target
losh user@target:/srv/app --harness claude

# Open Claude's picker, or resume a name/ID directly
losh user@target:/srv/app --harness claude --resume
losh user@target:/srv/app --harness claude --resume fix-login

# Exit hints include the exact captured session ID and selected harness
losh user@target:/srv/app --resume 01abc-codex-session-id
losh user@target:/srv/app --harness claude --resume claude-session-id

# List local target/root records
losh sessions
```

Arguments after `--` go to the selected harness:

```sh
losh user@target:/srv/app -- --model gpt-5.6-sol
losh user@target:/srv/app --harness claude -- --model sonnet
```

Codex may ask you to trust the generated project hook. Claude receives its
generated hook file through `--settings`; losh rejects a user-supplied Claude
`--settings` argument because replacing that file would bypass remote routing.
In either case, review that hooks invoke the expected local losh executable.

## Claude Code tool behavior

With `--harness claude`, losh passes a generated settings file to the local
Claude CLI. Its hooks map tools as follows:

| Claude tool | losh behavior |
| --- | --- |
| `Bash` | Rewrites the command to the durable remote command wrapper |
| `Read` | Reads a confined remote file/range (files up to 16 MiB) and returns bounded numbered output |
| `Edit` | Exact remote `old_string` replacement, staged and atomically published |
| `Write` | Whole-file remote replacement, staged and atomically published |
| `Glob`, `Grep` | Blocks local execution; Claude is instructed to retry with remote Bash |
| `NotebookEdit` | Blocks local execution; use a remote Bash/Python command |

Claude hooks cannot replace a native tool call with an arbitrary successful
result. losh therefore completes Read/Edit/Write remotely, denies the local
duplicate, and places the confirmed result in the denial reason. A “blocked”
label in Claude's UI can therefore accompany a successful remote operation.
Subsequent remote verification is authoritative.

Ordinary hook errors exit with Claude's blocking status. However, Claude's
documented command-hook contract lets a tool continue through normal permission
handling if the hook process is killed or times out. The generated local
workspace is read-only as defense in depth, but this is not a complete
fail-closed boundary. Do not treat this proof of concept as hardened isolation.

## Current apply_patch behavior

Shell commands are rewritten to a local losh wrapper and executed as durable
remote server operations.

Codex's current hook API can observe/block/rewrite the `apply_patch` input,
but cannot substitute a custom executor result for the native patch tool. The
current adapter therefore:

1. sends the original patch to `losh-server`;
2. waits for a committed remote result;
3. blocks the duplicate local patch;
4. tells Codex that the remote patch completed.

Codex continues correctly, and subsequent shell verification sees the remote
change. The CLI currently prints the native call as blocked even when the hook
message says the remote patch succeeded. This is a known integration limitation,
not an indication that the confirmed remote edit failed.

The local metadata workspace is read-only between generated updates, reducing
the effect of a hook failure that might otherwise let the local patch continue.

## User-invoked Codex shell mode

Do not use Codex's direct user shell mode for remote work in the current
release. It bypasses the `Bash` tool hook and runs locally in losh's metadata
workspace. Seeing a path under `$HOME/.losh/workspaces/` confirms
that the command did not run on the target.

The tested Codex build ignored a session-specific `SHELL` wrapper, so losh does
not implement this approach. Shell mode will remain unsupported unless Codex
adds a supported user-shell interception point. losh will not shadow shell
binaries or silently execute these commands locally. See the detailed
[shell-wrapper proposal](remote-interfaces.md#user-invoked-shell-mode-and-the-shell-wrapper-proposal).

## Noninteractive smoke test

```sh
losh user@target -- \
  exec \
  --skip-git-repo-check \
  --sandbox danger-full-access \
  --dangerously-bypass-hook-trust \
  'Do not modify anything. Report hostname, user, and current directory.'
```

These Codex flags are for controlled development. `danger-full-access` removes
Codex's local shell sandbox, and bypassing hook trust runs generated hooks
without review.

Repository developers can run the direct integration test:

```sh
make build
LOSH_SERVER_BINARY=$PWD/bin/losh \
LOSH_INTEGRATION_TARGET=user@target \
go test ./cmd/losh -run TestRemoteServerIntegration -v
```

It installs/verifies the helper, creates a uniquely named temporary workspace,
executes a command, applies a patch, verifies deduplication, and removes that
test workspace.

## Uninstall

Remove a Homebrew client:

```sh
brew uninstall losh
```

Or remove a source installation:

```sh
rm $HOME/.local/bin/losh
rm -r $HOME/.local/libexec/losh
```

Inspect local state before removing it:

```sh
losh sessions
find $HOME/.losh -maxdepth 3 -type f -print
```

On a target, inspect remote state before removal:

```sh
find $HOME/.local/lib/losh $HOME/.local/state/losh -maxdepth 4 -type f -print
```

Removing remote server/state directories deletes retained operation results and
deduplication history. Do that only when no losh operation is running and no
client may reconnect to an unresolved operation.
