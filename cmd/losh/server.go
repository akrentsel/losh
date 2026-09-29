package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

type operationRecord struct {
	OperationID string `json:"operation_id"`
	Digest      string `json:"digest"`
	Kind        string `json:"kind"`
	Root        string `json:"root,omitempty"`
	Payload     string `json:"payload"`
}

type operationStatus struct {
	State     string    `json:"state"`
	ExitCode  int       `json:"exit_code,omitempty"`
	Result    string    `json:"result,omitempty"`
	PID       int       `json:"pid,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

func runServer(args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing server command")
	}
	if args[0] == "__server-worker" {
		if len(args) != 2 {
			return errors.New("invalid server worker invocation")
		}
		return runServerWorker(args[1])
	}
	if len(args) != 2 {
		return errors.New("invalid server invocation")
	}
	switch args[1] {
	case "version":
		_, err := fmt.Fprintln(out, version)
		return err
	case "rpc":
		var request rpcRequest
		if err := json.NewDecoder(io.LimitReader(in, 64<<20)).Decode(&request); err != nil {
			return fmt.Errorf("decode request: %w", err)
		}
		response := handleServerRPC(request)
		return json.NewEncoder(out).Encode(response)
	default:
		return fmt.Errorf("unknown server command %q", args[1])
	}
}

func handleServerRPC(request rpcRequest) rpcResponse {
	switch request.Method {
	case "start":
		return serverStartOperation(request)
	case "status":
		return serverOperationStatus(request.OperationID)
	default:
		return rpcResponse{Error: fmt.Sprintf("unknown RPC method %q", request.Method)}
	}
}

func serverDataRoot() (string, error) {
	if configured := os.Getenv("LOSH_SERVER_STATE"); configured != "" {
		return configured, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "losh"), nil
}

func serverOperationDir(id string) (string, error) {
	if id == "" || safeName(id) != id || len(id) > 200 {
		return "", errors.New("invalid operation ID")
	}
	root, err := serverDataRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "operations", id), nil
}

func serverStartOperation(request rpcRequest) rpcResponse {
	if request.OperationID == "" || request.Digest == "" {
		return rpcResponse{Error: "start requires operation_id and digest"}
	}
	if request.Kind != "exec" && request.Kind != "patch" {
		return rpcResponse{Error: fmt.Sprintf("unsupported operation kind %q", request.Kind)}
	}
	if got := operationDigest(request.Kind, request.Root, request.Payload); got != request.Digest {
		return rpcResponse{Error: "request digest does not match operation contents"}
	}
	opDir, err := serverOperationDir(request.OperationID)
	if err != nil {
		return rpcResponse{Error: err.Error()}
	}

	record := operationRecord{
		OperationID: request.OperationID,
		Digest:      request.Digest,
		Kind:        request.Kind,
		Root:        request.Root,
		Payload:     request.Payload,
	}
	if _, err := os.Stat(opDir); err == nil {
		existing, loadErr := loadOperationRecord(opDir)
		if loadErr != nil {
			return rpcResponse{Error: fmt.Sprintf("load existing operation: %v", loadErr)}
		}
		if existing.Digest != record.Digest {
			return rpcResponse{Error: "operation ID already exists with a different request"}
		}
		return serverOperationStatus(request.OperationID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return rpcResponse{Error: err.Error()}
	}

	operationsDir := filepath.Dir(opDir)
	if err := os.MkdirAll(operationsDir, 0700); err != nil {
		return rpcResponse{Error: err.Error()}
	}
	staging, err := os.MkdirTemp(operationsDir, ".new-operation-")
	if err != nil {
		return rpcResponse{Error: err.Error()}
	}
	defer os.RemoveAll(staging)
	if err := writeJSONFile(filepath.Join(staging, "request.json"), record); err != nil {
		return rpcResponse{Error: err.Error()}
	}
	accepted := operationStatus{State: "accepted", UpdatedAt: time.Now().UTC()}
	if err := writeJSONFile(filepath.Join(staging, "status.json"), accepted); err != nil {
		return rpcResponse{Error: err.Error()}
	}
	if err := os.Rename(staging, opDir); err != nil {
		if errors.Is(err, os.ErrExist) {
			return serverStartOperation(request)
		}
		return rpcResponse{Error: err.Error()}
	}
	if err := syncDirectory(operationsDir); err != nil {
		return rpcResponse{Error: fmt.Sprintf("persist operation: %v", err)}
	}

	if err := launchServerWorker(opDir); err != nil {
		failed := operationStatus{State: "failed", Result: "could not launch operation worker: " + err.Error(), UpdatedAt: time.Now().UTC()}
		_ = writeJSONFile(filepath.Join(opDir, "status.json"), failed)
		return responseFromStatus(failed, opDir)
	}
	return serverOperationStatus(request.OperationID)
}

func launchServerWorker(opDir string) error {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer null.Close()

	cmd := exec.Command(os.Args[0], "__server-worker", opDir)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	// The worker owns all state transitions after launch. Writing "running"
	// here can race with a fast worker and overwrite its terminal result.
	return cmd.Process.Release()
}

func runServerWorker(opDir string) error {
	record, err := loadOperationRecord(opDir)
	if err != nil {
		return err
	}
	running := operationStatus{State: "running", PID: os.Getpid(), UpdatedAt: time.Now().UTC()}
	if err := writeJSONFile(filepath.Join(opDir, "status.json"), running); err != nil {
		return err
	}

	switch record.Kind {
	case "exec":
		return runExecWorker(opDir, record)
	case "patch":
		return runPatchWorker(opDir, record)
	default:
		return finishOperation(opDir, operationStatus{State: "failed", Result: "unsupported operation kind", UpdatedAt: time.Now().UTC()})
	}
}

func resolveOperationRoot(root string) (string, error) {
	if root == "" {
		return os.UserHomeDir()
	}
	if filepath.IsAbs(root) {
		return filepath.Clean(root), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, root), nil
}

func runExecWorker(opDir string, record operationRecord) error {
	root, err := resolveOperationRoot(record.Root)
	if err != nil {
		return finishOperation(opDir, operationStatus{State: "failed", Result: err.Error(), UpdatedAt: time.Now().UTC()})
	}
	stdout, err := os.OpenFile(filepath.Join(opDir, "stdout"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(filepath.Join(opDir, "stderr"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer stderr.Close()

	cmd := exec.Command("/bin/sh", "-c", record.Payload)
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Run()
	_ = stdout.Sync()
	_ = stderr.Sync()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return finishOperation(opDir, operationStatus{State: "failed", Result: err.Error(), UpdatedAt: time.Now().UTC()})
		}
	}
	return finishOperation(opDir, operationStatus{State: "exited", ExitCode: exitCode, UpdatedAt: time.Now().UTC()})
}

func runPatchWorker(opDir string, record operationRecord) error {
	root, err := resolveOperationRoot(record.Root)
	if err != nil {
		return finishOperation(opDir, operationStatus{State: "failed", Result: err.Error(), UpdatedAt: time.Now().UTC()})
	}
	result, err := applyAgentPatch(root, record.Payload, opDir)
	if err != nil {
		return finishOperation(opDir, operationStatus{State: "failed", Result: err.Error(), UpdatedAt: time.Now().UTC()})
	}
	return finishOperation(opDir, operationStatus{State: "committed", Result: result, UpdatedAt: time.Now().UTC()})
}

func finishOperation(opDir string, status operationStatus) error {
	if err := writeJSONFile(filepath.Join(opDir, "status.json"), status); err != nil {
		return err
	}
	return syncDirectory(opDir)
}

func serverOperationStatus(id string) rpcResponse {
	opDir, err := serverOperationDir(id)
	if err != nil {
		return rpcResponse{Error: err.Error()}
	}
	status, err := loadOperationStatus(opDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rpcResponse{Error: "operation not found"}
		}
		return rpcResponse{Error: err.Error()}
	}
	if status.State == "running" && status.PID > 0 {
		if err := syscall.Kill(status.PID, 0); errors.Is(err, syscall.ESRCH) {
			status.State = "uncertain"
			status.Result = "operation worker disappeared before recording a terminal result"
			status.UpdatedAt = time.Now().UTC()
			_ = writeJSONFile(filepath.Join(opDir, "status.json"), status)
		}
	}
	if status.State == "accepted" && time.Since(status.UpdatedAt) > 30*time.Second {
		status.State = "uncertain"
		status.Result = "operation was accepted but worker launch could not be confirmed"
		status.UpdatedAt = time.Now().UTC()
		_ = writeJSONFile(filepath.Join(opDir, "status.json"), status)
	}
	return responseFromStatus(status, opDir)
}

func responseFromStatus(status operationStatus, opDir string) rpcResponse {
	response := rpcResponse{
		State:    status.State,
		ExitCode: status.ExitCode,
		Result:   status.Result,
	}
	if status.State == "exited" || status.State == "failed" || status.State == "uncertain" {
		response.Stdout, _ = os.ReadFile(filepath.Join(opDir, "stdout"))
		response.Stderr, _ = os.ReadFile(filepath.Join(opDir, "stderr"))
	}
	return response
}

func loadOperationRecord(opDir string) (operationRecord, error) {
	var record operationRecord
	err := readJSONFile(filepath.Join(opDir, "request.json"), &record)
	return record, err
}

func loadOperationStatus(opDir string) (operationStatus, error) {
	var status operationStatus
	err := readJSONFile(filepath.Join(opDir, "status.json"), &status)
	return status, err
}

func readJSONFile(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := durableAtomicWrite(path, append(data, '\n'), 0600); err != nil {
		return err
	}
	return nil
}

func durableAtomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".losh-write-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, bytes.NewReader(data)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		// Some filesystems do not implement directory fsync. Keep this explicit
		// in the operation result rather than silently claiming durability.
		return fmt.Errorf("fsync directory %s: %w", path, err)
	}
	return nil
}
