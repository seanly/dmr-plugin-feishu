package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/seanly/dmr-plugin-feishu/pkg/utils"
)

const maxSendFileNameRunes = 200

// SendFileParams returns the JSON schema for feishuSendFile.
func SendFileParams() string {
	schema := map[string]any{
		"type":     "object",
		"required": []string{"path"},
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Local file path. Relative paths join send_file_root if configured.",
			},
			"caption": map[string]any{
				"type":        "string",
				"description": "Optional short text sent before the file.",
			},
			"filename": map[string]any{
				"type":        "string",
				"description": "Optional display/upload name with extension.",
			},
		},
	}
	b, _ := json.Marshal(schema)
	return string(b)
}

// FileClient defines the interface for file operations.
type FileClient interface {
	SendTextToChat(ctx context.Context, chatID, text string) error
	SendFileFromReader(ctx context.Context, chatID, triggerMessageID string, inThread bool, fileName string, r io.Reader) (string, error)
}

// ExecuteSendFile runs feishuSendFile.
func ExecuteSendFile(ctx context.Context, argsJSON string, jobChatID string, jobInThread bool, jobTriggerMessageID string, jobBot FileClient, maxBytes int64, sendFileRoot, workspace string) (map[string]any, error) {
	if jobBot == nil || jobChatID == "" {
		return nil, fmt.Errorf("feishuSendFile only works during a Feishu-triggered RunAgent")
	}

	var raw map[string]any
	if strings.TrimSpace(argsJSON) == "" {
		raw = map[string]any{}
	} else if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
		return nil, fmt.Errorf("invalid tool arguments JSON: %w", err)
	}

	if ArgString(raw, "content_base64") != "" {
		return nil, fmt.Errorf("content_base64 is not supported; write the file to disk and pass path")
	}

	pathStr := ArgString(raw, "path")
	if pathStr == "" {
		return nil, fmt.Errorf("path is required")
	}

	filenameArg := ArgString(raw, "filename")
	caption := ArgString(raw, "caption")

	if caption != "" {
		_ = jobBot.SendTextToChat(ctx, jobChatID, utils.TruncateRunes(caption, 18000))
	}

	abs, err := utils.ResolveSendFilePath(pathStr, sendFileRoot, workspace)
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
		fileName = utils.SanitizeFileName(filenameArg, maxSendFileNameRunes)
	} else {
		fileName = utils.SanitizeFileName(filepath.Base(abs), maxSendFileNameRunes)
	}

	key, err := jobBot.SendFileFromReader(ctx, jobChatID, jobTriggerMessageID, jobInThread, fileName, io.LimitReader(f, fi.Size()))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":        true,
		"file_key":  key,
		"filename":  fileName,
		"size":      fi.Size(),
		"chat_id":   jobChatID,
		"in_thread": jobInThread,
	}, nil
}
