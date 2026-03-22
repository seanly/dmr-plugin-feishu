package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveExtraPromptPath(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "a", "b.txt")
	rel := "prompts/x.md"
	got := resolveExtraPromptPath(rel, dir)
	want := filepath.Clean(filepath.Join(dir, rel))
	if got != want {
		t.Fatalf("relative: got %q want %q", got, want)
	}
	abs := filepath.Join(dir, "abs.md")
	got2 := resolveExtraPromptPath(abs, "/other")
	if got2 != filepath.Clean(abs) {
		t.Fatalf("absolute: got %q", got2)
	}
	_ = sub
}

func TestBuildResolvedExtraPrompt_inlineOnly(t *testing.T) {
	s, err := buildResolvedExtraPrompt(FeishuConfig{ExtraPrompt: "  hello  "})
	if err != nil || s != "hello" {
		t.Fatalf("got %q err=%v", s, err)
	}
	s, err = buildResolvedExtraPrompt(FeishuConfig{})
	if err != nil || s != "" {
		t.Fatalf("empty: %q err=%v", s, err)
	}
}

func TestBuildResolvedExtraPrompt_fileOnly(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "p.md")
	if err := os.WriteFile(fp, []byte(" from file \n"), 0644); err != nil {
		t.Fatal(err)
	}
	s, err := buildResolvedExtraPrompt(FeishuConfig{
		ConfigBaseDir:   dir,
		ExtraPromptFile: "p.md",
	})
	if err != nil || s != "from file" {
		t.Fatalf("got %q err=%v", s, err)
	}
}

func TestBuildResolvedExtraPrompt_fileAndInline(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(fp, []byte("FILE"), 0644)
	s, err := buildResolvedExtraPrompt(FeishuConfig{
		ConfigBaseDir:   dir,
		ExtraPromptFile: "f.txt",
		ExtraPrompt:     "INLINE",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s != "FILE\n\nINLINE" {
		t.Fatalf("got %q", s)
	}
}

func TestBuildResolvedExtraPrompt_missingFile(t *testing.T) {
	_, err := buildResolvedExtraPrompt(FeishuConfig{
		ConfigBaseDir:   t.TempDir(),
		ExtraPromptFile: "nope.txt",
	})
	if err == nil || !strings.Contains(err.Error(), "extra_prompt_file") {
		t.Fatalf("got %v", err)
	}
}

func TestComposeRunPrompt(t *testing.T) {
	p := NewFeishuPlugin()
	p.extraRunPrompt = ""
	g := p.composeRunPrompt("hi")
	if !strings.Contains(g, "feishuSendText") || !strings.Contains(g, "feishuSendFile") || !strings.Contains(g, "User message:") || !strings.Contains(g, "hi") {
		t.Fatalf("builtin+user: %q", g)
	}
	p.extraRunPrompt = "RULES"
	g = p.composeRunPrompt("hi")
	if !strings.Contains(g, "RULES") || !strings.Contains(g, "feishuSendText") || !strings.Contains(g, "feishuSendFile") || !strings.Contains(g, "User message:") || !strings.Contains(g, "hi") {
		t.Fatalf("builtin+config+user: %q", g)
	}
	g = p.composeRunPrompt("")
	if !strings.Contains(g, "feishuSendText") || !strings.Contains(g, "feishuSendFile") || !strings.Contains(g, "RULES") {
		t.Fatalf("empty user with config extra: %q", g)
	}
	p.extraRunPrompt = ""
	g = p.composeRunPrompt("")
	if !strings.Contains(g, "feishuSendText") || !strings.Contains(g, "feishuSendFile") {
		t.Fatalf("empty user builtin only: %q", g)
	}
}
