package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

const mcpProtocolVersion = "2024-11-05"

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResourceList struct {
	Files     []claudeFileResource `json:"files"`
	Truncated bool                 `json:"truncated,omitempty"`
}

type claudeFileResource struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

var mcpRemoteFileOperation = runRemoteFileOperation

func runClaudeMCP(sessionID string, in io.Reader, out io.Writer) error {
	s, err := loadSession(sessionID)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(in)
	encoder := json.NewEncoder(out)
	for {
		var request mcpRequest
		if err := decoder.Decode(&request); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decode MCP request: %w", err)
		}
		if err := handleClaudeMCPRequest(s, request, encoder); err != nil {
			return err
		}
	}
}

func handleClaudeMCPRequest(s session, request mcpRequest, encoder *json.Encoder) error {
	if len(request.ID) == 0 {
		return nil
	}
	switch request.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(request.Params, &params)
		protocolVersion := params.ProtocolVersion
		if protocolVersion == "" {
			protocolVersion = mcpProtocolVersion
		}
		return writeMCPResult(encoder, request.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities": map[string]any{
				"resources": map[string]any{"subscribe": false, "listChanged": false},
			},
			"serverInfo":   map[string]any{"name": "losh", "version": version},
			"instructions": "Provides read-only resources from the active workspace. Shell commands and mutations continue to use the normal workspace tools.",
		})
	case "ping":
		return writeMCPResult(encoder, request.ID, map[string]any{})
	case "resources/list":
		return listClaudeMCPResources(s, request.ID, encoder)
	case "resources/read":
		return readClaudeMCPResource(s, request, encoder)
	case "resources/templates/list":
		return writeMCPResult(encoder, request.ID, map[string]any{"resourceTemplates": []any{}})
	default:
		return writeMCPError(encoder, request.ID, -32601, "method not found")
	}
}

func listClaudeMCPResources(s session, id json.RawMessage, encoder *json.Encoder) error {
	payload, err := json.Marshal(claudeFileOperation{MaxEntries: 5000})
	if err != nil {
		return err
	}
	result, err := mcpRemoteFileOperation(s, mcpCallID("list", string(payload)), "fs-list", string(payload))
	if err != nil {
		return writeMCPError(encoder, id, -32000, "could not list workspace resources: "+err.Error())
	}
	var listing mcpResourceList
	if err := json.Unmarshal([]byte(result), &listing); err != nil {
		return writeMCPError(encoder, id, -32000, "invalid workspace resource list: "+err.Error())
	}
	resources := make([]map[string]any, 0, len(listing.Files))
	for _, file := range listing.Files {
		uri := claudeResourceURI(file.Path)
		resources = append(resources, map[string]any{
			"uri":         uri,
			"name":        file.Path,
			"description": "Workspace file",
			"mimeType":    resourceMIMEType(file.Path),
			"size":        file.Size,
		})
	}
	response := map[string]any{"resources": resources}
	if listing.Truncated {
		response["_meta"] = map[string]any{"losh/truncated": true}
	}
	return writeMCPResult(encoder, id, response)
}

func readClaudeMCPResource(s session, request mcpRequest, encoder *json.Encoder) error {
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil || params.URI == "" {
		return writeMCPError(encoder, request.ID, -32602, "resources/read requires a URI")
	}
	path, err := claudeResourcePath(params.URI)
	if err != nil {
		return writeMCPError(encoder, request.ID, -32602, err.Error())
	}
	payload, err := json.Marshal(claudeFileOperation{Path: path, Raw: true})
	if err != nil {
		return err
	}
	result, err := mcpRemoteFileOperation(s, mcpCallID("read", path), "fs-read", string(payload))
	if err != nil {
		return writeMCPError(encoder, request.ID, -32000, "could not read workspace resource: "+err.Error())
	}
	return writeMCPResult(encoder, request.ID, map[string]any{
		"contents": []map[string]any{{
			"uri":      params.URI,
			"mimeType": resourceMIMEType(path),
			"text":     result,
		}},
	})
}

func mcpCallID(kind, value string) string {
	return "mcp-" + kind + "-" + shortHash(value+"\x00"+time.Now().UTC().String())
}

func claudeResourceURI(path string) string {
	return (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(path)}).String()
}

func claudeResourcePath(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("invalid workspace resource URI: %w", err)
	}
	if parsed.Scheme != "file" || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("resource URI is not in the active losh workspace")
	}
	path := strings.TrimPrefix(parsed.Path, "/")
	if path == "" {
		return "", errors.New("workspace resource URI does not name a file")
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("workspace resource path escapes the active root")
	}
	return clean, nil
}

func resourceMIMEType(path string) string {
	kind := mime.TypeByExtension(filepath.Ext(path))
	if kind == "" {
		return "text/plain"
	}
	return kind
}

func writeMCPResult(encoder *json.Encoder, id json.RawMessage, result any) error {
	return encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func writeMCPError(encoder *json.Encoder, id json.RawMessage, code int, message string) error {
	return encoder.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
}
