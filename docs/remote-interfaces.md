# Remote process and filesystem server

Status: accepted design with an initial implementation, revised 2026-09-29.
Sections describe the target contract. Explicit “implemented baseline” notes
identify what exists today; remaining durability and interface details are
still proposed.

## 1. Preferred direction

**Run filesystem and process operations on a remote `losh-server`, reached
through ordinary SSH. Keep the coding harness, credentials, and conversation
state on the client. The remote filesystem is the source of truth.**

The client may be a laptop or an always-on login server. A harness adapter
translates native tool requests into remote operations and translates the
confirmed results back into the harness's expected form.

This replaces the previous recommendation to start with a local workspace
mirror. No authoritative local copy, periodic push/pull, or pre-command mirror
flush is needed when all workspace operations execute remotely. Optional local
caches are disposable read caches, not another writable workspace.

The server implements shared filesystem and process semantics, with small
format adapters where necessary for Codex patches or Claude edits. Adding a
harness should not require another transfer engine, recovery protocol, or
distributed filesystem.

Native tool names, patch behavior, approvals, and diff presentation should
change as little as possible. Whether the existing harness extension points
can preserve all of that is the first feasibility question, not an assumed
capability.

### Goals and limits

- Read, search, edit, and run commands against the actual target machine.
- Preserve pending user/agent operations across connection loss and client restart.
- Never publish a partial file upload or silently replay an uncertain mutation.
- Detect stale edits, retain recovery information, and stop on ambiguous outcomes.
- Start with whole-file staging and simple serialization; optimize transfers later.
- Reuse the same server for Codex first, then Claude Code and other adapters.

This design does not promise transactional shell commands, instantaneous
multi-file changes visible to every process, or protection against arbitrary
uncooperative filesystem writers. It must state those limits precisely.
A remote job can survive client disconnection; the harness itself continues
only while its client machine is running. A login server remains useful.

## 2. Architecture and responsibilities

```text
Client: laptop or login server                  Target machine
------------------------------------            ----------------------------
Codex / later Claude Code
  native tool invocation
       |
  harness adapter
       |
  losh client coordinator  === OpenSSH ===>     losh-server gateway
  - original-operation approval                 |
  - request IDs + pending journal               per-user supervisor
  - reconnect + status recovery                 - durable operation journal
  - native result/diff translation               - filesystem executor
  - session index                               - process workers
                                                - staging + result storage
       <=== confirmed result / read data / job output ===
```

| Component | Owns |
| --- | --- |
| Harness adapter | Launch/resume, tool coverage, native request/result mapping, path presentation, diff and approval integration |
| Client coordinator | Persisting intent, operation identity, retries/status queries, dependencies, reconnect UX |
| SSH transport | Authentication, host verification, aliases/jump hosts, framed byte delivery |
| Server coordinator | Capability negotiation, deduplication, scheduling, journal recovery, durable results |
| Filesystem executor | Reads/search, version checks, patch interpretation, staging, publication, metadata semantics |
| Process supervisor | Job launch, status, output replay, input, cancellation, lifecycle independent of SSH |

The gateway uses SSH stdio. The implemented baseline records each accepted
operation and launches a detached per-operation worker, which owns work
independently of the SSH connection. A later persistent per-user supervisor can
add stronger scheduling, cancellation, recovery, and upgrade coordination.
There is no extra public listening port, root daemon, or model credential on
the target.

## 3. Startup, installation, and session identity

Proposed reliable-mode startup:

1. Connect using system OpenSSH and its normal host-key verification.
2. Probe target OS/architecture, writable private state location, installed
   helper, and supported protocol versions.
3. If necessary, upload an appropriate verified server binary over SSH to
   staging, then install it atomically in a versioned user-owned location.
   A checksum detects corruption; authenticity must come from the trusted
   client release/distribution mechanism, not a checksum supplied alongside
   untrusted bytes.
4. Start or attach to the per-user supervisor and establish a framed session.
5. Negotiate capabilities, resolve remote cwd/root, and recover prior pending
   operations before granting the harness access.

Proposed separation of storage:

```text
client ~/.losh/
  sessions/<session>/           target binding, harness identity, pending IDs
  operations/<operation>/       immutable request, upload data, last known result
  cache/                       optional versioned read/diff artifacts

target ~/.local/share/losh/
  server/<version>/            versioned executable
  state/                      installation identity, journal, results, job logs
  run/                        private supervisor endpoint/lock

target destination filesystem/
  private staging/recovery    staged files on the same filesystem as their targets
```

