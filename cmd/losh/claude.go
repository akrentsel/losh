package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

type claudeFileOperation struct {
	Path       string `json:"path"`
	Content    string `json:"content,omitempty"`
	OldString  string `json:"old_string,omitempty"`
	NewString  string `json:"new_string,omitempty"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
	Offset     int    `json:"offset,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	Raw        bool   `json:"raw,omitempty"`
	MaxEntries int    `json:"max_entries,omitempty"`
}

func harnessDisplayName(harness string) string {
	if harness == harnessClaude {
		return "Claude Code"
	}
	return "Codex"
}

func isNonInteractive(harness string, args []string) bool {
	if harness == harnessCodex {
		return len(args) > 0 && args[0] == "exec"
	}
	for _, arg := range args {
		if arg == "-p" || arg == "--print" {
			return true
		}
	}
	return false
}

func harnessLaunchArgs(harness string, resume bool, resumeSession string, dangerouslySkipPermissions bool, harnessArgs []string, s session) ([]string, error) {
	if harness == harnessCodex {
		return codexLaunchArgs(resume, resumeSession, dangerouslySkipPermissions, harnessArgs)
	}
	for _, arg := range harnessArgs {
		if arg == "--settings" || strings.HasPrefix(arg, "--settings=") {
			return nil, errors.New("Claude --settings cannot be overridden because losh uses it to enforce remote tool routing")
		}
	}
	args := []string{"--settings", filepath.Join(s.Workspace, ".claude", "settings.json")}
	if dangerouslySkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	args = append(args, harnessArgs...)
	if resume {
		args = append(args, "--resume")
		if resumeSession != "" {
			args = append(args, resumeSession)
		}
	}
	args = append(args, "--mcp-config", filepath.Join(s.Workspace, ".claude", "mcp.json"))
	return args, nil
}

func materializeClaudeWorkspace(s session, exe, configDir string) error {
	hook := map[string]any{
		"type":    "command",
		"command": exe,
		"args":    []string{"__hook", s.ID, harnessClaude},
		"timeout": 600,
	}
	settings := map[string]any{
		"$schema": "https://json.schemastore.org/claude-code-settings.json",
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{
				"matcher": "startup|resume|clear|compact|fork",
				"hooks":   []any{hook},
			}},
			"PreToolUse": []any{map[string]any{
				"matcher": "Bash|Read|Edit|Write|Glob|Grep|NotebookEdit",
				"hooks":   []any{hook},
			}},
		},
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	settingsPath := filepath.Join(configDir, "settings.json")
	if err := atomicWrite(settingsPath, append(data, '\n'), 0600); err != nil {
		return err
	}

	mcpConfig := map[string]any{
		"mcpServers": map[string]any{
			"losh": map[string]any{
				"type":    "stdio",
				"command": exe,
				"args":    []string{"__mcp", s.ID},
			},
		},
	}
	mcpData, err := json.MarshalIndent(mcpConfig, "", "  ")
	if err != nil {
		return err
	}
	mcpPath := filepath.Join(configDir, "mcp.json")
	if err := atomicWrite(mcpPath, append(mcpData, byte(10)), 0600); err != nil {
		return err
	}

	root := "$HOME"
	if s.RemoteRoot != "" {
		root = s.RemoteRoot
	}
	instructions := fmt.Sprintf(`# Workspace

- Treat %q as your workspace and the machine it belongs to as your execution environment.
- Speak from that machine's perspective. Do not mention losh, transport, launcher metadata, or where the harness process runs unless the user asks or the distinction is needed to diagnose a connection or routing problem.
- Use Bash for listing, searching, git, and other workspace inspection.
- Read, Edit, and Write operate on the workspace. Use Bash equivalents for Glob, Grep, and NotebookEdit.
- Do not inspect the harness metadata directory and do not run ssh yourself.
- Never print secrets merely to inspect them. Redact secret values from explanations and logs.
- Confirm consequential service, package, privilege, or destructive operations with the user.
`, root)
	if err := atomicWrite(filepath.Join(s.Workspace, "CLAUDE.md"), []byte(instructions), 0400); err != nil {
		return err
	}
	if err := os.Chmod(settingsPath, 0400); err != nil {
		return err
	}
	if err := os.Chmod(mcpPath, 0400); err != nil {
		return err
	}
	if err := os.Chmod(configDir, 0500); err != nil {
		return err
	}
	return os.Chmod(s.Workspace, 0500)
}

