package main

import (
	"os"
	"testing"
)

// --header is how credentials reach the origin, because a flag value would land
// in shell history and in CI logs. The parsing has to be exact: a header dropped
// silently turns an authorised fetch into a 403 that reads as a broken stream.
func TestHeaderFlag_Set(t *testing.T) {
	h := headerFlag{}

	if err := h.Set("Authorization: Bearer abc123"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := h["Authorization"]; got != "Bearer abc123" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer abc123")
	}

	// The name and value are both trimmed, so the usual copy-pasted spacing works.
	if err := h.Set("  X-Token  :   secret  "); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := h["X-Token"]; got != "secret" {
		t.Errorf("X-Token = %q, want %q", got, "secret")
	}

	// A value containing a colon — a URL, a time — keeps everything after the
	// first one.
	if err := h.Set("Referer: https://example.com:8443/player"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := h["Referer"]; got != "https://example.com:8443/player" {
		t.Errorf("Referer = %q", got)
	}

	// An empty value is legal and distinct from an absent header.
	if err := h.Set("X-Empty:"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, ok := h["X-Empty"]; !ok || got != "" {
		t.Errorf("X-Empty = %q, present %v; want an empty value", got, ok)
	}

	// Without a colon there is no header name, and guessing one would send a
	// malformed request.
	if err := h.Set("NotAHeader"); err == nil {
		t.Error("a value with no colon was accepted")
	}

	// String is only there to satisfy flag.Value; it is never shown.
	if got := h.String(); got != "" {
		t.Errorf("String() = %q, want empty", got)
	}
}

// Colour goes to a terminal and nowhere else: a report piped into a file or an
// incident document must not carry ANSI escapes, and NO_COLOR is honoured even on
// a terminal.
func TestUseColor(t *testing.T) {
	// Under `go test` stdout is a pipe, not a character device, so the answer is
	// false however the flags are set.
	t.Setenv("NO_COLOR", "")
	if useColor(false) {
		t.Error("useColor was true with stdout redirected to a pipe")
	}

	if useColor(true) {
		t.Error("useColor was true with --no-color")
	}

	t.Setenv("NO_COLOR", "1")
	if useColor(false) {
		t.Error("useColor was true with NO_COLOR set")
	}

	// The character-device test is what decides it, and os.Stdout under a pipe is
	// the case every CI run takes.
	fi, err := os.Stdout.Stat()
	if err != nil {
		t.Skipf("cannot stat stdout here: %v", err)
	}
	if fi.Mode()&os.ModeCharDevice != 0 {
		t.Skip("stdout is a terminal, so the redirected case cannot be asserted")
	}
}
