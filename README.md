# losh

**Local agent, remote machine.**

losh is a natural-language interface to any computer you can already reach
with SSH. It runs the coding agent locally—starting with Codex—but executes the
agent's actions on a selected remote host.

    $ losh akrentsel@fuzz.foo.com

    fuzz.foo.com · akrentsel · $HOME · codex

    > Find out why nginx is returning 502s. Explain the cause and ask before
      changing anything.

The model session, provider authentication, policy, and session index remain on
your computer. The remote machine needs only its existing SSH server and a
POSIX shell.

> [!WARNING]
> losh is currently a proof of concept. It builds and its unit tests pass, but
> it has not received the hardening required for production hosts.

## Why?

Running a coding agent directly on every server means installing and updating
it everywhere, copying login state to each host, consuming resources on small
machines, and scattering conversations across the fleet.

losh reverses that arrangement:

    ┌──────────────────────── local ─────────────────────────┐
    │ losh + Codex                                           │
    │ provider authentication · sessions · approvals         │
    │                                                       │
    │          exec · read · write · patch · processes       │
    └──────────────────────────┬────────────────────────────┘
                               │ existing SSH connection
    ┌──────────────────────────▼────────────────────────────┐
    │ remote machine                                      │
    │ shell · files · services · source · logs             │
    │ no model, API key, inbound port, or permanent agent  │
    └──────────────────────────────────────────────────────┘

The compatibility goal is:

> If **ssh user@host** works, **losh user@host** should work.

## Intended experience

    # Start in the remote user's home directory
    losh user@example.com

    # Start in a remote directory
    losh prod:/srv/api
    losh prod --root /srv/api

    # Resume the latest Codex conversation for this target and root
    losh prod:/srv/api --resume

    # Forward options to Codex
    losh prod:/srv/api -- --model gpt-5.6-sol

    # Inspect known sessions
    losh sessions

## Current status

Implemented:

- system OpenSSH targets and aliases from **~/.ssh/config**;
- remote roots such as **prod:/srv/api**;
- SSH connectivity and POSIX-shell probing;
- connection reuse with ControlMaster and ControlPersist;
- Codex PreToolUse interception for shell commands;
- propagation of remote stdout, stderr, and exit status;
- blocking local apply_patch in a remote session;
- deterministic per-target state under **~/.losh**;
- resume through Codex's supported **resume --last**;
- session listing.

Not implemented yet:

- structured remote read, write, and patch tools;
- interactive PTYs and reattachable background processes;
- the optional automatically uploaded helper;
- losh-owned approvals and audit logs;
- strong local process sandboxing;
- `--harness claude` and other coding-agent adapters;
- release binaries and a Homebrew tap.

## Clean installation

Install losh on the **client** where Codex runs. Do not install it on the SSH
target. The target needs only a working SSH server and `/bin/sh`.

The recommended installation is:

    brew install akrentsel/tap/losh

This automatically adds the `akrentsel/homebrew-tap` tap and installs losh.
Codex remains a separate client-side prerequisite; install it with
`brew install --cask codex` if needed.

### 1. Check the prerequisites

The current prototype requires macOS or Linux, OpenSSH, and an installed and
authenticated Codex CLI. Go 1.22 or newer is needed only for a source build:

    ssh -V
    go version
    codex --version
    codex login status

If Codex is not authenticated, run `codex` and complete its sign-in flow. Then
confirm that ordinary SSH works before involving losh:

    ssh user@example.com true

### 2. Install from a source checkout

If Homebrew is unavailable, clone and install from source:

    git clone https://github.com/akrentsel/losh.git
    cd losh

    ./scripts/install.sh

The installer builds losh and copies it to `$HOME/.local/bin/losh`. It does not
use sudo, edit shell startup files, change SSH configuration, or install
anything on the target.

If `$HOME/.local/bin` is not on PATH, the installer prints the export command.
Run it for the current shell, and add the same line to the appropriate shell
startup file if you want it to persist:

    export PATH="$HOME/.local/bin:$PATH"

If a trusted binary has already been built for this OS and architecture, Go is
not required:

    ./scripts/install.sh --binary ./bin/losh