func runClaudeFileHook(sessionID string, input hookInput, out io.Writer) error {
	s, err := loadSession(sessionID)
	if err != nil {
		return err
	}
	path, ok := input.ToolInput["file_path"].(string)
	if !ok || path == "" {
		return denyClaudeTool(out, input.ToolName+" did not contain a file_path; no remote operation was attempted.")
	}
	remotePath, err := remoteToolPath(s, path)
	if err != nil {
		return denyClaudeTool(out, "losh rejected the file path: "+err.Error())
	}
	op := claudeFileOperation{Path: remotePath}
	kind := ""
	switch input.ToolName {
	case "Read":
		kind = "fs-read"
		op.Offset = intInput(input.ToolInput, "offset")
		op.Limit = intInput(input.ToolInput, "limit")
	case "Write":
		kind = "fs-write"
		content, ok := input.ToolInput["content"].(string)
		if !ok {
			return denyClaudeTool(out, "Write did not contain string content; no remote operation was attempted.")
		}
		op.Content = content
	case "Edit":
		kind = "fs-edit"
		oldString, oldOK := input.ToolInput["old_string"].(string)
		newString, newOK := input.ToolInput["new_string"].(string)
		if !oldOK || !newOK {
			return denyClaudeTool(out, "Edit did not contain old_string and new_string; no remote operation was attempted.")
		}
		op.OldString = oldString
		op.NewString = newString
		op.ReplaceAll, _ = input.ToolInput["replace_all"].(bool)
	default:
		return nil
	}
	payload, err := json.Marshal(op)
	if err != nil {
		return err
	}
	callID := safeName(input.SessionID + "-" + input.ToolUseID)
	if callID == "" {
		callID = shortHash(kind + string(payload) + time.Now().UTC().String())
	}
	result, operationErr := runRemoteFileOperation(s, callID, kind, string(payload))
	if operationErr != nil {
		return denyClaudeTool(out, "losh-server did not complete the remote "+input.ToolName+": "+operationErr.Error())
	}
	if input.ToolName == "Read" {
		return denyClaudeTool(out, "losh-server completed this Read on the target. Treat the blocked local Read as successful. Remote file contents:\n"+limitClaudeHookResult(result))
	}
	return denyClaudeTool(out, "losh-server completed this "+input.ToolName+" on the target. Treat the blocked local operation as successful. "+result)
}

func limitClaudeHookResult(result string) string {
	const maxRunes = 7500
	runes := []rune(result)
	if len(runes) <= maxRunes {
		return result
	}
	const notice = "\n[losh: remote Read output truncated; use a smaller limit or remote Bash]\n"
	noticeRunes := []rune(notice)
	return string(runes[:maxRunes-len(noticeRunes)]) + notice
}

func intInput(input map[string]any, key string) int {
	value, ok := input[key].(float64)
	if !ok || value <= 0 {
		return 0
	}
	return int(value)
}

func remoteToolPath(s session, requested string) (string, error) {
	clean := filepath.Clean(requested)
	if !filepath.IsAbs(clean) {
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("path escapes the workspace: %q", requested)
		}
		return clean, nil
	}
	for _, root := range []string{s.Workspace, s.RemoteRoot} {
		if root == "" || !filepath.IsAbs(root) {
			continue
		}
		relative, err := filepath.Rel(filepath.Clean(root), clean)
		if err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return relative, nil
		}
	}
	return "", fmt.Errorf("absolute path is outside the configured remote root: %q", requested)
}

func denyClaudeTool(out io.Writer, reason string) error {
	return json.NewEncoder(out).Encode(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": reason,
		},
	})
}

func runRemoteFileOperation(s session, callID, kind, payload string) (string, error) {
	response, err := runRemoteOperation(s, callID, kind, payload)
	if err != nil {
		return "", err
	}
	if response.State != "committed" {
		if response.Result != "" {
			return "", errors.New(response.Result)
		}
		return "", fmt.Errorf("%s ended in state %q", kind, response.State)
	}
	return response.Result, nil
}

