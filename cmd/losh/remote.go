package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type rpcRequest struct {
	Method      string `json:"method"`
	OperationID string `json:"operation_id,omitempty"`
	Digest      string `json:"digest,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Root        string `json:"root,omitempty"`
	Payload     string `json:"payload,omitempty"`
}

type rpcResponse struct {
	State    string `json:"state,omitempty"`
	Error    string `json:"error,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
	Stdout   []byte `json:"stdout,omitempty"`
	Stderr   []byte `json:"stderr,omitempty"`
	Result   string `json:"result,omitempty"`
	Version  string `json:"version,omitempty"`
}

func remoteServerRelativePath() string {
	return ".local/lib/losh/server/" + version + "/losh-server"
}

func remoteServerShellPath() string {
	return `"$HOME/` + remoteServerRelativePath() + `"`
}

func ensureRemoteServer(s session, allowInstall bool) error {
	current, err := remoteServerVersion(s)
	forceInstall := os.Getenv("LOSH_FORCE_SERVER_INSTALL") != ""
	if !forceInstall && err == nil && current == version {
		return nil
	}
	if !allowInstall {
		if err != nil {
			return fmt.Errorf("compatible losh-server is unavailable and --no-install was set: %w", err)
		}
		return fmt.Errorf("losh-server %s is installed; client requires %s and --no-install was set", current, version)
	}

	goos, goarch, err := remotePlatform(s)
	if err != nil {
		return fmt.Errorf("detect remote platform: %w", err)
	}
	binary, err := serverBinaryFor(goos, goarch)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Installing losh-server %s for %s/%s...\n", version, goos, goarch)
	if err := uploadRemoteServer(s, binary); err != nil {
		return fmt.Errorf("install losh-server: %w", err)
	}
	current, err = remoteServerVersion(s)
	if err != nil {
		return fmt.Errorf("verify losh-server installation: %w", err)
	}
	if current != version {
		return fmt.Errorf("installed losh-server reported %q, want %q", current, version)
	}
	fmt.Fprintln(os.Stderr, "Installed losh-server.")
	return nil
}

func remotePlatform(s session) (string, string, error) {
	args := append(sshBaseArgs(s), s.Target, "sh -c "+shellQuote("uname -s; uname -m"))
	out, err := exec.Command("ssh", args...).Output()
	if err != nil {
		return "", "", err
	}
	lines := strings.Fields(string(out))
	if len(lines) != 2 {
		return "", "", fmt.Errorf("unexpected uname response %q", string(out))
	}
	var goos string
	switch strings.ToLower(lines[0]) {
	case "linux":
		goos = "linux"
	case "darwin":
		goos = "darwin"
	default:
		return "", "", fmt.Errorf("unsupported remote OS %q", lines[0])
	}
	var goarch string
	switch strings.ToLower(lines[1]) {
	case "x86_64", "amd64":
		goarch = "amd64"
	case "aarch64", "arm64":
		goarch = "arm64"
	default:
		return "", "", fmt.Errorf("unsupported remote architecture %q", lines[1])
	}
	return goos, goarch, nil
}

