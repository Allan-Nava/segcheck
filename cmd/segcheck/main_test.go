package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// The CLI carries the invariant the README leads with — exit 0 whenever the
// check ran, non-zero only when --exit-on asks for it — and until this file
// existed nothing asserted it. Everything here goes through run(), which is
// main() with the process boundary taken out: argv in, writers in, exit code
// out.

const (
	frameDur  = int64(3600)   // 25fps on the 90kHz clock
	segFrames = 50            // 50 frames = exactly 2s
	segTicks  = int64(180000) // 2s in 90kHz ticks
	// The synthetic segments carry one small PES per frame, so a realistic
	// BANDWIDTH would be over-declared by 50x and the bitrate check would
	// rightly say so.
	syntheticBandwidth = 50_000
)

// origin serves a one-variant HLS stream of count 2s segments. When gapAt is
// positive, that segment and every later one start half a second late, with
// nothing in the manifest declaring it.
func origin(t *testing.T, count, gapAt int) string {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/master.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=1280x720,CODECS=\"avc1.4d401f\"\n720p/index.m3u8\n", syntheticBandwidth)
	})
	mux.HandleFunc("/720p/index.m3u8", func(w http.ResponseWriter, _ *http.Request) {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-VERSION:4\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n")
		for i := 0; i < count; i++ {
			fmt.Fprintf(&b, "#EXTINF:2.000,\nseg%d.ts\n", i)
		}
		b.WriteString("#EXT-X-ENDLIST\n")
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(b.String()))
	})
	for i := 0; i < count; i++ {
		i := i
		mux.HandleFunc(fmt.Sprintf("/720p/seg%d.ts", i), func(w http.ResponseWriter, _ *http.Request) {
			start := int64(i) * segTicks
			if gapAt > 0 && i >= gapAt {
				start += segTicks / 4
			}
			w.Header().Set("Content-Type", "video/mp2t")
			_, _ = w.Write(mediatest.TSWithSPS(start, frameDur, segFrames, mediatest.SPSFor(1280, 720)))
		})
	}
	return srv.URL + "/master.m3u8"
}

func exec(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr strings.Builder
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestRun_VersionAndHelp(t *testing.T) {
	for _, arg := range []string{"version", "--version", "-v"} {
		code, out, _ := exec(t, arg)
		if code != 0 {
			t.Errorf("%q exited %d, want 0", arg, code)
		}
		if !strings.Contains(out, "segcheck") {
			t.Errorf("%q printed %q, which does not name the tool", arg, out)
		}
	}
	for _, arg := range []string{"help", "--help", "-h"} {
		code, out, _ := exec(t, arg)
		if code != 0 {
			t.Errorf("%q exited %d, want 0", arg, code)
		}
		if !strings.Contains(out, "Usage:") {
			t.Errorf("%q did not print the usage", arg)
		}
	}
}

func TestRun_UsageErrorsExitTwo(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string // a fragment stderr must contain, so the message is useful
	}{
		{"no arguments", nil, "Usage:"},
		{"unknown command", []string{"lint"}, `"lint"`},
		{"check without a URL", []string{"check"}, "needs a manifest URL"},
		{"two URLs", []string{"check", "a", "b"}, "one manifest URL"},
		{"bad --from", []string{"check", "http://x/m.m3u8", "--from", "sideways"}, "--from"},
		{"bad --output", []string{"check", "http://x/m.m3u8", "--output", "yaml"}, "--output"},
		{"bad --exit-on", []string{"check", "http://x/m.m3u8", "--exit-on", "meh"}, "--exit-on"},
		{"negative --segments", []string{"check", "http://x/m.m3u8", "--segments", "-1"}, "--segments"},
		{"malformed --header", []string{"check", "http://x/m.m3u8", "--header", "no-colon"}, "header"},
		{"unknown flag", []string{"check", "http://x/m.m3u8", "--nope"}, "nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := exec(t, tc.args...)
			if code != 2 {
				t.Errorf("exited %d, want 2 (a usage error is not a finding)", code)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr does not mention %q, so the user cannot tell what is wrong:\n%s", tc.want, stderr)
			}
		})
	}
}