### 3. Verify the installation

    command -v losh
    losh version
    codex login status
    ssh user@example.com true

### 4. Start and resume a session

    losh user@example.com

To work in a specific remote directory and later resume the same local Codex
conversation:

    losh user@example.com:/srv/app
    losh user@example.com:/srv/app --resume

Session metadata and generated Codex workspaces live under `$HOME/.losh`. List
them with `losh sessions`. The target receives commands over ordinary SSH; it
does not receive model credentials or persistent losh state.

The source is published at **https://github.com/akrentsel/losh** and its formula
at **https://github.com/akrentsel/homebrew-tap**. After explicitly running
`brew tap akrentsel/tap`, the shorter `brew install losh` also works.

For alternate prefixes, noninteractive smoke testing, the reproduced
loshy2-to-loshy setup, troubleshooting, and uninstall instructions, see
**[Installing losh](docs/installation.md)**.

## Design

For the proposed remote filesystem/process server, native-tool integration,
and disconnect recovery guarantees, see the
[remote interfaces design proposal](docs/remote-interfaces.md). It selects
structured remote operations as the preferred direction; the sections below
describe the current prototype and earlier design context.

### Keep the agent local

losh starts Codex locally in a stable, target-specific workspace. That workspace
is not a mirror of the remote filesystem; it contains only generated
instructions and hooks representing the remote environment.

This keeps model credentials off the server, keeps conversations available when
a server is replaced, and lets one local installation operate many remote
architectures.

### Keep the transport independent of the harness

Codex is the only supported harness today and is the implicit default. The
planned interface is:

    losh user@example.com                       # --harness codex
    losh user@example.com --harness codex
    losh user@example.com --harness claude      # planned, not implemented

`--harness claude` will run Claude Code locally; it will not install Claude on
the target. SSH execution, target identity, approvals, durable operations, and
audit records should remain in the shared losh core. Each harness adapter owns
only its local CLI invocation, instructions, tool interception, and resume
mechanism. Harness identity must be included in session metadata so a Codex
conversation is never accidentally resumed as a Claude Code conversation.

Arguments following `--` belong to the selected harness. Until harness
selection is implemented, passing `--harness` is an error rather than a silent
fallback to Codex.

### Intercept tools, not system calls

Redirecting arbitrary processes or filesystem syscalls would turn losh into a
distributed operating system. Instead, it intercepts actions at the agent's
tool boundary, where their intent is explicit.

    Codex proposes Bash("systemctl status api")
                  │
                  ▼
    generated PreToolUse hook
                  │ saves original command privately
                  │ rewrites tool input
                  ▼
    losh __exec-file <session> <call>
                  │
                  ▼
    system OpenSSH client → remote sh
                  │
                  ▼
    stdout + stderr + exit code return to Codex

The command is stored briefly in a mode-0600 file instead of being embedded in
another shell command. This avoids an extra quoting boundary. The file is
deleted after consumption.

Codex's built-in apply_patch is denied because rewriting its arguments would
still produce a local edit. Until a structured remote patch tool exists, Codex
is instructed to edit through remotely executed shell commands.

### Let OpenSSH remain OpenSSH

The prototype invokes the system ssh executable rather than reimplementing the
protocol. It therefore inherits aliases, Match blocks, ProxyJump, certificates,
hardware-backed keys, known_hosts, and interactive authentication.

Repeated operations use multiplexed SSH connections but remain independent
channels with separate output and exit status.

### Keep session identity local

State lives under **~/.losh**. Set LOSH_HOME to override it.

    ~/.losh/
    ├── control/                    OpenSSH multiplexing sockets
    ├── sessions/<session-id>/
    │   ├── session.json            target, root, and timestamps
    │   └── calls/                  short-lived pending commands
    └── workspaces/<session-id>/
        ├── AGENTS.md               generated remote policy
        └── .codex/hooks.json       generated interception hook

The ID is derived from the literal SSH target and remote root. These are
therefore distinct:

    prod:$HOME
    prod:/srv/api
    deploy@prod:/srv/api

Codex remains authoritative for conversation state. With --resume, losh runs
**codex resume --last** from the stable target workspace instead of parsing
Codex's private transcript format.

