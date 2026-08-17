package analyze

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Allan-Nava/segcheck/internal/fetch"
	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// SC-22: checking a protected stream.
//
// Every check in this tool reads the media, so on an AES-128 stream every one of them
// is blind and the honest report is "segcheck could not look". Given the key it can
// look — and the point of the feature is that the *content* checks then run, not that
// decryption happened.

var testKey = bytes.Repeat([]byte{0x2A}, 16)

// With the key, a protected stream is checked like any other: the resolution comes
// out of the bitstream, the timeline out of the timestamps.
func TestRun_EncryptedStreamWithTheKey(t *testing.T) {
	srv := newEncryptedOrigin(t, encryptedSpec{})
	res := runWithKey(t, srv+"/index.m3u8", testKey, false)

	for _, f := range res.Findings {
		if f.Status != finding.OK {
			t.Errorf("a decrypted stream produced %s on %s/%s: %s", f.Status, f.Check, f.Target, f.Message)
		}
	}
	// The checks that only a decrypted segment can answer must actually have run.
	// (Not `resolution`: a bare media playlist declares none to compare against.)
	for _, check := range []string{"container", "continuity", "keyframe", "framerate"} {
		if !hasCheck(res, check) {
			t.Errorf("no %q finding: the content checks did not run on the decrypted media.\n%s", check, dump(res))
		}
	}
	f, ok := findFinding(res, "encryption", finding.OK)
	if !ok {
		t.Fatalf("no encryption finding.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "decrypted") {
		t.Errorf("the finding does not say the segments were decrypted: %q", f.Message)
	}
}

// Without a key nothing changes from before: the segments are opaque, and that is a
// limit of this tool rather than a defect in the stream.
func TestRun_EncryptedStreamWithoutAKey(t *testing.T) {
	srv := newEncryptedOrigin(t, encryptedSpec{})
	res := runWithKey(t, srv+"/index.m3u8", nil, false)

	for _, f := range res.Findings {
		if finding.AtLeast(f.Status, finding.BAD) {
			t.Errorf("an encrypted stream with no key produced %s on %s: %s", f.Status, f.Check, f.Message)
		}
	}
	f, ok := findFinding(res, "container", finding.OK)
	if !ok {
		t.Fatalf("no container finding.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "need the key") {
		t.Errorf("the finding does not say why nothing could be read: %q", f.Message)
	}
}

// The wrong key is a fact about the key, not about the stream. Reporting it as an
// unparseable segment would send an operator hunting a defect in healthy media.
func TestRun_EncryptedStreamWithTheWrongKey(t *testing.T) {
	srv := newEncryptedOrigin(t, encryptedSpec{})
	res := runWithKey(t, srv+"/index.m3u8", bytes.Repeat([]byte{0x2B}, 16), false)

	f, ok := findFinding(res, "encryption", finding.ERROR)
	if !ok {
		t.Fatalf("a wrong key was not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "key") {
		t.Errorf("the finding does not blame the key: %q", f.Message)
	}
	if _, ok := findFinding(res, "container", finding.BAD); ok {
		t.Errorf("a wrong key was reported as unparseable media.\n%s", dump(res))
	}
}

// EXT-X-KEY need not state an IV. When it does not, the IV is the segment's media
// sequence number — and this stream's sequence starts at 7, so a decrypter that
// defaulted to zeroes gets noise on every segment.
func TestRun_EncryptedStreamWithSequenceIV(t *testing.T) {
	srv := newEncryptedOrigin(t, encryptedSpec{noIV: true, firstSequence: 7})
	res := runWithKey(t, srv+"/index.m3u8", testKey, false)

	if _, ok := findFinding(res, "encryption", finding.ERROR); ok {
		t.Errorf("the sequence-number IV was not used.\n%s", dump(res))
	}
	if !hasCheck(res, "keyframe") {
		t.Errorf("the content checks did not run.\n%s", dump(res))
	}
}

// The key URI is in the manifest, so segcheck can fetch it — but only when asked.
// Pointing a checker at a key server is a request to a system that logs, rate-limits
// and sometimes bills, and a manifest mentioning a URL is not a reason to do it.
func TestRun_KeyFetchingIsOptIn(t *testing.T) {
	srv := newEncryptedOrigin(t, encryptedSpec{})

	// Off by default: the segments stay opaque even though the key is right there.
	res := runWithKey(t, srv+"/index.m3u8", nil, false)
	if hasCheck(res, "keyframe") {
		t.Errorf("the key was fetched without being asked for.\n%s", dump(res))
	}

	// Asked for: fetched, and the content checks run.
	res = runWithKey(t, srv+"/index.m3u8", nil, true)
	if !hasCheck(res, "keyframe") {
		t.Errorf("the key was not fetched when asked for.\n%s", dump(res))
	}
	if _, ok := findFinding(res, "encryption", finding.OK); !ok {
		t.Errorf("no OK encryption finding after fetching the key.\n%s", dump(res))
	}
}

// A key server that will not serve the key, or serves something that is not one:
// the segments stay opaque and nothing is blamed on the stream.
func TestRun_KeyFetchFailureLeavesSegmentsOpaque(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec encryptedSpec
	}{
		{"the key 404s", encryptedSpec{keyStatus: http.StatusNotFound}},
		{"the key is not sixteen bytes", encryptedSpec{keyBody: []byte("nope")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := runWithKey(t, newEncryptedOrigin(t, tc.spec)+"/index.m3u8", nil, true)
			for _, f := range res.Findings {
				if finding.AtLeast(f.Status, finding.BAD) {
					t.Errorf("%s produced %s on %s: %s", tc.name, f.Status, f.Check, f.Message)
				}
			}
		})
	}
}

// ---------- harness ----------

type encryptedSpec struct {
	// noIV omits the IV attribute, so the sequence number is the IV.
	noIV bool
	// firstSequence is EXT-X-MEDIA-SEQUENCE, which the IV then derives from.
	firstSequence int
	// keyStatus, when non-zero, is served instead of the key.
	keyStatus int
	// keyBody, when non-nil, is served instead of the sixteen-byte key.
	keyBody []byte
	// clearFrom, when non-zero, serves that 1-based segment and the ones after it
	// unencrypted, with EXT-X-KEY:METHOD=NONE in front of them.
	clearFrom int
	// noKeyURI writes an EXT-X-KEY with a method but no URI, so there is no key to
	// fetch even when fetching is asked for.
	noKeyURI bool
}

// newEncryptedOrigin serves a media playlist of AES-128 segments whose plaintext is
// known by construction, which is the only way to tell a decrypter that works from
// one that produces plausible noise.
func newEncryptedOrigin(t *testing.T, spec encryptedSpec) string {
	t.Helper()
	const segs = 4
	iv := bytes.Repeat([]byte{0x11}, 16)

	mux := http.NewServeMux()
	mux.HandleFunc("/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:%d\n", spec.firstSequence)
		switch {
		case spec.noKeyURI:
			b.WriteString("#EXT-X-KEY:METHOD=AES-128\n")
		case spec.noIV:
			b.WriteString("#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\n")
		default:
			fmt.Fprintf(&b, "#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\",IV=0x%x\n", iv)
		}
		for i := 0; i < segs; i++ {
			if spec.clearFrom > 0 && i == spec.clearFrom-1 {
				b.WriteString("#EXT-X-KEY:METHOD=NONE\n")
			}
			fmt.Fprintf(&b, "#EXTINF:%.3f,\nseg%d.ts\n", segSeconds, i)
		}
		b.WriteString("#EXT-X-ENDLIST\n")
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(b.String()))
	})
	mux.HandleFunc("/key.bin", func(w http.ResponseWriter, r *http.Request) {
		if spec.keyStatus != 0 {
			w.WriteHeader(spec.keyStatus)
			return
		}
		if spec.keyBody != nil {
			_, _ = w.Write(spec.keyBody)
			return
		}
		_, _ = w.Write(testKey)
	})
	for i := 0; i < segs; i++ {
		i := i
		mux.HandleFunc(fmt.Sprintf("/seg%d.ts", i), func(w http.ResponseWriter, r *http.Request) {
			plain := mediatest.TSWithSPS(int64(i)*segTicks, frameDur, segFrames, mediatest.SPSFor(1280, 720))
			w.Header().Set("Content-Type", "video/mp2t")
			if spec.clearFrom > 0 && i >= spec.clearFrom-1 {
				_, _ = w.Write(plain)
				return
			}
			segIV := iv
			if spec.noIV {
				segIV = mediaSequenceIV(spec.firstSequence + i)
			}
			_, _ = w.Write(mediatest.EncryptAES128(plain, testKey, segIV))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// mediaSequenceIV mirrors what the specification says the IV is when EXT-X-KEY
// states none. It is written out here rather than borrowed from the code under test:
// a fixture that derived its answer from the reader would agree with it however
// wrong the reader was.
func mediaSequenceIV(sequence int) []byte {
	iv := make([]byte, 16)
	n := uint64(sequence)
	for i := 0; i < 8; i++ {
		iv[15-i] = byte(n >> (8 * uint(i)))
	}
	return iv
}

func runWithKey(t *testing.T, url string, key []byte, fetchKeys bool) finding.Result {
	t.Helper()
	opts := Defaults()
	opts.Segments = 4
	opts.Concurrency = 4
	opts.Key = key
	opts.FetchKeys = fetchKeys
	opts.Now = func() time.Time { return time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC) }
	client := fetch.New(fetch.Options{Timeout: 5 * time.Second})
	return Run(context.Background(), client, url, opts)
}

// A stream where only some segments are encrypted, and the key decrypts those. It
// happens where a packager rotates protection on mid-rendition, and the count is what
// tells an operator which half they are looking at.
func TestRun_PartiallyEncryptedStream(t *testing.T) {
	srv := newEncryptedOrigin(t, encryptedSpec{clearFrom: 3})
	res := runWithKey(t, srv+"/index.m3u8", testKey, false)

	f, ok := findFinding(res, "encryption", finding.OK)
	if !ok {
		t.Fatalf("no encryption finding.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "2/4") {
		t.Errorf("the finding does not say how many decrypted: %q", f.Message)
	}
}

// An EXT-X-KEY with a method but no URI names no key to fetch. Nothing is requested,
// and the segments stay opaque.
func TestRun_EncryptedWithNoKeyURI(t *testing.T) {
	srv := newEncryptedOrigin(t, encryptedSpec{noKeyURI: true})
	res := runWithKey(t, srv+"/index.m3u8", nil, true)

	if hasCheck(res, "keyframe") {
		t.Errorf("something was decrypted with no key URI to fetch from.\n%s", dump(res))
	}
	for _, f := range res.Findings {
		if finding.AtLeast(f.Status, finding.BAD) {
			t.Errorf("produced %s on %s: %s", f.Status, f.Check, f.Message)
		}
	}
}
