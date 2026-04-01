package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeFileName(t *testing.T) {
	if g := sanitizeFileName(""); g != "file.bin" {
		t.Fatalf("empty: got %q", g)
	}
	if g := sanitizeFileName("../../etc/passwd"); g != "passwd" {
		t.Fatalf("basename: got %q", g)
	}
	long := strings.Repeat("a", maxSendFileNameRunes+50)
	if utf8Len(sanitizeFileName(long)) != maxSendFileNameRunes {
		t.Fatalf("truncate runes")
	}
}

func utf8Len(s string) int {
	return len([]rune(s))
}

func TestEnforcePathUnderRoot(t *testing.T) {
	root := t.TempDir()
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(rootAbs, "ok", "f.txt")
	if err := os.MkdirAll(filepath.Dir(sub), 0755); err != nil {
		t.Fatal(err)
	}
	if err := enforcePathUnderRoot(sub, rootAbs); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(rootAbs, "..", "outside")
	if err := enforcePathUnderRoot(outside, rootAbs); err == nil {
		t.Fatal("expected error for path outside root")
	}
}

func TestResolveSendFilePath_relativeOk(t *testing.T) {
	root := t.TempDir()
	rel := filepath.Join("dir", "x.dat")
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	abs, err := resolveSendFilePath(rel, root, "")
	if err != nil {
		t.Fatal(err)
	}
	if abs != full {
		t.Fatalf("abs=%q want %q", abs, full)
	}
}

func TestResolveSendFilePath_escapeRejected(t *testing.T) {
	root := t.TempDir()
	_, err := resolveSendFilePath(filepath.Join("..", "..", "etc", "passwd"), root, "")
	if err == nil {
		t.Fatal("expected escape error")
	}
}

func TestExecSendFile_noActiveJob(t *testing.T) {
	p := NewFeishuPlugin()
	_, err := p.execSendFile(context.Background(), `{"path":"foo"}`)
	if err == nil || !strings.Contains(err.Error(), "Feishu-triggered") {
		t.Fatalf("got err=%v", err)
	}
}

func TestExecSendFile_pathRequired(t *testing.T) {
	p := NewFeishuPlugin()
	p.setActiveJob(&inboundJob{ChatID: "c"})
	defer p.clearActiveJob()

	_, err := p.execSendFile(context.Background(), `{}`)
	if err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Fatalf("got %v", err)
	}
}

func TestExecSendFile_contentBase64Rejected(t *testing.T) {
	p := NewFeishuPlugin()
	p.setActiveJob(&inboundJob{ChatID: "c"})
	defer p.clearActiveJob()

	_, err := p.execSendFile(context.Background(), `{"content_base64":"Zg==","filename":"x.txt"}`)
	if err == nil || !strings.Contains(err.Error(), "content_base64") {
		t.Fatalf("got %v", err)
	}
}

func TestExecSendFile_fileTooLarge(t *testing.T) {
	p := NewFeishuPlugin()
	p.cfg.SendFileMaxBytes = 10
	root := t.TempDir()
	p.cfg.SendFileRoot = root
	p.setActiveJob(&inboundJob{ChatID: "c"})
	defer p.clearActiveJob()

	path := filepath.Join(root, "big.bin")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 20)), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := p.execSendFile(context.Background(), `{"path":"big.bin"}`)
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("got %v", err)
	}
}
