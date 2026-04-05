// Package utils provides common utility functions for the feishu plugin.
package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ExpandTilde replaces a leading "~/" with the user's home directory.
func ExpandTilde(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// EnforcePathUnderRoot returns an error if pathAbs is not under rootAbs.
func EnforcePathUnderRoot(pathAbs, rootAbs string) error {
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil {
		return fmt.Errorf("path not under allowed root: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path escapes allowed root directory")
	}
	return nil
}

// ResolveSendFilePath resolves user path against root (send_file_root or workspace or getwd) and checks containment.
func ResolveSendFilePath(pathStr, rootFromCfg, workspace string) (absPath string, err error) {
	pathStr = strings.TrimSpace(pathStr)
	if pathStr == "" {
		return "", fmt.Errorf("path is empty")
	}
	root := strings.TrimSpace(rootFromCfg)
	var rootAbs string
	if root != "" {
		root = ExpandTilde(root)
		rootAbs, err = filepath.Abs(filepath.Clean(root))
	} else if workspace != "" {
		rootAbs, err = filepath.Abs(filepath.Clean(workspace))
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
	if err := EnforcePathUnderRoot(pathAbs, rootAbs); err != nil {
		return "", err
	}
	return pathAbs, nil
}

// SanitizeFileName returns a safe display/upload name (no path separators, length cap).
func SanitizeFileName(name string, maxRunes int) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "file.bin"
	}
	name = filepath.Base(name)
	name = strings.ReplaceAll(name, string(filepath.Separator), "_")
	if name == "" || name == "." || name == ".." {
		return "file.bin"
	}
	if utf8.RuneCountInString(name) > maxRunes {
		runes := []rune(name)
		name = string(runes[:maxRunes])
	}
	return name
}
