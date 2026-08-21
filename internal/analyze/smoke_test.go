//go:build smoke

// The real-stream smoke suite (SC-36).
//
// Every false positive this project has shipped was found by running against a
// reference stream, not by a unit test — five of them in the work on SC-16
// through SC-19 alone: a keyframe rule that flagged Apple's own bipbop, a NAL walk
// that stopped before the keyframe on 1080p, a DASH profile that states no
// SegmentBase at all, a hierarchical index whose top level references only other
// indexes, and `trex` defaults without which every duration read as zero. Unit
// tests asserted exactly what their author had thought of; the streams did not.
//
// It lives behind a build tag because it needs the open internet, and runs the
// built binary rather than calling the library, so what is under test is the
// thing that ships — including `--output json` and the exit-code contract.
//
//	go build -o /tmp/segcheck ./cmd/segcheck
//	SEGCHECK_BIN=/tmp/segcheck go test -tags smoke -run TestSmoke ./internal/analyze/ -v
//
// The assertion is *not* "nothing above OK", which is what SC-36 originally asked
// for. That rule does not survive contact with the streams: Apple's advanced
// example legitimately over-declares BANDWIDTH and ships an inverted ladder, and a
// correct segcheck reports both. So each stream carries a baseline of the checks
// allowed to exceed OK today, with the reason. Anything outside that baseline is
// a regression, which is the thing worth catching — and a baseline entry that
// stops firing is worth knowing about too.
package analyze

import (
	"encoding/json"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Allan-Nava/segcheck/internal/finding"
)

// A reference stream and what it is known to say.
type smokeStream struct {
	name string
	url  string
	// local, when set, serves the stream from mediatest over a loopback origin
	// and returns its manifest URL, instead of reaching the open internet. It is
	// the consolation prize for a feature no public stream declares: LL-HLS parts
	// (SC-99) and EXT-X-DISCONTINUITY (SC-103) were both searched for across
	// Apple's, mux's, Unified Streaming's, JW's and Akamai's vectors and every
	// FAST channel that answered, and not one of them carries either tag.
	//
	// A stream this repository builds itself cannot catch a shared misreading —
	// the writer and the reader agree by construction, which is the whole reason
	// the remote entries exist. What it can catch is the other half, and the half
	// that has actually gone wrong here before: a check that falls silent. A
	// parser that stops reading reports nothing, and nothing reads exactly like a
	// clean bill of health.
	local func(t *testing.T) string
	// allowed maps a check name to why that check legitimately exceeds OK on this
	// stream. A finding from any other check is a regression.
	allowed map[string]string
	// expect names checks that must produce a finding at all. Their silence is the
	// failure mode this suite exists for: a parser that stops reading reports
	// nothing, and nothing reads exactly like a clean bill of health.
	expect []string
}

