package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type agentFilePatch struct {
	Action string
	Path   string
	MoveTo string
	Body   []string
}

type patchHunk struct {
	Old   []string
	New   []string
	AtEOF bool
}

type plannedFileChange struct {
	Path    string
	Delete  bool
	Content []byte
	Mode    os.FileMode
}

type stagedFileChange struct {
	plannedFileChange
	TempPath   string
	BackupPath string
	Existed    bool
	Published  bool
}

func applyAgentPatch(root, patchText, operationDir string) (string, error) {
	filePatches, err := parseAgentPatch(patchText)
	if err != nil {
		return "", err
	}
	changes, descriptions, err := planAgentPatch(root, filePatches)
	if err != nil {
		return "", err
	}
	if err := publishPlannedChanges(root, operationDir, changes); err != nil {
		return "", err
	}
	return strings.Join(descriptions, "; "), nil
}

func parseAgentPatch(patchText string) ([]agentFilePatch, error) {
	normalized := strings.ReplaceAll(patchText, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) < 2 || lines[0] != "*** Begin Patch" {
		return nil, errors.New("patch must start with *** Begin Patch")
	}
	var patches []agentFilePatch
	for index := 1; index < len(lines); {
		line := lines[index]
		if line == "*** End Patch" {
			if len(patches) == 0 {
				return nil, errors.New("patch contains no file operations")
			}
			for _, trailing := range lines[index+1:] {
				if trailing != "" {
					return nil, errors.New("unexpected text after *** End Patch")
				}
			}
			return patches, nil
		}

		current := agentFilePatch{}
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			current.Action = "add"
			current.Path = strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: "))
		case strings.HasPrefix(line, "*** Update File: "):
			current.Action = "update"
			current.Path = strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: "))
		case strings.HasPrefix(line, "*** Delete File: "):
			current.Action = "delete"
			current.Path = strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File: "))
		default:
			return nil, fmt.Errorf("unexpected patch line %q", line)
		}
		if current.Path == "" {
			return nil, errors.New("patch file path is empty")
		}
		index++

		for index < len(lines) {
			line = lines[index]
			if line == "*** End Patch" || strings.HasPrefix(line, "*** Add File: ") ||
				strings.HasPrefix(line, "*** Update File: ") || strings.HasPrefix(line, "*** Delete File: ") {
				break
			}
			if current.Action == "update" && strings.HasPrefix(line, "*** Move to: ") {
				if current.MoveTo != "" {
					return nil, fmt.Errorf("duplicate move destination for %s", current.Path)
				}
				current.MoveTo = strings.TrimSpace(strings.TrimPrefix(line, "*** Move to: "))
				if current.MoveTo == "" {
					return nil, fmt.Errorf("empty move destination for %s", current.Path)
				}
				index++
				continue
			}
			current.Body = append(current.Body, line)
			index++
		}
		patches = append(patches, current)
	}
	return nil, errors.New("patch is missing *** End Patch")
}