Actual platform paths remain an implementation choice. Staging cannot always
live beside the journal: atomic rename requires the destination filesystem.
No automatic sudo; insufficient permissions are explicit operation failures.

Bind requests to SSH-authenticated target identity, effective remote user,
server state-store identity, workspace handle, and harness session. A workspace
handle identifies the resolved remote scope, not a local directory. An SSH
alias alone is insufficient evidence that pending work belongs to the same
machine after a rebuild. Host-key or state-store changes require reconciliation,
not blind replay. Preserve normal OpenSSH handling of host identity changes.

The helper is required for the proposed reliable mode. Plain SSH can remain a
clearly labeled compatibility mode, with its current weaker guarantees.
Never fall back mid-operation to a fresh shell command because a helper failed.
An upgrade must preserve journal compatibility and active jobs; incompatible
versions drain or refuse startup rather than abandoning operation history.

## 4. Harness integration is a separate feasibility gate

### Established behavior versus proposed integration

The current implementation rewrites Codex Bash commands through `PreToolUse`,
routes commands and patches through durable remote operations, and blocks the
duplicate native `apply_patch`. Its harness coverage is not yet complete.

Codex documents input rewriting for Bash and `apply_patch`, but that does not
establish replacement of a native executor with an arbitrary remote result.
Some tool paths bypass hooks, and some unsupported hook responses allow the
original call to continue. Hook coverage must be tested rather than treated
as enforcement by itself. [Codex hooks](https://learn.chatgpt.com/docs/hooks)

Claude Code documents pre-tool input replacement and permission decisions.
That establishes a possible interception point, not a proven general remote
execution backend for native file tools.
[Claude Code hooks](https://code.claude.com/docs/en/hooks)

### User-invoked shell mode and the shell-wrapper proposal

Codex's user-invoked shell mode is a separate execution path from an
agent-issued `Bash` tool call. In the current integration, commands entered
directly by the user run locally in the losh metadata workspace. For example,
`pwd` reports `$HOME/.losh/workspaces/<session-id>` and `ls` can expose the
generated `AGENTS.md`. The `PreToolUse` hook never sees this command, so the
existing Bash rewrite cannot make it remote.

This is unsupported behavior, not a harmless presentation defect. A command
such as `uname`, `git status`, or `rm` would act on the client while appearing
inside a remote-oriented session. losh must not describe user shell mode as
remote until it has a verified interception point.

The bounded experiment considered a per-session shell wrapper:

```text
Codex user shell mode
        |
        | verified one-shot shell invocation
        v
losh-shell wrapper (local, session-bound)
        |
        | durable process.start(operation ID, target, root, command)
        v
losh-server -> selected remote shell -> remote workspace
```

At session launch, losh would set `SHELL` to a losh-owned executable and pass
the session identity separately through a private environment variable or
per-session wrapper path. The wrapper must accept only shell invocation shapes
that have been observed and tested, such as `-c <command>` or `-lc <command>`.
It must pass the command as protocol data, never interpolate it into another
local shell command. The remote server selects and invokes the configured
remote shell.

The feasibility probe failed on the tested Codex stack: Codex CLI 0.158.0 with
its active 0.154.0 service on Linux. Codex was launched with `SHELL` set to a
probe executable. A user-entered `!printf LOSH_PROBE` command executed locally
and returned `LOSH_PROBE`, while the probe executable was never invoked. The
lightweight wrapper approach is therefore deferred, and no implementation is
planned unless Codex exposes a supported user-shell hook or execution provider.

Other Codex builds may use a different path, but losh cannot infer support from
`SHELL`. PATH-shadowing `bash`, `sh`, or `zsh` is not an acceptable fallback:
it is process-wide, can cause recursion, and could unexpectedly redirect
unrelated local programs. Wrapping the whole Codex TUI PTY is also insufficient
because terminal keystrokes do not provide a reliable command boundary.

#### Capability detection and fail-closed behavior

Support should be enabled only for a harness/version combination that passes a
runtime conformance probe. The probe records the wrapper's argument vector,
environment shape, working directory, exit-status handling, and whether stdout
and stderr are preserved. The compatibility record is keyed by Codex version
and platform; an unknown or changed version is unsupported until retested.

The wrapper should perform a session handshake before executing any command:

1. Load a session record by opaque ID, not by user-controlled target text.
2. Verify the record is owned by the current local user and names the active
   target and remote root.
3. Contact the matching losh-server and verify its installation/state identity.
4. Allocate a fresh durable operation ID for this user submission.
5. Submit once, poll/reconnect using the same ID, and return the retained result.

If any check fails, the wrapper exits nonzero and prints that remote shell mode
is unavailable. It must never run the command locally. If Codex bypasses the
wrapper entirely, losh cannot reliably fail that individual command; therefore
the feature remains disabled unless the version probe establishes that Codex
uses the wrapper. The local metadata workspace should remain read-only, but
read-only metadata is defense in depth rather than proof of interception.

#### Command, environment, and output semantics

The first implementation can support only one-shot, noninteractive commands:

- Each submission starts in the configured remote root.
- `cd`, aliases, functions, and exported variables last only for that command.
- The wrapper returns the remote exit status and keeps stdout and stderr
  distinct where Codex's shell-mode interface permits it.
- A disconnect retries status/result retrieval with the same operation ID; it
  never starts a second command merely because the acknowledgement was lost.
- Entering the same text twice intentionally creates two different operation
  IDs and therefore two executions.
- Local `PATH`, credentials, tokens, and provider environment variables are not
  forwarded. A small allowlist such as locale and terminal metadata can be
  negotiated explicitly.

The remote shell is part of the session contract. Initially it should be an
explicit executable, defaulting to `/bin/sh`, rather than trusting arbitrary
client environment. Login-shell behavior must be opt-in because startup files
can mutate state, emit output, and change command interpretation. The command,
shell choice, target identity, root, and environment policy are bound into the
operation digest.

The current buffered process RPC is enough for a proof of concept, but a good
interactive experience requires the future streaming process interface.
Ctrl-C must become an acknowledged remote cancellation request; killing only
the local wrapper can leave the detached remote command running. PTYs, REPLs,
editors, password prompts, and persistent shell state are out of scope until
`process.attach`, input sequencing, resizing, cancellation, and output offsets
exist.

`sudo` follows the same boundary as agent-issued commands. Noninteractive
`sudo -n` can work when remote policy permits it. Password delivery, `sudo -S`,
credential forwarding, and hidden privilege escalation are forbidden. A host
that requires a password or TTY returns an explicit failure.

#### Rejected shortcuts

- A remote filesystem mount alone is not shell routing. It can make `ls` look
  remote while `uname`, services, processes, networking, and absolute paths
  still refer to the client, which is more misleading than an explicit error.
- Mirroring remote files into the metadata workspace has the same execution
  mismatch and reintroduces synchronization ordering.
- Prompt instructions cannot redirect a command typed directly by the user.
- Showing a remote-looking prompt or rewriting `pwd` output is cosmetic and
  must never substitute for remote execution.

#### Acceptance tests

Before enabling the wrapper by default, test at least:

- supported Codex versions on macOS and Linux, including resume;
- exact wrapper argv for `-c`, `-lc`, quoting, newlines, pipes, redirects,
  substitutions, and empty commands;
- remote `pwd`, `uname`, filesystem changes, exit codes, stdout, and stderr;
- spaces and non-ASCII characters in roots and command payloads;
- local-secret exclusion and remote environment selection;
- disconnect before acceptance, after acceptance, during execution, and while
  returning output, proving deduplication in each case;
- two identical user submissions producing two distinct executions;
- Ctrl-C and client termination without false cancellation claims;
- `sudo -n` success and password/TTY-required failure; and
- an unknown Codex version refusing support rather than silently executing
  locally.

If Codex does not honor a controllable shell executable, the next correct step
is an upstream Codex execution-provider or user-shell hook. A remote mount is
not the fallback for this feature.

The table below describes desired mappings, not verified hook capabilities:

| Harness behavior | Remote operation | Adapter result |
| --- | --- | --- |
| Read a file or range | `fs.read` | Bytes/text, requested range, version, truncation information |
| List/glob/search | `fs.list`, `fs.search` | Remote matches and coverage/continuation information |
| Codex patch | `fs.applyChange` with a negotiated patch format | Confirmed changed paths, versions, diff/result expected by Codex |
| Claude edit/write | `fs.applyChange` with replace-text or replace-file operations | Matching behavior, confirmed result, native edit presentation |
| Bash/exec | `process.start`, then status/output | Remote stdout/stderr, job handle, exit status |
| Poll/input/resize/cancel | Corresponding process methods | Map native execution handle to the same remote job |
| Git status/diff or other implicit reads | Remote execution or explicit read integration | Consistent view of the remote repository |
| Project instructions and file artifacts | Remote reads/downloads | Content or versioned temporary local artifact, as required by the harness |

Before implementation, demonstrate that interception can:

1. Stop the original local execution reliably.
2. Submit exactly one logical remote operation despite hook/tool retries.
3. Wait for or resume its result without treating a timeout as permission to
   execute locally.
4. Return success, errors, cancellation, and diffs in the harness's expected form.
5. Cover subagents, implicit reads, alternate tool paths, and process continuation.
6. Preserve approval of the original action, target, and cwd.

Preferred integration is a supported execution-provider/backend extension if
one exists, or hooks where they demonstrably support the complete contract.
A small harness modification is another option, with maintenance costs made
explicit. MCP/custom tools can use the same server, but change the native
tool surface and may lose native diff UX; they are a product tradeoff to decide,
not a silent substitute.

Do not execute a patch in a pre-hook and then allow the original local patch.
Do not use an empty patch, fabricated local success, or post-hook feedback to
pretend a native remote execution contract exists.

Unintercepted filesystem access must not silently fall back to the client's
metadata workspace. Some harness behaviors may need temporary local artifacts
or a mount presentation; identify those individually. Unknown coverage is a
reason to leave a feature unsupported, not to claim complete compatibility.
Local harness metadata/authentication remains local and separate from remote
project files, including remote `AGENTS.md` and harness configuration.

## 5. Shared protocol and operation identity

Use a versioned, framed protocol over the SSH byte stream. Reserve stdout for
protocol frames and stderr for diagnostics. Frame size limits, binary encoding,
backpressure, checksums for transferred payloads, and explicit EOF/error handling
are required. JSON metadata with separately framed binary chunks is a reasonable
first choice; exact serialization remains open.

Illustrative request envelope:

```json
{
  "protocol": 1,
  "operation_id": "client-generated-unique-id",
  "request_digest": "hash-of-canonical-request-and-payload-identities",
  "server_state_id": "expected-state-store-identity",
  "workspace_id": "server-issued-scope",
  "session_id": "client-session",
  "method": "fs.applyChange",
  "depends_on": ["prior-operation-id"],
  "arguments": {
    "expected_versions": {"src/main.go": "opaque-version-token"},
    "change": {"format": "negotiated-patch-format", "payload_ref": "sha256:..."}
  }
}
```

The server recomputes/validates the digest. The same ID with the same request
returns the existing operation state/result; the same ID with a different
request is rejected. Scope, expected versions, dependencies, and payload
identities are part of the digest. IDs must not be derived solely from command
text: intentionally running the same command twice requires two IDs.

The client durably records intent and upload contents before sending a mutation.
Map repeated handling of a harness tool invocation to the existing operation.
After a client crash, unresolved operations are reconciled before the harness
can accidentally propose the same pending action as a fresh retry. A genuinely
new tool invocation is not deduplicated just because its text matches.

Separate response concepts:

| Response | Meaning |
| --- | --- |
| `accepted` | Complete validated request/input is durable and recoverable; no claim that effects are committed |
| `in_progress` | Operation exists and is executing or awaiting recovery |
| `committed` | Filesystem publication and recoverable result meet the negotiated durability contract |
| `exited` | Process has a recorded exit status; does not mean its arbitrary effects were transactional |
| `rejected` / `aborted` | Definitive no live effects for the filesystem operation |
| `needs_reconciliation` | Effects cannot yet be established safely; dependent work stays blocked |

Read-only calls can use lightweight request IDs without a durable mutation
journal. Retrying a read may return a newer version, which must be identified.
A lost mutation acknowledgement always triggers status recovery with the
original ID, never a new mutation ID.

## 6. Filesystem interface

Illustrative API:

```text
fs.stat(workspace, path, followSymlinks) -> metadata, version
fs.read(workspace, path, range, expectedVersion?) -> data, version, coverage
fs.list(workspace, path, cursor?, limits) -> entries, continuation, consistency
fs.search(workspace, query, paths, cursor?, limits) -> matches, coverage
fs.applyChange(operationID, workspace, expectedVersions, change) -> operation
fs.previewChange(workspace, expectedVersions, change) -> sealedPlan, diff
fs.applyPlan(operationID, sealedPlan) -> operation
operation.status(operationID) -> state, progress, result
operation.result(operationID, range?) -> retained result/artifact
```

Preview is optional and side-effect-free with respect to live files. A sealed
plan binds exact base versions, content, targets, and metadata. If approval is
based on that preview, application must revalidate those versions and reject
stale plans. Recomputing a different patch after approval needs a new plan.

### Reads, search, and versions

Execute list/search remotely rather than pulling a whole tree locally.
Results must state truncation, skipped/unreadable paths, and continuation
semantics. Do not report an incomplete scan as “no matches.” Pagination over a
changing directory may require restarting; do not imply snapshot consistency
without implementing it.

Read responses pair content with the version of those bytes. Repeated ranges
must use that same version or fail/restart; do not splice bytes from different
file revisions. Cooperating writes can be excluded while reading. For external
in-place writers, stable reads need coordination/snapshots; stat-before/after
alone is not proof. If stability cannot be established, report an unstable read
or explicitly weaker live-read semantics, especially for logs.

Versions cover content, object type, and mutation-relevant metadata. Absence
is explicit. Modification time and size are insufficient identity. A future
cache is keyed by target/scope/path/version; writes still validate remotely.

### Changes and patch formats

Use a common change model: create, replace contents, replace text, rename,
delete, and supported metadata updates. A negotiated patch dialect can be
passed intact and interpreted on the server, preserving its exact matching,
newline, encoding, move, and error semantics. Do not pretend that all patch
formats are interchangeable or reinterpret a Codex patch as a generic diff.

Claude-style text replacement similarly needs defined occurrence counts and
matching behavior. Server-side format modules normalize requests into a
validated change plan; commit/recovery stays shared across harnesses.

The adapter carries versions from the reads on which an edit was based.
Do not simply stat the latest file immediately before writing and call that a
stale-edit check: that could bless a change the agent never saw. If a native
tool supplies no base token, the adapter must track read receipts, or prepare
an explicitly reviewed plan against a fresh read. Patches with matching context
alone provide weaker conflict detection and must not silently replace this
contract.

The first implementation can reconstruct a complete new file remotely, even
when only a small patch crossed the network. Validate all paths and changes
before publication. Keep the before/after versions and diff/result so a lost
reply can be reproduced without executing the patch again.

### Scope and filesystem semantics

Remote paths resolve against server-issued scopes and remote cwd, not local
mount points. Start with explicit scopes; add additional roots for machine
administration without mirroring them. Apply remote permission checks normally.
Structured paths can be translated; arbitrary shell scripts must never be
rewritten through string substitution.

Check traversal, symlink resolution, and path races on the server. Where
available use directory-relative operations with appropriate no-follow checks;
string prefix checking alone is insufficient. The server must define how
renamed/replaced roots invalidate handles.

Initially support a documented subset of regular-file/directory operations.
Preserve supported modes and object types. Detect unsupported hard-link,
symlink, ownership, ACL/xattr, special-file, and cross-filesystem rename
semantics rather than silently changing them. Atomic replacement breaks
hard-link identity and can change metadata unless deliberately handled.

## 7. Mutation lifecycle and durability

The initial guarantee is **per-file atomic publication with journaled,
recoverable multi-file operations**, assuming supported storage semantics and
coordination with other writers. It is not whole-tree atomic visibility.

```text
local pending -> receiving -> accepted -> prepared -> committing -> committed
                                  |          |
                                  +----------+----> rejected/aborted
                                                     (before live effects)

interrupted committing -> recover recorded progress -> committed
                        -> needs_reconciliation
```

### Commit protocol

1. Persist client intent and payload. Upload into private staging; incomplete
   input is not accepted as a complete mutation and never touches live files.
2. Durably record the full accepted request and payload references on the server.
   After acceptance, loss of SSH must not discard the operation.
3. Acquire the appropriate server writer gate; verify dependency outcomes,
   scope identity, base versions, destinations, and permissions. Reconstruct
   and verify all new contents. A conflict stops before publication.
4. Prepare recovery data for the old and new states, including directory/name
   changes. Sync staged files and persist the plan before recording commit intent.
   Stage each replacement on its destination filesystem.
5. Revalidate under the gate, record durable commit intent, then publish each
   supported file replacement with a same-filesystem atomic rename. Record
   recoverable per-path progress and sync affected directories as required.
6. Persist the terminal result, versions, and diff/artifact references before
   acknowledging committed success. Release the gate only when dependent work
   can safely proceed.

The selected filesystem/storage must support the durability assumptions.
Capabilities must distinguish atomic visibility from power-loss durability.
Do not advertise durable commits on an unsupported filesystem merely because
rename returned success.

The server should finish an accepted mutation independently of the connection.
A worker crash is handled by journal recovery before serving conflicting
operations. If recovery observes states consistent with the recorded plan and
exclusive ownership, it can continue publication. If unrelated changes or
missing evidence make that unsafe, retain recovery data and block for
reconciliation. Never return ordinary failure with no-effects implications
after partial publication.

Creates, deletes, renames, and multi-directory changes need their own recovery
plans. Deleted contents may be moved into private recovery storage until the
operation is settled. A prepared operation can be aborted before publication;
once committing, cancellation cannot promise rollback. Do not automatically
roll back over subsequent external changes.

### Atomicity and competing writers

Other processes can observe an intermediate multi-file tree. The server can
keep participating losh reads/commands behind a gate during commit and recovery;
it cannot block arbitrary external readers. Whole-tree atomic activation needs
a separately supported snapshot/versioned-directory strategy and cooperating
consumers. Service configuration may need validation followed by explicit
activation/reload.

Hash-check-then-rename is not atomic compare-and-swap against uncooperative
writers. Serialize participating writers and assume exclusive access during
commit for the strong guarantee. Concurrent external writers require shared
coordination, an isolated workspace, or a weaker documented mode. Watching
filesystem events does not eliminate the race.

Start with conservative server-wide serialization per account, or an
equivalently safe lock domain, rather than locks keyed only by caller-provided
root strings: overlapping roots and path aliases can refer to the same files.
A later lock manager may permit proven-independent operations. One supervisor
must arbitrate ownership, and reconnecting stale clients must not bypass it.

### A lost acknowledgement, concretely

```text
client -> server: apply operation A, expecting file version V1
server: journal A; prepare V2; publish V2; persist A=committed
server -> client: committed(A, V2)       [connection drops]

client reconnects -> status(A)
server -> client: committed(A, V2), original result/diff
client -> harness: the confirmed result
```

The patch is not reapplied. Even if the file later becomes V3, querying A
returns A's historical result; a new read reports V3. If publication happened
but its final journal update was interrupted, recovery must reconcile the
commit intent and per-path evidence rather than infer “not found, run again.”

## 8. Process interface and ordering

```text
process.start(operationID, command, cwd, env, terminal, dependsOn) -> job
process.status(jobID) -> accepted | running | exited | uncertain
process.output(jobID, afterOffset, limit) -> frames, nextOffset, exitStatus?
process.input(jobID, sequence, bytes) -> acknowledgedSequence
process.resize(jobID, rows, columns)
process.cancel(jobID, cancellationID) -> cancellationStatus
```

Use the same target binding, deduplication, and recovery infrastructure as file
operations. Preserve separate stdout/stderr where possible; PTYs may combine
them by design. Output has stable ordered offsets. Reattachment reads missing
frames and the retained exit status rather than rerunning the process.

With no writable mirror, there is nothing to flush:

```text
remote read -> remote edit commits -> dependent remote command starts
            -> command exits -> next read queries the remote filesystem
```

The server checks dependencies itself. A queued test command cannot run while
its patch is merely accepted, partially committed, or unresolved. First version
can serialize commands and file operations conservatively. Later parallelism
must maintain dependency and writer coordination.

Shell commands can write arbitrary files, launch daemons, or affect services
and databases. Their writes do not pass through the filesystem commit protocol.
Do not claim atomic-write guarantees for shell redirection just because the
shell is remote. Tool-level structured edits use the safer filesystem path;
process side effects retain normal OS/application semantics.

A command that backgrounds writers may outlive its apparent exit. Strict
coordination needs supervised process groups/isolation and a stated supported
workload; parsing shell syntax cannot enforce it. Background access to shared
files needs explicit policy. Workspace scopes do not confine an unrestricted
shell; remote OS permissions/isolation do.

Durable acceptance, worker identity, and launch intent reduce duplicate launches
but cannot guarantee exactly-once arbitrary effects through every server-crash
window. If it is unclear whether a command ran, keep it uncertain and do not
automatically relaunch. Store boot/process identity rather than trusting a PID
that may be reused.

Input delivery also needs explicit semantics: a crash between writing stdin
and recording its sequence may make delivery uncertain. Do not blindly replay
ambiguous input. Cancellation is an acknowledged request followed by observed
job state, not a guarantee that prior effects were undone.

## 9. Reconnection, state retention, and user experience

On disconnect, retain pending requests locally and mark the connection as
reconnecting. Retry transport setup with bounded attempts, backoff, and jitter;
offer pause/cancel without discarding operation identity. Avoid holding a short
pre-hook open indefinitely if the harness cannot tolerate it: the adapter must
support asynchronous pending results or safe suspension/resume.

After reconnecting:

1. Reauthenticate and verify target/state-store identity and capabilities.
2. Query pending IDs before submitting dependent work.
3. Retrieve known results/output; resume verified incomplete uploads by identity.
4. Wait for in-progress recovery or surface conflicts/uncertainty.
5. Return confirmed results to the harness, then continue its queued work.

A timeout means unknown/pending, not “did not happen.” An operation missing from
a healthy unchanged journal is retryable only if the protocol proves it was
never accepted. Missing history, journal corruption, restore from backup, or
expired records must not be interpreted as permission to execute again.

Keep terminal results until client acknowledgement plus a documented recovery
window. Retain deduplication tombstones or reject operations from retired
epochs so a late retry cannot become a fresh mutation after result cleanup.
Clients must retain unresolved payloads. Garbage collection must preserve
staging/recovery objects referenced by pending work. If a historical result
has expired, return explicit result-unavailable status without replay.

Treat restoring a server state backup as an identity/epoch transition requiring
reconciliation. Detection of an unnoticed rollback of the entire machine and
journal is outside the basic protocol's guarantee; stronger protection needs
an external monotonic witness or equivalent infrastructure.

| Failure | Required outcome |
| --- | --- |
| Upload interrupted | Live files intact; retain/resume staging with hashes and original ID |
| Commit reply lost | Query the same ID; return the original result |
| Client restarts | Recover local pending IDs before permitting conflicting new work |
| Server/worker crashes during multi-file publication | Gate access; recover from durable plan or report reconciliation needed |
| Target reboots during command | Query job record; report exited/interrupted/uncertain as evidenced; no blind restart |
| Output stream interrupted | Resume by offset; mark any retention gap explicitly |
| Base version changed | Conflict with expected/observed versions; no silent overwrite |
| Disk fills | Reject before effects where possible; preserve recovery state after commit intent |
| Journal/identity changes | Stop automatic replay and reconcile |
| Read stream interrupted | Restart/version-check; never concatenate unrelated revisions |

Display distinct states such as uploading, accepted, committing, reconnecting,
committed, conflict, and uncertain. “Saved” means confirmed remote commit.
A read returns actual data or an explicit error, not a substituted local file.
Initially pause new agent work while disconnected; preserving pending requests
does not imply that offline edits have already taken effect.

## 10. Efficient transfers

The direct server already avoids synchronizing a whole workspace. Search runs
remotely, reads return selected contents/ranges, and a patch can cross the wire
without uploading the complete replacement file.

| Stage | Mechanism | Stable correctness contract |
| --- | --- | --- |
| Initial | Whole-file upload for replacements; small patch payloads; remote full-file staging | Base checks, verified complete output, durable ID, atomic per-file publication |
| Resumable transfer | Content-addressed chunks with verified offsets/hashes | Incomplete chunks never become live contents |
| Delta replacement | Changed blocks against an identified base, reconstructed remotely | Wrong base fails or falls back; verify final hash before publication |
| Read optimization | Versioned ranges and optional read cache | No mixing revisions or silent stale-data substitution |
| I/O optimization | Streaming reconstruction, reflinks where supported, scoped parallel work | Same journal, publication, and recovery guarantees |

A tiny patch to a huge file may need very few network bytes while still
requiring a full local rewrite on the server. Bandwidth, remote disk I/O, space
amplification, and latency are separate metrics. A patch's intended result hash
may be computed by the server during preparation and bound to the durable
plan; replacement uploads must verify the client's declared full-content hash.

Rsync-style deltas are one possible transfer engine, not the commit protocol.
They must reconstruct into staging rather than mutate live files in place.
[How rsync works](https://rsync.samba.org/how-rsync-works.html)

No optimization may bypass expected-version checks, change operation identity,
or acknowledge success before the negotiated commit boundary. Benchmark huge
files, binary files, many small files, remote search, and high-latency links;
run the same disconnect/crash tests against every transfer strategy.

## 11. Alternatives and why they are no longer the default

| Alternative | Useful property | Reason to defer |
| --- | --- | --- |
| SSHFS-style mount | Native filesystem access, including implicit reads | Cache/open-handle behavior and native in-place writes do not automatically satisfy the operation journal contract |
| Custom mount backed by losh-server | Could preserve broad native behavior while reusing server semantics | Substantial platform/filesystem work; must define when syscall writes form a committed operation |
| Complete local mirror | Ordinary native paths and edits without a filesystem driver | Reintroduces flush/pull, conflicts, implicit reads, disk duplication, and ordering complexity |
| Sparse mirror | Lower initial transfer | Missing files/listings/search require comprehensive access mediation |
| Explicit remote MCP/tools | Clean use of the preferred server without native executor replacement | Native tool/diff UX may differ |
| Plain SSH shell operations | Minimal prerequisites and existing compatibility | No equivalent durable mutation or job-recovery contract |

A mount could become a presentation adapter for the same server if implicit
native reads make it necessary. It should not be assumed to transform arbitrary
filesystem syscalls into atomic tool-sized changes. A mirror would be a
deliberate alternate design, not an automatic fallback.

## 12. Permissions and deployment boundaries

Keep model credentials and conversation storage on the client. Use the remote
SSH account's normal permissions. Do not automatically enable agent forwarding
or copy provider credentials. Bind each operation to configured target/scope;
tool-supplied content cannot select a different SSH destination.

Approval must cover the original command/change and remote target before any
effect, not merely a generic allowed wrapper. A preview-based approval binds
the exact prepared plan. Remote paths, file contents, search results, and job
output are data, never shell-interpolated transport instructions.

Protect journals, sockets, staging, and retained contents with user-only access
where appropriate. Account for sensitive files in result retention and cleanup.
A per-user server is not a multi-tenant privilege boundary; root/sudo support
requires an explicit future design rather than hidden escalation.

## 13. Implementation milestones and acceptance criteria

The first implementation now provides:

- automatic platform detection and atomic versioned server upload;
- durable request IDs/digests, remote request/status journals, and deduplication;
- detached command workers with retained terminal output and exit status;
- a server-side Codex patch parser for add/update/delete/move;
- confined paths, staged complete files, atomic per-file rename, and rollback
  attempts when a reported publication step fails;
- reconnecting RPC calls and status recovery; and
- unit plus real-SSH integration coverage.

The first Codex integration feasibility result is also known: shell replacement
works cleanly. Current hooks cannot replace native `apply_patch` with a custom
executor result, so the pre-hook commits remotely and blocks the local duplicate.
Codex follows the success reason, but its UI labels the call blocked. Preserving
the native diff/result requires a stronger harness extension or a carefully
mediated local presentation layer.

Still required for the full contract:

- server read/stat/list/search with stable version tokens;
- optimistic expected-version checks independent of patch context;
- persistent supervision, output-by-offset streaming, PTYs, input, cancellation,
  dependency scheduling, and target epochs;
- crash recovery for partial multi-file publication and retained-result GC;
- approval/audit integration and broad failure injection; and
- Claude Code conformance.

### D. Failure testing before reliability claims

Inject disconnects before/after acceptance, rename, result persistence, and
reply delivery. Kill client, gateway, worker, and supervisor separately; reboot
the target. Test full disks, malformed/duplicate requests, mismatched IDs,
overlapping roots, stale clients, external writers, changed host/state identity,
and expired results.

Required assertions include:

- An incomplete upload never becomes a live file.
- Confirmed single-file edits survive supported crash/durability scenarios.
- A lost acknowledgement does not apply a patch or launch a command twice.
- Pending operations and staged contents survive client restart.
- Partial multi-file publication blocks dependent losh operations until resolved.
- Conflicts and ambiguous external changes preserve evidence instead of overwriting it.
- Reads/ranges report correct versions and incomplete results explicitly.
- Native results/diffs correspond to confirmed remote actions.
- Cancellation and timeouts do not falsely assert that effects never happened.

### E. Transfer efficiency and other harnesses

Add resumable chunks/deltas after measuring the baseline. Keep server semantics
unchanged when adding `--harness claude`; validate its tool matching, result,
approval, and diff behavior with the same conformance cases.

## 14. Decision record and remaining questions

| Topic | Direction |
| --- | --- |
| Authoritative files | Remote target only |
| Default filesystem approach | Structured remote operations through losh-server |
| Agent location | Client/login server; credentials and conversation remain there |
| Transport | System OpenSSH; no additional public port |
| Server installation | Bootstrap a compatible per-user server over SSH |
| Durability | Operation IDs, journals, staged publication, retained results |
| Initial transfer strategy | Whole-file staging/replacement support plus remote patch interpretation |
| Native harness experience | Preserve where demonstrably supported; first feasibility gate |
| Mirror/mount | Alternatives, not the default architecture |
| Implementation status | Initial bootstrap, journaling, detached exec, and patch slice implemented |

Open implementation choices:

1. Which stronger Codex integration can return a native successful patch result
   without a local authoritative mirror?
2. Which stable read/version API should the Codex adapter expose next?
3. Which target filesystems can advertise power-loss durability?
4. How should additional scopes, external writers, and background jobs be exposed?
5. Is recoverable multi-file publication sufficient, or do specific workflows
   require atomic activation before release?
6. What retention, disk quotas, target epochs, and reconciliation UX are appropriate?
7. When should a persistent supervisor replace detached per-operation workers?

The preferred architecture is settled at the proposal level: remote filesystem
and process services over SSH. These questions determine its concrete protocol,
integration, and supported guarantees; they do not require maintaining a local
writable mirror.