var smokeStreams = []smokeStream{
	{
		name: "apple-fmp4",
		url:  "https://devstreaming-cdn.apple.com/videos/streaming/examples/adv_dv_atmos/main.m3u8",
		allowed: map[string]string{
			"cache":   "a fresh runner is a cold edge: nobody in its region had asked for these segments, so every one is a miss. On VOD that is where the request came from, not what the stream is",
			"bitrate": "the example over-declares BANDWIDTH by about 2x on several rungs",
			"ladder":  "the example ships rungs where more bandwidth buys fewer pixels",
		},
		expect: []string{"container", "resolution", "keyframe", "framerate", "continuity", "audio", "captions", "byterange"},
	},
	{
		name: "apple-mpeg-ts",
		url:  "https://devstreaming-cdn.apple.com/videos/streaming/examples/bipbop_16x9/bipbop_16x9_variant.m3u8",
		// Byte-range segments off one main.ts, which is what made the first
		// keyframe rule report this stream three times over.
		allowed: map[string]string{
			"cache": "a fresh runner is a cold edge: nobody in its region had asked for these segments, so every one is a miss. On VOD that is where the request came from, not what the stream is",
			// A real finding, checked by hand: the 1080p rung declares
			// avc1.4d401f — Main profile, level 3.1 — and its SPS codes level 4.0.
			// Level 3.1 tops out at 1280x720, so a device that honours the
			// manifest decides it cannot decode a rung it certainly could. The
			// asset is from 2016 and over-declares BANDWIDTH too.
			"codecstring": "the 1080p rung declares level 3.1 for level-4.0 media",
		},
		expect: []string{"container", "resolution", "keyframe", "framerate", "continuity", "audio", "captions", "subtitles", "codecstring", "byterange"},
	},
	{
		name: "dash-segment-template",
		url:  "https://dash.akamaized.net/akamai/bbb_30fps/bbb_30fps.mpd",
		allowed: map[string]string{
			"cache": "a fresh runner is a cold edge: nobody in its region had asked for these segments, so every one is a miss. On VOD that is where the request came from, not what the stream is",
		},
		expect: []string{"container", "resolution", "keyframe", "framerate", "continuity", "audio", "byterange"},
	},
	{
		name: "dash-single-file",
		url:  "https://dash.akamaized.net/dash264/TestCases/1a/sony/SNE_DASH_SD_CASE1A_REVISED.mpd",
		// The on-demand profile: no SegmentBase, a hierarchical index, and
		// fragments that state no durations at all. Until SC-19 and SC-87 this
		// stream produced three ERRORs and sampled nothing.
		allowed: map[string]string{
			"cache": "a fresh runner is a cold edge: nobody in its region had asked for these segments, so every one is a miss. On VOD that is where the request came from, not what the stream is",
		},
		expect: []string{"container", "resolution", "keyframe", "continuity", "byterange"},
	},
	{
		name: "dash-representation-attrs",
		url:  "https://dash.akamaized.net/dash264/TestCasesHD/2b/qualcomm/1/MultiResMPEG2.mpd",
		// The AdaptationSet states no mimeType, no contentType and no codecs and
		// leaves all of it to its Representations. Until SC-89 every one of its
		// four video rungs read as audio, so `ladder` reported "no video rendition
		// in the manifest" and `resolution`, `framerate` and `keyframe` went silent
		// on the whole stream. Those four checks in `expect` are the point of it.
		allowed: map[string]string{
			"cache":   "a fresh runner is a cold edge: nobody in its region had asked for these segments, so every one is a miss. On VOD that is where the request came from, not what the stream is",
			"bitrate": "a segment peak exceeds the declared BANDWIDTH by about 20%",
		},
		expect: []string{"container", "resolution", "keyframe", "framerate", "continuity", "audio", "ladder", "byterange"},
	},
	{
		name: "dash-multi-period",
		url:  "https://dash.akamaized.net/dash264/TestCases/5b/nomor/1.mpd",
		// Three Periods and not one @start between them: every Period after the
		// first derives its position from the one before it, and until SC-102
		// segcheck derived nothing and placed all three at 0.000s — the first
		// one's position, printed for media 250 and 360 seconds further on. It is
		// also the only multi-period stream in this suite, which is why the
		// arithmetic SC-40 added went five months without a real-stream reading.
		allowed: map[string]string{
			"cache":   "a fresh runner is a cold edge: nobody in its region had asked for these segments, so every one is a miss. On VOD that is where the request came from, not what the stream is",
			"bitrate": "several rungs peak 20-40% above the BANDWIDTH they declare",
		},
		expect: []string{"container", "resolution", "keyframe", "framerate", "continuity", "audio", "period", "byterange"},
	},
	{
		name: "dash-cenc",
		url:  "https://media.axprod.net/TestVectors/v7-MultiDRM-SingleKey/Manifest_1080p.mpd",
		// Partial encryption: the container parses and the samples do not, which is the
		// shape that makes the bitstream readers succeed and find nothing. Its clear
		// twin at v7-Clear produces the same findings minus the encryption notes, which
		// is what says the protected stream is checked as well as an unprotected one.
		allowed: map[string]string{
			"cache":   "a fresh runner is a cold edge: nobody in its region had asked for these segments, so every one is a miss. On VOD that is where the request came from, not what the stream is",
			"bitrate": "the vectors under-declare one segment's peak and over-declare the audio average",
		},
		expect: []string{"container", "resolution", "keyframe", "framerate", "continuity", "encryption", "byterange"},
	},
	{
		// SC-103. Nothing public declares EXT-X-DISCONTINUITY, so this one is
		// served from mediatest: four 2s segments with a real timeline reset
		// under the tag before the third, which is what a splice into other
		// content looks like. The check has to stay quiet — the tag is honoured
		// — and it has to speak, which is the half a loopback origin can prove.
		//
		// The reset sits at index 2 because runSmoke samples three segments and
		// this playlist is VOD, so segments 0, 1 and 2 are what gets fetched: the
		// tag and a segment on either side of it. Moving it later would fetch
		// none of it and the check would fall silent for a reason that has
		// nothing to do with the check.
		name: "local-discontinuity",
		local: func(t *testing.T) string {
			segs := discSegments(4, 2, 1280, 720)
			segs[2].discontinuity = true
			return newHLSOrigin(t, []variantSpec{{
				name: "720p", bandwidth: syntheticBandwidth,
				width: 1280, height: 720, segments: segs,
			}}).URL + "/master.m3u8"
		},
		allowed: map[string]string{},
		expect:  []string{"container", "resolution", "keyframe", "framerate", "continuity", "discontinuity", "byterange"},
	},
	{
		// SC-99. Same reason: no public LL-HLS endpoint was reachable when
		// `parts` was written, and none was reachable when this was. Two plain
		// segments and two the packager is still publishing parts for, with the
		// parts reconstructing the segment they make up — so `parts` must report
		// and must report nothing wrong.
		name: "local-ll-hls",
		local: func(t *testing.T) string {
			return newPartsOrigin(t, 2, []partedSeg{
				{parts: cleanParts()},
				{parts: cleanParts()},
			})
		},
		allowed: map[string]string{},
		// No `resolution` and no `ladder`: newPartsOrigin serves the media
		// playlist directly, and with no master there is no RESOLUTION attribute
		// for the media to be checked against. A check with nothing to compare
		// staying quiet is the contract, not a gap.
		expect: []string{"container", "keyframe", "continuity", "parts", "byterange"},
	},
}