func planAgentPatch(root string, patches []agentFilePatch) ([]plannedFileChange, []string, error) {
	seen := make(map[string]bool)
	var changes []plannedFileChange
	var descriptions []string

	for _, patch := range patches {
		path, err := secureWorkspacePath(root, patch.Path)
		if err != nil {
			return nil, nil, err
		}
		if seen[path] {
			return nil, nil, fmt.Errorf("patch modifies %s more than once", patch.Path)
		}
		seen[path] = true

		switch patch.Action {
		case "add":
			if _, err := os.Lstat(path); err == nil {
				return nil, nil, fmt.Errorf("cannot add %s: file already exists", patch.Path)
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, nil, err
			}
			content, err := contentFromAddPatch(patch.Body)
			if err != nil {
				return nil, nil, fmt.Errorf("add %s: %w", patch.Path, err)
			}
			changes = append(changes, plannedFileChange{Path: path, Content: []byte(content), Mode: 0644})
			descriptions = append(descriptions, "added "+patch.Path)

		case "update":
			info, err := os.Lstat(path)
			if err != nil {
				return nil, nil, fmt.Errorf("update %s: %w", patch.Path, err)
			}
			if !info.Mode().IsRegular() {
				return nil, nil, fmt.Errorf("update %s: only regular files are supported", patch.Path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, nil, fmt.Errorf("update %s: %w", patch.Path, err)
			}
			content, err := applyUpdatePatch(string(data), patch.Body)
			if err != nil {
				return nil, nil, fmt.Errorf("update %s: %w", patch.Path, err)
			}
			destination := path
			displayPath := patch.Path
			if patch.MoveTo != "" {
				destination, err = secureWorkspacePath(root, patch.MoveTo)
				if err != nil {
					return nil, nil, err
				}
				if seen[destination] {
					return nil, nil, fmt.Errorf("patch destination %s is modified more than once", patch.MoveTo)
				}
				seen[destination] = true
				if _, err := os.Lstat(destination); err == nil {
					return nil, nil, fmt.Errorf("cannot move to %s: destination exists", patch.MoveTo)
				} else if !errors.Is(err, os.ErrNotExist) {
					return nil, nil, err
				}
				changes = append(changes, plannedFileChange{Path: path, Delete: true, Mode: info.Mode().Perm()})
				displayPath = patch.Path + " to " + patch.MoveTo
			}
			changes = append(changes, plannedFileChange{Path: destination, Content: []byte(content), Mode: info.Mode().Perm()})
			if patch.MoveTo == "" {
				descriptions = append(descriptions, "updated "+displayPath)
			} else {
				descriptions = append(descriptions, "moved "+displayPath)
			}

		case "delete":
			if len(patch.Body) != 0 {
				return nil, nil, fmt.Errorf("delete %s: unexpected patch body", patch.Path)
			}
			info, err := os.Lstat(path)
			if err != nil {
				return nil, nil, fmt.Errorf("delete %s: %w", patch.Path, err)
			}
			if !info.Mode().IsRegular() {
				return nil, nil, fmt.Errorf("delete %s: only regular files are supported", patch.Path)
			}
			changes = append(changes, plannedFileChange{Path: path, Delete: true, Mode: info.Mode().Perm()})
			descriptions = append(descriptions, "deleted "+patch.Path)
		default:
			return nil, nil, fmt.Errorf("unsupported patch action %q", patch.Action)
		}
	}
	return changes, descriptions, nil
}

func contentFromAddPatch(lines []string) (string, error) {
	if len(lines) == 0 {
		return "", nil
	}
	result := make([]string, 0, len(lines))
	noFinalNewline := false
	for _, line := range lines {
		if line == "\\ No newline at end of file" {
			noFinalNewline = true
			continue
		}
		if !strings.HasPrefix(line, "+") {
			return "", fmt.Errorf("added-file line must start with +: %q", line)
		}
		result = append(result, strings.TrimPrefix(line, "+"))
	}
	content := strings.Join(result, "\n")
	if !noFinalNewline && len(result) > 0 {
		content += "\n"
	}
	return content, nil
}

func applyUpdatePatch(original string, body []string) (string, error) {
	hunks, err := parsePatchHunks(body)
	if err != nil {
		return "", err
	}
	lines, trailingNewline := splitTextLines(original)
	cursor := 0
	for hunkIndex, hunk := range hunks {
		position := -1
		if len(hunk.Old) == 0 {
			position = cursor
			if hunk.AtEOF {
				position = len(lines)
			}
		} else {
			position = findLineSequence(lines, hunk.Old, cursor)
			if position == -2 {
				return "", fmt.Errorf("hunk %d context is ambiguous", hunkIndex+1)
			}
			if position < 0 {
				return "", fmt.Errorf("hunk %d context was not found", hunkIndex+1)
			}
			if hunk.AtEOF && position+len(hunk.Old) != len(lines) {
				return "", fmt.Errorf("hunk %d expected to match at end of file", hunkIndex+1)
			}
		}
		next := make([]string, 0, len(lines)-len(hunk.Old)+len(hunk.New))
		next = append(next, lines[:position]...)
		next = append(next, hunk.New...)
		next = append(next, lines[position+len(hunk.Old):]...)
		lines = next
		cursor = position + len(hunk.New)
	}
	return joinTextLines(lines, trailingNewline), nil
}

func parsePatchHunks(body []string) ([]patchHunk, error) {
	var hunks []patchHunk
	var current *patchHunk
	flush := func() {
		if current != nil {
			hunks = append(hunks, *current)
			current = nil
		}
	}
	for _, line := range body {
		if strings.HasPrefix(line, "@@") {
			flush()
			current = &patchHunk{}
			continue
		}
		if current == nil {
			current = &patchHunk{}
		}
		if line == "*** End of File" {
			current.AtEOF = true
			continue
		}
		if line == "\\ No newline at end of file" {
			continue
		}
		if line == "" {
			return nil, errors.New("empty hunk line must carry a context prefix")
		}
		switch line[0] {
		case ' ':
			text := line[1:]
			current.Old = append(current.Old, text)
			current.New = append(current.New, text)
		case '-':
			current.Old = append(current.Old, line[1:])
		case '+':
			current.New = append(current.New, line[1:])
		default:
			return nil, fmt.Errorf("invalid hunk line %q", line)
		}
	}
	flush()
	if len(hunks) == 0 {
		return nil, errors.New("update contains no hunks")
	}
	return hunks, nil
}

func splitTextLines(content string) ([]string, bool) {
	if content == "" {
		return nil, false
	}
	trailing := strings.HasSuffix(content, "\n")
	if trailing {
		content = strings.TrimSuffix(content, "\n")
	}
	return strings.Split(content, "\n"), trailing
}

func joinTextLines(lines []string, trailing bool) string {
	if len(lines) == 0 {
		if trailing {
			return "\n"
		}
		return ""
	}
	content := strings.Join(lines, "\n")
	if trailing {
		content += "\n"
	}
	return content
}

func findLineSequence(lines, sequence []string, start int) int {
	if len(sequence) == 0 {
		return start
	}
	found := -1
	for index := start; index+len(sequence) <= len(lines); index++ {
		match := true
		for offset := range sequence {
			if lines[index+offset] != sequence[offset] {
				match = false
				break
			}
		}
		if match {
			if found >= 0 {
				return -2
			}
			found = index
		}
	}
	return found
}

func secureWorkspacePath(root, requested string) (string, error) {
	if requested == "" || filepath.IsAbs(requested) {
		return "", fmt.Errorf("workspace path must be relative: %q", requested)
	}
	clean := filepath.Clean(requested)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace path escapes root: %q", requested)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	target := filepath.Join(resolvedRoot, clean)
	parent := filepath.Dir(target)
	existingParent := parent
	for {
		if _, err := os.Lstat(existingParent); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		next := filepath.Dir(existingParent)
		if next == existingParent {
			return "", errors.New("could not resolve workspace parent")
		}
		existingParent = next
	}
	resolvedParent, err := filepath.EvalSymlinks(existingParent)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedParent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace path traverses outside root: %q", requested)
	}
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("workspace path is a symlink: %q", requested)
	}
	return target, nil
}