func serverBinaryFor(goos, goarch string) (string, error) {
	if configured := os.Getenv("LOSH_SERVER_BINARY"); configured != "" {
		if info, err := os.Stat(configured); err == nil && !info.IsDir() {
			return configured, nil
		}
		return "", fmt.Errorf("LOSH_SERVER_BINARY %q is not a readable file", configured)
	}

	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	platform := goos + "-" + goarch
	base := filepath.Dir(exe)
	candidates := []string{
		filepath.Join(base, "servers", platform, "losh-server"),
		filepath.Join(base, "..", "libexec", "losh", "servers", platform, "losh-server"),
		filepath.Join(base, "..", "libexec", "servers", platform, "losh-server"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	if goos == runtime.GOOS && goarch == runtime.GOARCH {
		return exe, nil
	}
	return "", fmt.Errorf("no bundled losh-server for %s/%s; checked %s", goos, goarch, strings.Join(candidates, ", "))
}

func uploadRemoteServer(s session, localPath string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()

	dir := `"$HOME/.local/lib/losh/server/` + version + `"`
	script := "umask 077\n" +
		"dir=" + dir + "\n" +
		"mkdir -p \"$dir\" || exit $?\n" +
		"tmp=\"$dir/.losh-server.$$\"\n" +
		"trap 'rm -f \"$tmp\"' EXIT HUP INT TERM\n" +
		"cat > \"$tmp\" || exit $?\n" +
		"chmod 700 \"$tmp\" || exit $?\n" +
		"mv -f \"$tmp\" \"$dir/losh-server\" || exit $?\n" +
		"trap - EXIT HUP INT TERM\n"
	args := append(sshBaseArgs(s), s.Target, "sh -c "+shellQuote(script))
	cmd := exec.Command("ssh", args...)
	cmd.Stdin = file
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func remoteServerVersion(s session) (string, error) {
	args := append(sshBaseArgs(s), s.Target, remoteServerShellPath()+" __server version")
	cmd := exec.Command("ssh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func callRemoteRPC(s session, request rpcRequest) (rpcResponse, error) {
	var response rpcResponse
	data, err := json.Marshal(request)
	if err != nil {
		return response, err
	}

	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		args := append(sshBaseArgs(s), s.Target, remoteServerShellPath()+" __server rpc")
		cmd := exec.Command("ssh", args...)
		cmd.Stdin = bytes.NewReader(data)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			lastErr = err
			if stderr.Len() > 0 {
				lastErr = fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
			}
			time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
			continue
		}
		response = rpcResponse{}
		if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
			lastErr = fmt.Errorf("decode losh-server response %q: %w", stdout.String(), err)
			time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
			continue
		}
		if response.Error != "" {
			return response, errors.New(response.Error)
		}
		return response, nil
	}
	return response, fmt.Errorf("losh-server unavailable after reconnect attempts: %w", lastErr)
}

func operationDigest(kind, root, payload string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + root + "\x00" + payload))
	return hex.EncodeToString(sum[:])
}

func runRemoteOperation(s session, callID, kind, payload string) (rpcResponse, error) {
	opID := safeName(s.ID + "-" + callID)
	if opID == "" {
		return rpcResponse{}, errors.New("invalid remote operation ID")
	}
	request := rpcRequest{
		Method:      "start",
		OperationID: opID,
		Digest:      operationDigest(kind, s.RemoteRoot, payload),
		Kind:        kind,
		Root:        s.RemoteRoot,
		Payload:     payload,
	}
	response, err := callRemoteRPC(s, request)
	if err != nil {
		return response, err
	}
	for {
		switch response.State {
		case "exited", "committed", "failed", "uncertain":
			return response, nil
		case "accepted", "running":
			time.Sleep(200 * time.Millisecond)
			response, err = callRemoteRPC(s, rpcRequest{Method: "status", OperationID: opID})
			if err != nil {
				return response, err
			}
		default:
			return response, fmt.Errorf("unexpected losh-server operation state %q", response.State)
		}
	}
}

func runRemoteCommand(s session, callID, command string, stdout, stderr io.Writer) error {
	response, err := runRemoteOperation(s, callID, "exec", command)
	if err != nil {
		return err
	}
	if len(response.Stdout) > 0 {
		_, _ = stdout.Write(response.Stdout)
	}
	if len(response.Stderr) > 0 {
		_, _ = stderr.Write(response.Stderr)
	}
	if response.State == "uncertain" {
		return errors.New("remote command outcome is uncertain; losh will not rerun it automatically")
	}
	if response.State == "failed" {
		return fmt.Errorf("remote command failed: %s", response.Result)
	}
	if response.ExitCode != 0 {
		return exitCodeError{code: response.ExitCode}
	}
	return nil
}

func runRemotePatch(s session, callID, patchText string) (string, error) {
	response, err := runRemoteOperation(s, callID, "patch", patchText)
	if err != nil {
		return "", err
	}
	if response.State != "committed" {
		if response.Result != "" {
			return "", errors.New(response.Result)
		}
		return "", fmt.Errorf("patch ended in state %q", response.State)
	}
	return response.Result, nil
}