### Make the remote helper optional

Plain SSH is the compatibility contract. A future helper can add structured
files, atomic patches, PTYs, and process attachment, but must not be required.

Proposed startup:

1. Connect with ordinary SSH.
2. Detect OS, architecture, cache directory, and an existing helper version.
3. Reuse **~/.cache/losh/losh-server** when compatible.
4. Otherwise upload a checksummed binary over SSH and install it atomically.
5. Run **losh-server --stdio** as the SSH user.
6. Negotiate supported capabilities.
7. Fall back to pure SSH if any step fails.

Unlike mosh-server, this helper need not listen on another TCP or UDP port.
The protocol travels over SSH standard input/output. It needs neither sudo nor
a system service and never receives model credentials.

### Conversation persistence is not process persistence

Local conversation state can survive a disconnect immediately. An ordinary
remote process may still receive SIGHUP when its connection disappears.

Future durable-process backends could include existing tmux or screen, nohup
with PID/log tracking, user-level systemd-run, or PTYs managed by the optional
helper. --resume restores a conversation; a future attach operation reconnects
to a remote process.

## Security model

The eventual boundary must not depend solely on the model behaving well.

Requirements:

- never copy model credentials to the remote host;
- never enable SSH agent forwarding automatically;
- preserve normal OpenSSH host-key verification;
- bind a session to one target, user, and root;
- never let model-supplied arguments select arbitrary hosts;
- treat remote files, logs, names, and output as untrusted data;
- require approval for escalation and consequential changes;
- perform writes atomically with optimistic hash checks;
- maintain an inspectable local audit trail;
- sandbox the local agent as a backstop to hook coverage;
- encourage restricted remote accounts and OS-enforced permissions.

The prototype implements only part of this model. Its hook rewrites a Codex
call with an allow decision, so losh-owned approval handling must be added
before production use. Instructions asking Codex to be cautious are not an
enforcement boundary.

## Open design questions

1. Should pure-SSH mode remain first-class forever, or only bootstrap a helper?
2. Should the default approval policy ask for every command, or permit a narrow
   read-only set?
3. Should fallback file operations use SFTP, portable shell commands, or both?
4. Should sessions use the literal alias or resolved host-key fingerprint and
   username?
5. How should sudo work without exposing passwords to the model?
6. Should each target/root have one conversation or named conversations?
7. Should the helper be another mode of this binary or a smaller binary?

## Near-term roadmap

1. Add losh-owned approval handling.
2. Add structured read, write, stat, list, and hash-checked patch operations.
3. Add integration tests with an ephemeral SSH server and fake hook caller.
4. Add streaming PTYs, cancellation, and background-process tracking.
5. Implement the optional stdio helper with capability negotiation.
6. Add signed release artifacts and a Homebrew tap.
7. Add the `--harness` abstraction and a Claude Code adapter.

## Development

    make test    # go test ./...
    make build   # bin/losh
    make install # go install ./cmd/losh

Current tests cover target/root parsing, argument forwarding, working-directory
construction, and shell quoting. Integration tests are still needed.

## Next step: unreliable networks

losh should treat a disappearing network as a normal event. The local Codex
conversation already survives because its state is kept on the client, but an
in-flight remote action needs stronger semantics than simply retrying it: after
a disconnect, losh may not know whether the command ran.

The next transport milestone should be:

1. reconnect automatically for new actions and retry only when losh knows an
   action was not started;
2. give actions durable IDs and record their status, output, and exit code via
   the optional helper, making uncertain completion recoverable;
3. run long-lived work as durable remote jobs and support attach, poll, cancel,
   and output replay after reconnection;
4. test suspend/resume, packet loss, IP changes, and laptop sleep against an
   ephemeral SSH target.

Mosh is worth evaluating later as an optional interactive terminal backend,
especially for a human taking over a PTY. It is not a transparent byte stream
for arbitrary command execution, so running SSH "over mosh" or parsing a mosh
terminal should not be losh's reliability layer. It also requires mosh-server
on the target and reachable UDP ports. Pure SSH plus resumable, identified
operations gives losh clearer correctness guarantees and retains the
zero-install target path; mosh can complement that design without becoming a
requirement.