// The headline rule: a check that ran is a success, however bad the findings.
func TestRun_ExitsZeroOnAStreamFullOfDefects(t *testing.T) {
	url := origin(t, 4, 2) // an undeclared half-second gap from segment 2 on
	code, stdout, stderr := exec(t, "check", url, "--output", "json", "--no-color")
	if code != 0 {
		t.Fatalf("exited %d, want 0 — findings are output, not failure\nstderr: %s", code, stderr)
	}

	var res finding.Result
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("--output json is not parseable: %v\n%s", err, stdout)
	}
	if finding.Worst(res.Findings) != finding.BAD {
		t.Fatalf("worst finding is %s, want BAD — the planted gap was not reported", finding.Worst(res.Findings))
	}
}

func TestRun_ExitOnGatesOnTheWorstFinding(t *testing.T) {
	dirty := origin(t, 4, 2)
	clean := origin(t, 4, 0)

	if code, _, _ := exec(t, "check", dirty, "--exit-on", "bad", "--output", "json"); code != 1 {
		t.Errorf("--exit-on bad on a stream with a BAD finding exited %d, want 1", code)
	}
	if code, _, _ := exec(t, "check", clean, "--exit-on", "bad", "--output", "json"); code != 0 {
		t.Errorf("--exit-on bad on a clean stream exited %d, want 0", code)
	}
	// ERROR outranks BAD, so a BAD stream must not trip an ERROR threshold.
	if code, _, _ := exec(t, "check", dirty, "--exit-on", "error", "--output", "json"); code != 0 {
		t.Errorf("--exit-on error on a stream whose worst finding is BAD exited %d, want 0", code)
	}
	// The threshold is case-insensitive, as validate() accepts.
	if code, _, _ := exec(t, "check", dirty, "--exit-on", "BAD", "--output", "json"); code != 1 {
		t.Errorf("--exit-on BAD (upper case) exited %d, want 1", code)
	}
}

// `check <url> --flags` is the order everyone types, and Go's flag package stops
// at the first non-flag argument. If this regresses, every flag after the URL is
// silently ignored — the worst kind of bug, because the run still succeeds.
func TestRun_FlagsAfterTheURLAreHonoured(t *testing.T) {
	url := origin(t, 6, 0)

	_, before, _ := exec(t, "check", "--segments", "2", url, "--output", "json")
	_, after, _ := exec(t, "check", url, "--segments", "2", "--output", "json")

	var b, a finding.Result
	if err := json.Unmarshal([]byte(before), &b); err != nil {
		t.Fatalf("flags before the URL: %v", err)
	}
	if err := json.Unmarshal([]byte(after), &a); err != nil {
		t.Fatalf("flags after the URL: %v", err)
	}
	if a.Segments != 2 {
		t.Errorf("--segments after the URL sampled %d segments, want 2: the flag was ignored", a.Segments)
	}
	if a.Segments != b.Segments {
		t.Errorf("flag order changed the result: %d segments before the URL, %d after", b.Segments, a.Segments)
	}
}

func TestRun_OutputFormats(t *testing.T) {
	url := origin(t, 3, 0)

	_, jsonOut, _ := exec(t, "check", url, "--output", "json")
	if !strings.HasPrefix(strings.TrimSpace(jsonOut), "{") {
		t.Errorf("--output json did not produce an object: %.60q", jsonOut)
	}

	_, mdOut, _ := exec(t, "check", url, "--output", "markdown")
	if !strings.Contains(mdOut, "# segcheck") || !strings.Contains(mdOut, "| Status |") {
		t.Errorf("--output markdown is not the report shape:\n%.200s", mdOut)
	}

	_, textOut, _ := exec(t, "check", url, "--no-color")
	if strings.Contains(textOut, "\x1b[") {
		t.Errorf("--no-color still emitted ANSI escapes")
	}
	if !strings.Contains(textOut, "continuity") {
		t.Errorf("text output does not name the checks that ran:\n%.200s", textOut)
	}
}

// A rendered report is never coloured when it is going somewhere other than a
// terminal — JSON and markdown get piped into incident docs.
func TestUseColor_HonoursNoColorAndTheEnvironment(t *testing.T) {
	if useColor(true) {
		t.Error("--no-color did not disable colour")
	}
	t.Setenv("NO_COLOR", "1")
	if useColor(false) {
		t.Error("NO_COLOR is set and colour was still enabled")
	}
}
