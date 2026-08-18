package analyze

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/fetch"
	"github.com/Allan-Nava/segcheck/internal/manifest"
)

// rangeRecordingOrigin answers anything and records the Range header it was
// asked for.
func rangeRecordingOrigin(t *testing.T, into *string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*into = r.Header.Get("Range")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("not media"))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// The bisection's edge cases. Reaching them through Run needs a window shaped in
// ways no fixture here builds — a single probe point, a ladder whose sequence
// numbers do not advance, a byte-range window — and each of them is the
// difference between a number an operator can act on and one that is wrong.

func TestProbeSpan_TheNewestProbeIsOneSegmentDeep(t *testing.T) {
	probes := []manifest.Segment{
		{Sequence: 1, Duration: 2},
		{Sequence: 5, Duration: 2},
	}
	// Landing on the newest probe means the origin holds the live edge and
	// nothing measurably older: one segment, not zero, because that segment is
	// really there.
	if got := probeSpan(probes, 1); got != 2 {
		t.Errorf("probeSpan at the newest probe = %v, want 2 (the segment itself)", got)
	}
	// And landing on the oldest means the whole span between them.
	if got := probeSpan(probes, 0); got != 8 {
		t.Errorf("probeSpan across four segments of 2s = %v, want 8", got)
	}
}

// A window whose probe points do not advance in sequence — an HLS playlist
// listing the same media sequence twice, or a template segcheck read wrongly —
// must not measure a zero or negative gap and report the origin as holding
// nothing.
func TestProbeGap_NonAdvancingSequencesCountAsOneSegment(t *testing.T) {
	probes := []manifest.Segment{{Sequence: 9, Duration: 2}, {Sequence: 9, Duration: 2}}
	if got := probeGap(probes, 0); got != 2 {
		t.Errorf("probeGap over a sequence that did not advance = %v, want one segment", got)
	}
}

// A window addressed by byte range — an HLS playlist whose segments are ranges
// of one file — has to be probed with the range, or the request asks for the
// whole asset and the answer says nothing about whether that segment is there.
func TestRetentionHolds_AByteRangeProbeAsksForItsRange(t *testing.T) {
	var asked string
	srv := rangeRecordingOrigin(t, &asked)
	rd := &renditionData{r: manifest.Rendition{Name: "720p", Kind: manifest.Video}}
	seg := manifest.Segment{URI: srv + "/all.ts", ByteRange: &manifest.ByteRange{Length: 100, Offset: 200}}

	retentionHolds(context.Background(), fetch.New(fetch.Options{}), rd, nil, seg)

	if asked != "bytes=200-299" {
		t.Errorf("the probe asked for %q, not the segment's own range", asked)
	}
}

// Nothing to bisect over is not a measurement of zero: a rendition whose window
// segcheck could not enumerate has to leave the finding saying the window is
// short without inventing how short.
func TestMeasureRetention_NoProbesMeasuresNothing(t *testing.T) {
	p := &dvrProbe{depth: 60}
	measureRetention(context.Background(), fetch.New(fetch.Options{}), &renditionData{}, nil, p)
	if p.measured {
		t.Errorf("a rendition with no probe points was measured at %v", p.held)
	}
	if got := heldSuffix(p); got != "" {
		t.Errorf("heldSuffix invented a measurement: %q", got)
	}
	if v := heldValue(p); v == nil || *v != 60 {
		t.Errorf("heldValue = %v, want the claim that was disproved", v)
	}
}
