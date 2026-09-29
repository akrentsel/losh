package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApplyAgentPatchAddUpdateDeleteAndMove(t *testing.T) {
	root := t.TempDir()
	mustWriteTestFile(t, filepath.Join(root, "update.txt"), "alpha\nbeta\ngamma\n", 0640)
	mustWriteTestFile(t, filepath.Join(root, "delete.txt"), "remove me\n", 0644)
	mustWriteTestFile(t, filepath.Join(root, "move.txt"), "before\n", 0600)
	operationDir := filepath.Join(t.TempDir(), "operation-1")
	if err := os.MkdirAll(operationDir, 0700); err != nil {
		t.Fatal(err)
	}

	patch := `*** Begin Patch
*** Update File: update.txt
@@
 alpha
-beta
+BETA
 gamma
*** Add File: nested/added.txt
+one
+two
*** Delete File: delete.txt
*** Update File: move.txt
*** Move to: moved.txt
@@
-before
+after
*** End Patch`

	result, err := applyAgentPatch(root, patch, operationDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"updated update.txt", "added nested/added.txt", "deleted delete.txt", "moved move.txt to moved.txt"} {
		if !strings.Contains(result, expected) {
			t.Fatalf("result %q does not contain %q", result, expected)
		}
	}
	assertTestFile(t, filepath.Join(root, "update.txt"), "alpha\nBETA\ngamma\n")
	assertTestFile(t, filepath.Join(root, "nested", "added.txt"), "one\ntwo\n")
	assertTestFile(t, filepath.Join(root, "moved.txt"), "after\n")
	if _, err := os.Stat(filepath.Join(root, "delete.txt")); !os.IsNotExist(err) {
		t.Fatalf("delete.txt still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "move.txt")); !os.IsNotExist(err) {
		t.Fatalf("move.txt still exists: %v", err)
	}
	info, err := os.Stat(filepath.Join(root, "moved.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("moved mode = %o, want 600", info.Mode().Perm())
	}
}

func TestApplyAgentPatchConflictLeavesFilesUntouched(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	mustWriteTestFile(t, path, "current\n", 0644)
	opDir := filepath.Join(t.TempDir(), "operation-2")
	if err := os.MkdirAll(opDir, 0700); err != nil {
		t.Fatal(err)
	}
	patch := `*** Begin Patch
*** Update File: file.txt
@@
-stale
+new
*** Add File: should-not-exist.txt
+bad
*** End Patch`
	if _, err := applyAgentPatch(root, patch, opDir); err == nil {
		t.Fatal("expected context conflict")
	}
	assertTestFile(t, path, "current\n")
	if _, err := os.Stat(filepath.Join(root, "should-not-exist.txt")); !os.IsNotExist(err) {
		t.Fatalf("partial patch was published: %v", err)
	}
}

func TestApplyAgentPatchRejectsTraversalAndSymlink(t *testing.T) {
	root := t.TempDir()
	opDir := filepath.Join(t.TempDir(), "operation-3")
	if err := os.MkdirAll(opDir, 0700); err != nil {
		t.Fatal(err)
	}
	traversal := "*** Begin Patch\n*** Add File: ../escape\n+x\n*** End Patch"
	if _, err := applyAgentPatch(root, traversal, opDir); err == nil {
		t.Fatal("expected traversal rejection")
	}

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	symlink := "*** Begin Patch\n*** Add File: link/escape\n+x\n*** End Patch"
	if _, err := applyAgentPatch(root, symlink, opDir); err == nil {
		t.Fatal("expected symlink escape rejection")
	}
}

func TestApplyAgentPatchRejectsAmbiguousContextAndDeleteBody(t *testing.T) {
	root := t.TempDir()
	opDir := filepath.Join(t.TempDir(), "operation-ambiguous")
	if err := os.MkdirAll(opDir, 0700); err != nil {
		t.Fatal(err)
	}
	mustWriteTestFile(t, filepath.Join(root, "duplicate.txt"), "same\nmiddle\nsame\n", 0644)
	ambiguous := "*** Begin Patch\n*** Update File: duplicate.txt\n@@\n-same\n+changed\n*** End Patch"
	if _, err := applyAgentPatch(root, ambiguous, opDir); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous-context error, got %v", err)
	}
	assertTestFile(t, filepath.Join(root, "duplicate.txt"), "same\nmiddle\nsame\n")

	mustWriteTestFile(t, filepath.Join(root, "delete.txt"), "keep\n", 0644)
	deleteWithBody := "*** Begin Patch\n*** Delete File: delete.txt\n-unexpected\n*** End Patch"
	if _, err := applyAgentPatch(root, deleteWithBody, opDir); err == nil {
		t.Fatal("expected delete-body rejection")
	}
	assertTestFile(t, filepath.Join(root, "delete.txt"), "keep\n")
}

func TestServerRPCVersionAndDigestValidation(t *testing.T) {
	var out bytes.Buffer
	if err := runServer([]string{"__server", "version"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != version {
		t.Fatalf("server version = %q, want %q", out.String(), version)
	}

	t.Setenv("LOSH_SERVER_STATE", t.TempDir())
	request := rpcRequest{
		Method:      "start",
		OperationID: "bad-digest",
		Digest:      "wrong",
		Kind:        "exec",
		Payload:     "true",
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runServer([]string{"__server", "rpc"}, bytes.NewReader(data), &out); err != nil {
		t.Fatal(err)
	}
	var response rpcResponse
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Error, "digest") {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestRunPatchWorkerRecordsDurableResult(t *testing.T) {
	state := t.TempDir()
	t.Setenv("LOSH_SERVER_STATE", state)
	root := t.TempDir()
	opDir, err := serverOperationDir("patch-worker")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(opDir, 0700); err != nil {
		t.Fatal(err)
	}
	record := operationRecord{
		OperationID: "patch-worker",
		Kind:        "patch",
		Root:        root,
		Payload:     "*** Begin Patch\n*** Add File: made.txt\n+durable\n*** End Patch",
	}
	if err := writeJSONFile(filepath.Join(opDir, "request.json"), record); err != nil {
		t.Fatal(err)
	}
	if err := runPatchWorker(opDir, record); err != nil {
		t.Fatal(err)
	}
	status, err := loadOperationStatus(opDir)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "committed" {
		t.Fatalf("status = %#v", status)
	}
	assertTestFile(t, filepath.Join(root, "made.txt"), "durable\n")
}

func mustWriteTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func assertTestFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, string(data), want)
	}
}

func TestRemoteServerIntegration(t *testing.T) {
	target := os.Getenv("LOSH_INTEGRATION_TARGET")
	if target == "" {
		t.Skip("set LOSH_INTEGRATION_TARGET to run")
	}
	serverBinary := os.Getenv("LOSH_SERVER_BINARY")
	if serverBinary == "" {
		t.Fatal("LOSH_SERVER_BINARY must point to a built losh executable")
	}
	state := t.TempDir()
	t.Setenv("LOSH_HOME", state)

	sessionID := shortHash(target + time.Now().UTC().String())
	dirName := ".losh-integration-" + sessionID
	homeSession, err := ensureSession(target, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureRemoteServer(homeSession, true); err != nil {
		t.Fatal(err)
	}

	disconnectID := safeName(homeSession.ID + "-disconnect-" + sessionID)
	disconnectPayload := "sleep 1; printf survived-disconnect"
	disconnectRequest := rpcRequest{
		Method:      "start",
		OperationID: disconnectID,
		Digest:      operationDigest("exec", "", disconnectPayload),
		Kind:        "exec",
		Payload:     disconnectPayload,
	}
	if _, err := callRemoteRPC(homeSession, disconnectRequest); err != nil {
		t.Fatal(err)
	}
	closeArgs := append(sshBaseArgs(homeSession), "-O", "exit", homeSession.Target)
	_ = exec.Command("ssh", closeArgs...).Run()
	time.Sleep(1200 * time.Millisecond)
	disconnected, err := callRemoteRPC(homeSession, rpcRequest{Method: "status", OperationID: disconnectID})
	if err != nil {
		t.Fatal(err)
	}
	if disconnected.State != "exited" || string(disconnected.Stdout) != "survived-disconnect" {
		t.Fatalf("operation after disconnect = %#v", disconnected)
	}

	if err := runRemoteCommand(homeSession, "mkdir-"+sessionID, "mkdir -p -- "+shellQuote(dirName), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = runRemoteCommand(homeSession, "cleanup-"+sessionID, "rm -rf -- "+shellQuote(dirName), io.Discard, io.Discard)
	})

	workspaceSession, err := ensureSession(target, dirName)
	if err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Add File: hello.txt\n+hello from losh-server\n*** End Patch"
	result, err := runRemotePatch(workspaceSession, "patch-"+sessionID, patch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "added hello.txt") {
		t.Fatalf("unexpected patch result: %q", result)
	}
	var stdout bytes.Buffer
	if err := runRemoteCommand(workspaceSession, "read-"+sessionID, "cat hello.txt", &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "hello from losh-server\n" {
		t.Fatalf("remote contents = %q", stdout.String())
	}

	replayed, err := runRemotePatch(workspaceSession, "patch-"+sessionID, patch)
	if err != nil {
		t.Fatal(err)
	}
	if replayed != result {
		t.Fatalf("deduplicated result = %q, want %q", replayed, result)
	}
	claudeWrite := marshalClaudeOperation(t, claudeFileOperation{
		Path:    "claude.txt",
		Content: "before\n",
	})
	if _, err := runRemoteFileOperation(workspaceSession, "claude-write-"+sessionID, "fs-write", claudeWrite); err != nil {
		t.Fatal(err)
	}
	claudeEdit := marshalClaudeOperation(t, claudeFileOperation{
		Path:      "claude.txt",
		OldString: "before",
		NewString: "after",
	})
	if _, err := runRemoteFileOperation(workspaceSession, "claude-edit-"+sessionID, "fs-edit", claudeEdit); err != nil {
		t.Fatal(err)
	}
	claudeRead := marshalClaudeOperation(t, claudeFileOperation{
		Path:  "claude.txt",
		Limit: 10,
	})
	claudeResult, err := runRemoteFileOperation(workspaceSession, "claude-read-"+sessionID, "fs-read", claudeRead)
	if err != nil {
		t.Fatal(err)
	}
	if claudeResult != "     1→after\n" {
		t.Fatalf("remote Claude read = %q", claudeResult)
	}
}
