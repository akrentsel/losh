# losh

**Local agent, remote machine.**

losh is a natural-language interface to any computer you can already reach
with SSH. It runs Codex or Claude Code locally but executes the
agent's actions on a selected remote host.

    $ losh akrentsel@fuzz.foo.com

    fuzz.foo.com · akrentsel · $HOME · codex

    > Find out why nginx is returning 502s. Explain the cause and ask before
      changing anything.

The model session, provider authentication, policy, and session index remain on
your computer. On first connection, losh uploads a small versioned server
through SSH into the remote user's home directory. It needs no sudo, model
credential, public port, or system service.

> [!WARNING]
> losh is currently a proof of concept. It builds and its unit tests pass, but
> it has not received the hardening required for production hosts.

## Why?

Running a coding agent directly on every server means installing and updating
it everywhere, copying login state to each host, consuming resources on small
machines, and scattering conversations across the fleet.

losh reverses that arrangement:

    ┌──────────────────────── local ─────────────────────────┐
    │ losh + Codex or Claude Code                            │
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

    # Pick a Codex conversation for this target and root
    losh prod:/srv/api --resume

    # Or resume one directly by name or ID
    losh prod:/srv/api --resume fix-login

    # Forward options to Codex
    losh prod:/srv/api -- --model gpt-5.6-sol

    # Run locally authenticated Claude Code instead
    losh prod:/srv/api --harness claude

    # Resume through Claude's picker, or select a name/ID directly
    losh prod:/srv/api --harness claude --resume
    losh prod:/srv/api --harness claude --resume fix-login

    # Inspect known sessions
    losh sessions

## Current status

Implemented:

- system OpenSSH targets and aliases from **~/.ssh/config**;
- automatic OS/architecture detection and atomic per-user server installation;
- bundled Linux/macOS AMD64/ARM64 server binaries;
- durable operation IDs, remote journals, detached command workers, and result replay;
- remote roots such as **prod:/srv/api**;
- Codex shell interception with remote stdout, stderr, and exit status;
- Claude Bash interception, atomic remote Read/Edit/Write operations, native
  picker/direct resume, and session-ID capture;
- blocking of Claude Glob, Grep, and NotebookEdit with instructions
  to use their remote Bash equivalents;
- server-side add/update/delete/move patches with path confinement, staging,
  atomic per-file publication, and rollback on reported commit errors;
- deterministic local session state, **--resume**, and session listing.

Current limitations:

- Codex's user-invoked shell mode bypasses tool hooks and currently runs in the
  local metadata workspace. Do not use it for remote commands. The tested
  `$SHELL` wrapper was not invoked; the feasibility result is documented in
  [Remote interfaces](docs/remote-interfaces.md#user-invoked-shell-mode-and-the-shell-wrapper-proposal).
- Codex's hook API cannot replace native `apply_patch` execution with a custom
  executor result. losh applies the patch remotely, blocks its local duplicate,
  and returns the confirmed result in the hook message; Codex continues
  correctly, but the UI labels the native call as blocked.
- Claude's native Read/Edit/Write calls are committed remotely and their local
  duplicates are denied. Claude receives the confirmed result in the denial
  reason, so the UI can label an operation blocked even though the remote
  operation succeeded. Glob, Grep, and NotebookEdit currently retry through
  Bash rather than preserving their native presentation.
- Claude command hooks fail open if the hook process itself is killed or reaches
  Claude's timeout; the read-only local metadata workspace is only a backstop.
- command output is returned when the command exits rather than streamed;
- interactive PTYs, stdin attachment, cancellation, and reconnectable terminals
  are not implemented;
- patch conflict checks currently use patch context, not read-version tokens;
- multi-file patches use staged per-file publication and best-effort rollback,
  not crash-recovered whole-tree transactions;
- losh-owned approvals, signed release artifacts, complete native-tool coverage,
  and production hardening remain to be implemented.

## Clean installation

Install losh on the **client** where Codex or Claude Code runs. Do not install it
manually on the target. The target needs SSH, `/bin/sh`, a supported
OS/architecture, and a writable home directory. The first `losh` connection
installs the matching server automatically through SSH.

The recommended installation is:

    brew install akrentsel/tap/losh

This automatically adds the `akrentsel/homebrew-tap` tap and installs losh.
The selected harness remains a separate client-side prerequisite. Install Codex
with `brew install --cask codex`, or install and authenticate Claude Code using
Anthropic's supported installer.

Run `losh` with no arguments after installation. Its setup screen explains the
local-harness/remote-target model, shows whether Codex and Claude Code are on
`PATH`, and saves the default harness in `~/.losh/config.json`. Running `losh`
again reopens setup. An explicit `--harness codex` or `--harness claude`
overrides the saved default for one connection.

### 1. Check the prerequisites

The current prototype requires macOS or Linux, OpenSSH, and at least one
installed/authenticated harness. Go 1.22 or newer is needed only for a source build:

    ssh -V
    go version
    codex --version
    codex login status

    claude --version
    claude auth status
Authenticate only the harnesses you intend to use: run `codex` for Codex, or
`claude auth login` for Claude Code. Then confirm that ordinary SSH works
before involving losh:

    ssh user@example.com true

### 2. Install from a source checkout

If Homebrew is unavailable, clone and install from source:

    git clone https://github.com/akrentsel/losh.git
    cd losh

    ./scripts/install.sh

The installer builds the client plus cross-platform server bundles, then copies
them under `$HOME/.local`. It does not use sudo, edit shell startup files, or
change SSH configuration. A target is changed only when you later connect with
`losh`, which installs the matching bundle in the remote user's home.

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
    losh
    codex login status
    ssh user@example.com true

### 4. Start and resume a session

    losh user@example.com

The setup-selected harness is used by default. To override it for one session:

    losh user@example.com:/srv/app --harness claude
    losh user@example.com:/srv/app --harness claude --resume

To work in a specific remote directory and later resume the same local
conversation:

    losh user@example.com:/srv/app
    losh user@example.com:/srv/app --resume

Session metadata and generated harness workspaces live under `$HOME/.losh`. List
them with `losh sessions`. The target stores the versioned helper and durable
operation records under its user account; it never receives model credentials
or either harness's conversation.

The source is published at **https://github.com/akrentsel/losh** and its formula
at **https://github.com/akrentsel/homebrew-tap**. After explicitly running
`brew tap akrentsel/tap`, the shorter `brew install losh` also works.

For alternate prefixes, noninteractive smoke testing, the reproduced
loshy2-to-loshy setup, troubleshooting, and uninstall instructions, see
**[Installing losh](docs/installation.md)**.

## Design

For the remote filesystem/process server, native-tool integration, and
disconnect recovery contract, see the
[remote interfaces design](docs/remote-interfaces.md). The current implementation
is the first usable slice of that design; later durability stages are marked
explicitly in the document.

### Keep the agent local

losh starts the selected harness locally in a stable, target-specific workspace.
That workspace is not a mirror of the remote filesystem; it contains only generated
instructions and hooks representing the remote environment.

This keeps model credentials off the server, keeps conversations available when
a server is replaced, and lets one local installation operate many remote
architectures.

### Keep the transport independent of the harness

Codex remains the implicit default; Claude Code is selected explicitly:

    losh user@example.com                       # --harness codex
    losh user@example.com --harness codex
    losh user@example.com --harness claude

`--harness claude` runs Claude Code locally; it does not install Claude on
the target. SSH execution, target identity, approvals, durable operations, and
audit records should remain in the shared losh core. Each harness adapter owns
only its local CLI invocation, instructions, tool interception, and resume
mechanism. Harness identity must be included in session metadata so a Codex
conversation is never accidentally resumed as a Claude Code conversation.

Arguments following `--` belong to the selected harness. losh refuses a
Claude `--settings` override because its generated settings file contains the
hooks that prevent workspace operations from silently running locally.

### Intercept tools, not system calls

Redirecting arbitrary processes or filesystem syscalls would turn losh into a
distributed operating system. Instead, it intercepts actions at the agent's
tool boundary, where their intent is explicit.

    Codex or Claude proposes a tool action
                  │
                  ▼
    generated tool hook + losh client coordinator
                  │ durable operation ID
                  ▼
    OpenSSH → losh-server → detached worker / filesystem transaction
                  │ remote journal + retained result
                  ▼
    confirmed stdout, exit status, read data, or edit result returns to the harness

Shell commands are stored briefly in a mode-0600 local call file so the rewritten
wrapper does not add a quoting boundary. losh-server records the accepted
operation before executing it, and a reconnect queries the same ID instead of
blindly rerunning it.

For `apply_patch`, the pre-hook sends the native patch to losh-server, waits for
its committed result, and denies the original local duplicate. This is safe for
the target and functional for Codex, although current Codex UI presents the
native tool call as blocked. The generated instructions otherwise describe an
ordinary workspace rather than teaching the model a separate remote workflow.


For Claude, Bash is rewritten through the same command wrapper. Read, Edit, and
Write become durable structured server operations; Edit uses exact
`old_string` matching and Write publishes a staged whole file atomically.
Because Claude hooks also cannot inject an arbitrary native success result,
losh blocks the local duplicate and returns the confirmed remote result in the
hook reason. Glob, Grep, and NotebookEdit are blocked and retried through Bash.
### Let OpenSSH remain OpenSSH

The prototype invokes the system ssh executable rather than reimplementing the
protocol. It therefore inherits aliases, Match blocks, ProxyJump, certificates,
hardware-backed keys, known_hosts, and interactive authentication.

Repeated operations use multiplexed SSH connections but remain independent
channels with separate output and exit status.

### Keep session identity local

State lives under **~/.losh**. Set LOSH_HOME to override it.

    ~/.losh/
    ├── config.json                 default local coding harness
    ├── sessions/<session-id>/
    │   ├── session.json            target, root, harness IDs, and timestamps
    │   └── calls/                  short-lived pending commands
    └── workspaces/<sanitized-target>--<session-id>/
        ├── AGENTS.md               generated Codex policy, when used
        ├── .codex/hooks.json       generated Codex hook, when used
        ├── CLAUDE.md               generated Claude policy, when used
        └── .claude/settings.json   generated Claude hooks, when used

Workspace labels retain ASCII letters, digits, `.`, `@`, `-`, and `_`;
Ephemeral OpenSSH multiplexing sockets live in a private `0700` runtime
directory named `/tmp/losh-<uid>-<state-hash>/`. Keeping them outside
`~/.losh` lets a sandboxed harness replace a dead master connection after
laptop sleep while retaining short Unix-socket paths on macOS.

other character runs become `_`, and labels are capped at 48 characters. The
full session ID remains in the name, preventing sanitized-label collisions.

The ID is derived from the literal SSH target and remote root. These are
therefore distinct:

    prod:$HOME
    prod:/srv/api
    deploy@prod:/srv/api

Each harness remains authoritative for its own conversation state. Bare
**--resume** opens the selected harness's native picker from the stable target
workspace. Pass a conversation name or ID after **--resume** to select it
directly. losh records supported session IDs from `SessionStart`; it does not
parse either harness's private transcript format.

### Bootstrap losh-server automatically

On every connection, losh checks the target OS, architecture, and installed
server version. If needed, it uploads the matching bundled binary and installs
it atomically at:

    ~/.local/lib/losh/server/<version>/losh-server

The helper runs through SSH and detached per-operation workers. It does not
listen on a public port, require sudo, or receive model credentials. Durable
operation records live under `~/.local/state/losh`. Use `--no-install` to fail
rather than installing or upgrading the helper. losh does not silently fall
back to weaker direct execution when reliable server mode is expected.

### Conversation and operation persistence

Codex and Claude conversation state remains local; **--resume** opens the
selected harness's picker, while **--resume NAME_OR_ID** restores a conversation directly. Accepted
commands run in detached server workers with remote status, output, and exit
records, so losing the SSH transport does not cause losh to submit a second
command. A harness `SessionStart` hook stores its supported `session_id`;
when the harness exits, losh prints a complete, harness-specific
**losh TARGET [--harness claude] --resume SESSION_ID** command for that exact
conversation. Streaming attachment and interactive PTYs remain
future work. A target reboot can still interrupt a worker; losh reports evidence
or uncertainty instead of relaunching it automatically.

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

The prototype implements only part of this model. Its hooks rewrite harness
calls, so losh-owned approval handling must be added before production use.
Instructions asking an agent to be cautious are not an
enforcement boundary.

## Open design questions

1. Which Codex integration can return a native successful patch result while
   keeping the target filesystem authoritative?
2. Should the default approval policy ask for every command, or permit a narrow
   read-only set?
3. Which read-version and target-identity tokens should bind later edits?
4. How should target epochs distinguish a rebuilt host from a reconnect?
5. How should sudo work without exposing passwords to the model?
6. Should each target/root have one conversation or named conversations?
7. When should detached workers become a persistent per-user supervisor?

## Near-term roadmap

1. Add read-version tokens and server-side stat/read/list/search operations.
2. Add crash recovery for interrupted multi-file commits.
3. Add streaming output, PTYs, stdin, cancellation, and job attachment.
4. Add losh-owned approvals, audit records, and target identity epochs.
5. Add signed release artifacts and broader failure-injection testing.
6. Expand Claude native Glob/Grep/Notebook coverage and conformance testing.

## Development

    make test    # unit tests and vet
    make build   # client plus cross-platform server bundles
    make install # install client and bundles under PREFIX

Tests cover argument handling, patch parsing and conflicts, path escapes,
journaling, and durable results. The opt-in SSH integration test exercises
bootstrap, commands, patches, and operation deduplication:

    LOSH_SERVER_BINARY=$PWD/bin/losh \
      LOSH_INTEGRATION_TARGET=user@host \
      go test ./cmd/losh -run TestRemoteServerIntegration -v

## Next step: unreliable networks

losh should treat a disappearing network as a normal event. The local harness
conversation already survives because its state is kept on the client, but an
in-flight remote action needs stronger semantics than simply retrying it: after
a disconnect, losh may not know whether the command ran.

The implemented baseline reconnects RPC calls, deduplicates requests with
durable operation IDs, runs accepted commands in detached workers, and replays
terminal output/status. SSH masters use keepalives and a sandbox-writable
runtime socket, so a tool call after laptop sleep can discard a dead transport
and establish a fresh connection without resubmitting an accepted operation.
The next transport milestone is streaming output by
offset, interactive attachment/cancellation, explicit target epochs, and
failure injection around every journal transition, reboot, and result-retention
boundary.

Mosh is worth evaluating later as an optional interactive terminal backend,
especially for a human taking over a PTY. It is not a transparent byte stream
for arbitrary command execution, so running SSH "over mosh" or parsing a mosh
terminal should not be losh's reliability layer. It also requires mosh-server
on the target and reachable UDP ports. Pure SSH plus resumable, identified
operations gives losh clearer correctness guarantees and retains the
zero-install target path; mosh can complement that design without becoming a
requirement.