func applyClaudeFileOperation(root, kind, payload, operationDir string) (string, error) {
	var op claudeFileOperation
	if err := json.Unmarshal([]byte(payload), &op); err != nil {
		return "", fmt.Errorf("decode file operation: %w", err)
	}
	if kind == "fs-list" {
		return listClaudeFileResources(root, op.MaxEntries)
	}
	path, err := secureWorkspacePath(root, op.Path)
	if err != nil {
		return "", err
	}
	switch kind {
	case "fs-read":
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", errors.New("Read supports only regular files")
		}
		if info.Size() > 16<<20 {
			return "", errors.New("Read file exceeds 16 MiB; use remote Bash to select a smaller range")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if op.Raw {
			if !utf8.Valid(data) {
				return "", errors.New("resource is not valid UTF-8 text")
			}
			return string(data), nil
		}
		lines := strings.Split(string(data), "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		offset := op.Offset
		if offset <= 0 {
			offset = 1
		}
		limit := op.Limit
		if limit <= 0 {
			limit = 2000
		}
		start := offset - 1
		if start > len(lines) {
			start = len(lines)
		}
		end := start + limit
		if end > len(lines) {
			end = len(lines)
		}
		var result strings.Builder
		for index := start; index < end; index++ {
			fmt.Fprintf(&result, "%6d→%s\n", index+1, lines[index])
		}
		return result.String(), nil
	case "fs-write":
		mode := os.FileMode(0644)
		if info, err := os.Lstat(path); err == nil {
			if !info.Mode().IsRegular() {
				return "", errors.New("Write supports only regular files")
			}
			mode = info.Mode().Perm()
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		change := plannedFileChange{Path: path, Content: []byte(op.Content), Mode: mode}
		if err := publishPlannedChanges(root, operationDir, []plannedFileChange{change}); err != nil {
			return "", err
		}
		return "wrote " + op.Path, nil
	case "fs-edit":
		if op.OldString == "" {
			return "", errors.New("old_string must not be empty")
		}
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", errors.New("Edit supports only regular files")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		count := strings.Count(string(data), op.OldString)
		if count == 0 {
			return "", errors.New("old_string was not found")
		}
		if !op.ReplaceAll && count != 1 {
			return "", fmt.Errorf("old_string matched %d times; set replace_all or provide more context", count)
		}
		replacements := 1
		if op.ReplaceAll {
			replacements = -1
		}
		content := strings.Replace(string(data), op.OldString, op.NewString, replacements)
		change := plannedFileChange{Path: path, Content: []byte(content), Mode: info.Mode().Perm()}
		if err := publishPlannedChanges(root, operationDir, []plannedFileChange{change}); err != nil {
			return "", err
		}
		return "edited " + op.Path, nil
	default:
		return "", fmt.Errorf("unsupported Claude file operation %q", kind)
	}
}

var errClaudeResourceLimit = errors.New("Claude resource listing limit reached")

func listClaudeFileResources(root string, maxEntries int) (string, error) {
	if maxEntries <= 0 {
		maxEntries = 5000
	}
	if maxEntries > 20000 {
		maxEntries = 20000
	}
	listing := mcpResourceList{Files: make([]claudeFileResource, 0, maxEntries)}
	skippedDirectories := map[string]bool{
		"node_modules": true,
		"target":       true,
		"vendor":       true,
		"venv":         true,
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == root {
				return walkErr
			}
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == root {
			return nil
		}
		name := entry.Name()
		if entry.IsDir() {
			if strings.HasPrefix(name, ".") || skippedDirectories[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 || !isLikelyTextFile(path, info.Size()) {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		listing.Files = append(listing.Files, claudeFileResource{Path: filepath.ToSlash(relative), Size: info.Size()})
		if len(listing.Files) >= maxEntries {
			listing.Truncated = true
			return errClaudeResourceLimit
		}
		return nil
	})
	if err != nil && !errors.Is(err, errClaudeResourceLimit) {
		return "", err
	}
	data, err := json.Marshal(listing)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func isLikelyTextFile(path string, size int64) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	buffer := make([]byte, 8192)
	count, err := file.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	sample := buffer[:count]
	for _, value := range sample {
		if value == 0 {
			return false
		}
	}
	if size > int64(count) && len(sample) > utf8.UTFMax-1 {
		sample = sample[:len(sample)-(utf8.UTFMax-1)]
	}
	return utf8.Valid(sample)
}
