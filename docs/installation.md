# Installing losh

losh is installed only on the **client machine**: the machine where Codex runs
and where you begin the SSH connection. The target machine does not need losh,
Codex, a model credential, an open port, or a system service.

## Topology

    client machine                       target machine
    ──────────────                       ──────────────
    losh
    authenticated Codex   ─── SSH ───▶   sshd + POSIX sh
    ~/.losh state                        project, services, logs

Before installing, this should already work from the client:

    ssh user@target true

## Requirements on the client

- macOS or Linux;
- OpenSSH client;
- Codex CLI installed and authenticated;
- network and SSH credentials for the target;
- Go 1.22+ only when building losh from source.

Confirm the first three:

    ssh -V
    codex --version
    codex login status

If Codex is not authenticated, start it and complete its sign-in flow:

    codex

The Codex installation and sign-in happen on the client, never on the target.
See the [official OpenAI Codex quickstart](https://developers.openai.com/codex/quickstart)
for current Codex setup instructions.

## Requirements on the target

The current pure-SSH backend requires:

- an SSH server reachable by the client;
- the intended remote account;
- a POSIX sh at /bin/sh;
- permission to perform whatever work the user requests.

Check it:

    ssh user@target 'command -v sh && printf TARGET_OK\n'

Nothing is installed remotely by the current prototype.

## Install today: build from source

Clone the public repository:

    git clone https://github.com/akrentsel/losh.git
    cd losh

    ./scripts/install.sh

This builds with Go and installs to:

    $HOME/.local/bin/losh

If that directory is not already in PATH, the installer prints the exact export
line to add to the shell configuration. The installer does not edit startup
files automatically.

Choose another prefix when needed:

    ./scripts/install.sh --prefix /usr/local

A system prefix may require running the installer with suitable permissions.
Prefer a user-owned prefix when possible.

## Install today: existing binary

If a trusted, architecture-compatible binary has already been built:

    ./scripts/install.sh --binary ./bin/losh

Or select a prefix:

    ./scripts/install.sh --binary ./bin/losh --prefix /opt/losh

The installer copies one executable. It does not create a daemon or modify the
SSH configuration.

To build the binary without installing it:

    make test
    make build
    ./bin/losh version

## Recommended installation: Homebrew

Install directly from the public tap:

    brew install akrentsel/tap/losh

Homebrew adds the tap automatically. Alternatively:

    brew tap akrentsel/tap
    brew install losh

The formula builds the tagged source release with Go as a build-time dependency
and installs the single `losh` executable. Codex remains a separate runtime
prerequisite and can be installed with `brew install --cask codex`.

## Verify after installation

On the client:

    command -v losh
    losh version
    codex login status
    ssh user@target true

Then start an interactive session:

    losh user@target

Start in a remote directory:

    losh user@target:/srv/app

Resume the most recent conversation for that exact target and root:

    losh user@target:/srv/app --resume

Inspect local session records:

    losh sessions

By default, state is stored under $HOME/.losh. Set LOSH_HOME to use another
location.

## What first run creates

Only the client changes:

    ~/.losh/
    ├── control/                    multiplexed SSH sockets
    ├── sessions/<id>/
    │   ├── session.json
    │   └── calls/                  short-lived command payloads
    └── workspaces/<id>/
        ├── AGENTS.md
        └── .codex/hooks.json

Codex may ask the user to trust the generated project hook. Review it and accept
only if its command points to the expected losh executable.

No losh directory is created on the target in pure-SSH mode.

## Noninteractive smoke test

For development and CI, Codex exec requires permission to run outside a Git
repository and the generated hook must be trusted explicitly:

    losh user@target -- \
      exec \
      --skip-git-repo-check \
      --sandbox danger-full-access \
      --dangerously-bypass-hook-trust \
      'Do not modify anything. Report hostname, user, and current directory.'

Those flags are intended for a controlled smoke test. In particular,
danger-full-access removes Codex's local shell sandbox for that run. This is
currently necessary because the local losh wrapper must open an SSH connection.
It is not the intended final approval design.

The production design needs a losh-owned approval boundary before rewritten
tool calls execute.

## Reproduced two-machine installation

The development topology used to verify this guide was:

    loshy2.exe.xyz                    loshy.exe.xyz
    ──────────────                    ─────────────
    /exe.dev/bin/losh
    Codex CLI 0.158.0   ─── SSH ───▶  exedev account
    ~/.losh sessions                  /bin/sh

The binary was copied to the client and installed:

    scp bin/losh loshy2.exe.xyz:/tmp/losh
    ssh loshy2.exe.xyz \
      'sudo install -m 0755 /tmp/losh /exe.dev/bin/losh && rm /tmp/losh'

Then, on loshy2:

    losh exedev@loshy.exe.xyz

The test confirmed that:

- Codex and ~/.losh state lived on loshy2;
- generated shell actions executed on loshy;
- --resume restored the same Codex session on loshy2;
- no losh executable or cache was installed on loshy.

The /exe.dev/bin path is specific to that test image, not a general
installation recommendation.

## Uninstall

Remove the installed executable:

    rm $HOME/.local/bin/losh

For a system prefix, remove the corresponding PREFIX/bin/losh with the required
permissions.

Local session state is separate. Inspect it before choosing to remove it:

    losh sessions
    find $HOME/.losh -maxdepth 2 -type f -print

Deleting $HOME/.losh removes losh's session index, generated workspaces, and
control sockets. Codex may also retain its own conversation records according
to Codex's storage behavior.

Nothing needs to be uninstalled from a pure-SSH target.
