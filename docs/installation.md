# Installing losh

Install losh on the **client machine**, where Codex runs and where you initiate
SSH. On first use, the client automatically installs a small versioned
`losh-server` binary under the remote user's home directory.

## Topology

```text
client/login machine                           target machine
--------------------                           --------------
losh + authenticated Codex
~/.losh conversations/sessions  --- SSH --->   ~/.local/lib/losh/server/<version>/
bundled server binaries                        ~/.local/state/losh/ operation records
                                               project, services, logs
```

The target does not receive Codex, model credentials, conversation state, an
SSH agent, or a new public listening port. `losh-server` runs with the
permissions of the SSH account and needs no sudo or system service.

## Requirements

Client:

- macOS or Linux;
- system OpenSSH;
- Codex CLI installed and authenticated;
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
codex --version
codex login status
ssh user@target 'command -v sh && uname -s && uname -m'
```

If Codex is not authenticated, run `codex` and complete its login flow on the
client. See the [official OpenAI Codex quickstart](https://developers.openai.com/codex/quickstart).

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
Linux/macOS AMD64/ARM64. Codex is a separate client dependency:

```sh
brew install --cask codex
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
7. verifies the remote server version before starting Codex.

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
├── control/                     OpenSSH multiplexing sockets
├── sessions/<id>/
│   ├── session.json
│   └── calls/                   pending shell payloads
└── workspaces/<id>/
    ├── AGENTS.md                generated transparent workspace instructions
    └── .codex/hooks.json
```

Remote state:

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
# Remote user's home
losh user@target

# Explicit workspace
losh user@target:/srv/app
losh user@target --root /srv/app

# Pick a conversation for this target/root
losh user@target:/srv/app --resume

# Or resume one directly by name or ID
losh user@target:/srv/app --resume fix-login

# List local session records
losh sessions
```

Arguments after `--` go to Codex:

```sh
losh user@target:/srv/app -- --model gpt-5.6-sol
```

Codex may ask you to trust the generated project hook. Review that it invokes
the expected local losh executable.

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
workspace. Seeing a path such as `$HOME/.losh/workspaces/<session-id>` confirms
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