type smokeResult struct {
	Findings []finding.Finding `json:"findings"`
	Segments int               `json:"segments"`
	Bytes    int64             `json:"bytes"`
}

func TestSmokeReferenceStreams(t *testing.T) {
	bin := os.Getenv("SEGCHECK_BIN")
	if bin == "" {
		t.Skip("set SEGCHECK_BIN to the built binary: go build -o /tmp/segcheck ./cmd/segcheck")
	}

	reached := 0
	for _, s := range smokeStreams {
		s := s
		t.Run(s.name, func(t *testing.T) {
			url := s.url
			if s.local != nil {
				url = s.local(t)
			}
			res, code, ok := runSmoke(t, bin, url)
			if !ok {
				t.Skipf("%s is not reachable from here", url)
			}
			// Only a remote stream counts. A loopback origin is always reachable,
			// so letting it satisfy the guard below would hide a total outage of
			// every real reference behind two streams this repository serves to
			// itself.
			if s.local == nil {
				reached++
			}

			// The contract, first: a check that ran is a success however bad the
			// findings, and only --exit-on may change that.
			if code != 0 {
				t.Errorf("exit code = %d, want 0 — a check that ran is a success", code)
			}
			if res.Segments == 0 {
				t.Fatalf("no segments sampled; the whole run looked at nothing:\n%s", smokeDump(res))
			}

			// Any check above OK that is not in the baseline is a regression.
			for _, f := range res.Findings {
				if !finding.AtLeast(f.Status, finding.WARN) {
					continue
				}
				why, allowed := s.allowed[f.Check]
				if !allowed {
					t.Errorf("%s %s on %s: %s\n  ↳ not in this stream's baseline, so it is a regression",
						f.Status, f.Check, f.Target, f.Message)
					continue
				}
				t.Logf("known: %s %s — %s (%s)", f.Status, f.Check, f.Message, why)
			}

			// A baseline entry that no longer fires is worth knowing about: either
			// the stream changed or a check went quiet.
			for check, why := range s.allowed {
				if !smokeHasAbove(res, check) {
					t.Logf("baseline entry %q no longer fires (%s) — worth removing", check, why)
				}
			}

			// And the checks that must have looked at something.
			for _, check := range s.expect {
				if !smokeHasCheck(res, check) {
					t.Errorf("the %s check produced nothing at all — silence reads exactly like a pass:\n%s",
						check, smokeDump(res))
				}
			}
		})
	}

	// A suite that skips every stream passes while asserting nothing, which is
	// worse than failing: it would hide a total outage behind a green run.
	if reached == 0 {
		t.Fatal("no remote reference stream was reachable; the suite asserted nothing a loopback origin could not")
	}
}

// runSmoke runs the binary and parses its JSON. The bool is false when the origin
// could not be reached at all, which is a fact about the network rather than
// about segcheck.
func runSmoke(t *testing.T, bin, url string) (smokeResult, int, bool) {
	t.Helper()

	cmd := exec.Command(bin, "check", url,
		"--renditions", "3", "--segments", "3", "--output", "json", "--timeout", "30s")
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb

	start := time.Now()
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Logf("running %s: %v", bin, err)
		return smokeResult{}, 0, false
	}
	t.Logf("%s in %s", url, time.Since(start).Round(time.Millisecond))

	var res smokeResult
	if jsonErr := json.Unmarshal([]byte(out.String()), &res); jsonErr != nil {
		t.Logf("output was not JSON (%v): %s", jsonErr, truncateForLog(out.String()+errb.String()))
		return smokeResult{}, code, false
	}

	// A manifest that could not be fetched is the network, not a finding.
	for _, f := range res.Findings {
		if f.Check == "manifest" && f.Status == finding.ERROR {
			t.Logf("origin unreachable: %s", f.Message)
			return res, code, false
		}
	}
	return res, code, true
}

func smokeHasCheck(res smokeResult, check string) bool {
	for _, f := range res.Findings {
		if f.Check == check {
			return true
		}
	}
	return false
}

func smokeHasAbove(res smokeResult, check string) bool {
	for _, f := range res.Findings {
		if f.Check == check && finding.AtLeast(f.Status, finding.WARN) {
			return true
		}
	}
	return false
}

func smokeDump(res smokeResult) string {
	var b strings.Builder
	counts := map[string]int{}
	for _, f := range res.Findings {
		counts[f.Check]++
	}
	names := make([]string, 0, len(counts))
	for k := range counts {
		names = append(names, k)
	}
	sort.Strings(names)
	b.WriteString("  checks that reported: ")
	b.WriteString(strings.Join(names, ", "))
	return b.String()
}

func truncateForLog(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
