package main

import "testing"

func TestRestartPayloadFromFirstLine(t *testing.T) {
	p, ok := restartPayloadFromFirstLine(",dmr-restart", ",dmr-restart secret\nmore")
	if !ok || p != "secret" {
		t.Fatalf("got %q ok=%v", p, ok)
	}
	_, ok = restartPayloadFromFirstLine(",dmr-restart", "hello")
	if ok {
		t.Fatal("expected no match")
	}
}

func TestConstantTimeStringEqual(t *testing.T) {
	if !constantTimeStringEqual("a", "a") {
		t.Fatal()
	}
	if constantTimeStringEqual("a", "b") {
		t.Fatal()
	}
	if constantTimeStringEqual("a", "aa") {
		t.Fatal()
	}
}