func publishPlannedChanges(root, operationDir string, changes []plannedFileChange) error {
	suffix := ".losh-" + shortHash(operationDir)
	staged := make([]stagedFileChange, 0, len(changes))
	cleanup := func() {
		for _, change := range staged {
			if change.TempPath != "" {
				_ = os.Remove(change.TempPath)
			}
		}
	}
	defer cleanup()

	for _, change := range changes {
		stagedChange := stagedFileChange{plannedFileChange: change}
		if info, err := os.Lstat(change.Path); err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("unsupported file type at %s", change.Path)
			}
			stagedChange.Existed = true
			stagedChange.BackupPath = change.Path + suffix + ".backup"
			if _, err := os.Lstat(stagedChange.BackupPath); err == nil {
				return fmt.Errorf("recovery path already exists: %s", stagedChange.BackupPath)
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if !change.Delete {
			if err := os.MkdirAll(filepath.Dir(change.Path), 0755); err != nil {
				return err
			}
			tmp, err := os.CreateTemp(filepath.Dir(change.Path), "."+filepath.Base(change.Path)+".losh-stage-")
			if err != nil {
				return err
			}
			stagedChange.TempPath = tmp.Name()
			if err := tmp.Chmod(change.Mode); err != nil {
				tmp.Close()
				return err
			}
			if _, err := io.Copy(tmp, strings.NewReader(string(change.Content))); err != nil {
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
		}
		staged = append(staged, stagedChange)
	}

	rollback := func(last int) {
		for index := last; index >= 0; index-- {
			change := &staged[index]
			if !change.Published {
				continue
			}
			if !change.Delete {
				_ = os.Remove(change.Path)
			}
			if change.Existed && change.BackupPath != "" {
				_ = os.Rename(change.BackupPath, change.Path)
			}
		}
	}

	for index := range staged {
		change := &staged[index]
		if change.Existed {
			if err := os.Rename(change.Path, change.BackupPath); err != nil {
				rollback(index - 1)
				return fmt.Errorf("prepare recovery for %s: %w", change.Path, err)
			}
		}
		if !change.Delete {
			if err := os.Rename(change.TempPath, change.Path); err != nil {
				if change.Existed {
					_ = os.Rename(change.BackupPath, change.Path)
				}
				rollback(index - 1)
				return fmt.Errorf("publish %s: %w", change.Path, err)
			}
			change.TempPath = ""
		}
		change.Published = true
		if err := syncDirectory(filepath.Dir(change.Path)); err != nil {
			rollback(index)
			return err
		}
	}

	for _, change := range staged {
		if change.BackupPath != "" {
			if err := os.Remove(change.BackupPath); err == nil {
				_ = syncDirectory(filepath.Dir(change.Path))
			}
			// A leftover recovery copy is garbage-collection work. The live
			// publication is already committed and must not be reported failed.
		}
	}
	_ = root
	return nil
}
