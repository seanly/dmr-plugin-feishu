package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxSendFileNameRunes = 200

// sendFileToolParamsJSON is the JSON Schema for feishu.send_file (validated further in code).
func sendFileToolParamsJSON() string {
	schema := map[string]any{
		"type":     "object",
		"required": []string{"path"},
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Local file path. Relative paths join send_file_root (if configured) or cwd; absolute paths must still lie under that root.",
			},
			"caption": map[string]any{
				"type":        "string",
				"description": "Optional short text sent before the file.",
			},
			"filename": map[string]any{
				"type":        "string",
				"description": "Optional display/upload name with extension; default is basename of path.",
			},
		},
	}
	b, _ := json.Marshal(schema)
	return string(b)
}

func argString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

// sanitizeFileName returns a safe display/upload name (no path separators, length cap).
func sanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "file.bin"
	}
	name = filepath.Base(name)
	name = strings.ReplaceAll(name, string(filepath.Separator), "_")
	if name == "" || name == "." || name == ".." {
		return "file.bin"
	}
	if utf8.RuneCountInString(name) > maxSendFileNameRunes {
		runes := []rune(name)
		name = string(runes[:maxSendFileNameRunes])
	}
	return name
}

// enforcePathUnderRoot returns an error if pathAbs is not under rootAbs.
func enforcePathUnderRoot(pathAbs, rootAbs string) error {
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil {
		return fmt.Errorf("path not under allowed root: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes allowed root directory")
	}
	return nil
}

// resolveSendFilePath resolves user path against root (send_file_root or getwd) and checks containment.
func resolveSendFilePath(pathStr, rootFromCfg string) (absPath string, err error) {
	pathStr = strings.TrimSpace(pathStr)
	if pathStr == "" {
		return "", fmt.Errorf("path is empty")
	}
	root := strings.TrimSpace(rootFromCfg)
	var rootAbs string
	if root != "" {
		rootAbs, err = filepath.Abs(filepath.Clean(root))
	} else {
		cwd, e := os.Getwd()
		if e != nil {
			return "", fmt.Errorf("getwd: %w", e)
		}
		rootAbs, err = filepath.Abs(cwd)
	}
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}

	cleaned := filepath.Clean(pathStr)
	var pathAbs string
	if filepath.IsAbs(cleaned) {
		pathAbs = cleaned
	} else {
		pathAbs = filepath.Join(rootAbs, cleaned)
	}
	pathAbs, err = filepath.Abs(pathAbs)
	if err != nil {
		return "", err
	}
	if err := enforcePathUnderRoot(pathAbs, rootAbs); err != nil {
		return "", err
	}
	return pathAbs, nil
}

// execSendFile runs feishu.send_file using the active inbound job (Feishu-triggered RunAgent only).
func (p *FeishuPlugin) execSendFile(ctx context.Context, argsJSON string) (map[string]any, error) {
	job := p.getActiveJob()
	if job == nil {
		return nil, fmt.Errorf("feishu.send_file only works during a Feishu-triggered RunAgent (no active chat context)")
	}

	var raw map[string]any
	if strings.TrimSpace(argsJSON) == "" {
		raw = map[string]any{}
	} else if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
		return nil, fmt.Errorf("invalid tool arguments JSON: %w", err)
	}

	if argString(raw, "content_base64") != "" {
		return nil, fmt.Errorf("content_base64 is not supported; write the file to disk (e.g. fs.write) and pass path")
	}

	pathStr := argString(raw, "path")
	if pathStr == "" {
		return nil, fmt.Errorf("path is required")
	}

	filenameArg := argString(raw, "filename")
	caption := argString(raw, "caption")

	maxBytes := p.cfg.sendFileMaxBytes()

	if caption != "" {
		_ = p.sendTextToChat(ctx, job.ChatID, truncateRunes(caption, maxFeishuTextRunes))
	}

	abs, err := resolveSendFilePath(pathStr, p.cfg.SendFileRoot)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat path: %w", err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("path is a directory, not a file")
	}
	if fi.Size() > maxBytes {
		return nil, fmt.Errorf("file size %d exceeds limit %d bytes", fi.Size(), maxBytes)
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	var fileName string
	if strings.TrimSpace(filenameArg) != "" {
		fileName = sanitizeFileName(filenameArg)
	} else {
		fileName = sanitizeFileName(filepath.Base(abs))
	}
	key, err := p.sendFileFromReader(ctx, job, fileName, io.LimitReader(f, fi.Size()))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":        true,
		"file_key":  key,
		"filename":  fileName,
		"size":      fi.Size(),
		"chat_id":   job.ChatID,
		"in_thread": job.InThread,
	}, nil
}
