package analyze

import (
	"strings"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// The encryption and ladder checks, and the guards every measuring check shares.
//
// The guards are the important half. Each one is the place where a check decides
// it cannot measure something and therefore says nothing — the `(value, false)`
// protocol. Removing any of them would not crash anything; it would produce a
// confident wrong number from a zero value, which is the one outcome this tool
// treats as worse than silence.

// ---------- encryption ----------

// Segments that are encrypted while the manifest declares no key cannot be played
// by anyone: there is nowhere for a player to get the key from. That is a BAD.
func TestCheckEncryption_EncryptedWithNoDeclaredKey(t *testing.T) {
	enc := videoTrack()
	enc.Encrypted = true
	rd := rend("720p", withSegs(
		okSeg(1, media.ContainerMP4, enc),
		okSeg(2, media.ContainerMP4, enc),
	))

	got := checkEncryption([]*renditionData{rd})
	f, ok := findIn(got, "encryption", finding.BAD)
	if !ok {
		t.Fatalf("no BAD for encrypted segments with no declared key:\n%s", dumpFindings(got))
	}
	if !strings.Contains(f.Message, "2/2") {
		t.Errorf("message = %q, want the affected count", f.Message)
	}
}

// Protection that starts part way through a rendition is how a clear lead-in is
// built, but it is also what a misconfigured packager produces. Naming it as a
// WARN lets the operator decide which it is.
func TestCheckEncryption_ProtectionChangesMidRendition(t *testing.T) {
	clear1 := okSeg(1, media.ContainerMP4, videoTrack())
	enc := videoTrack()
	enc.Encrypted = true
	prot := okSeg(2, media.ContainerMP4, enc)
	prot.seg.KeyMethod = "SAMPLE-AES"

	got := checkEncryption([]*renditionData{rend("720p", withSegs(clear1, prot))})
	f, ok := findIn(got, "encryption", finding.WARN)
	if !ok {
		t.Fatalf("no WARN when only some segments declare a key:\n%s", dumpFindings(got))
	}
	if !strings.Contains(f.Message, "1/2") {
		t.Errorf("message = %q", f.Message)
	}
}

// Every segment declaring a key is the healthy protected case, reported at OK so
// the run records that protection was checked rather than skipped.
func TestCheckEncryption_EverySegmentDeclaresAKey(t *testing.T) {
	enc := videoTrack()
	enc.Encrypted = true
	var segs []segmentData
	for i := 1; i <= 3; i++ {
		sd := okSeg(i, media.ContainerMP4, enc)
		sd.seg.KeyMethod = "SAMPLE-AES"
		segs = append(segs, sd)
	}
	got := checkEncryption([]*renditionData{rend("720p", withSegs(segs...))})
	// SAMPLE-AES also draws the partial-encryption note, so there are two OK findings
	// here and the declaration one has to be looked for rather than taken as the first.
	found := false
	for _, f := range got {
		if f.Check == "encryption" && f.Status == finding.OK && strings.Contains(f.Message, "all 3") {
			found = true
		}
	}
	if !found {
		t.Errorf("no OK finding saying every segment declares a key:\n%s", dumpFindings(got))
	}
}

// Nothing parsed means nothing to say about protection.
func TestCheckEncryption_NothingToLookAt(t *testing.T) {
	if got := checkEncryption([]*renditionData{rend("720p")}); len(got) != 0 {
		t.Errorf("checkEncryption spoke with no parsed segments:\n%s", dumpFindings(got))
	}
}

// ---------- tracks: the codec comparison ----------

// The manifest's CODECS is what a player uses to decide whether it can play a
// rendition before downloading anything. A rendition whose bitstream is a
// different codec will fail on devices that trusted the declaration.
func TestCheckTracks_CodecMismatchAgainstTheManifest(t *testing.T) {
	hevc := videoTrack()
	hevc.Codec = "hevc"
	rd := rend("720p", withSegs(okSeg(1, media.ContainerMP4, hevc)))
	rd.r.Codecs = "avc1.4d401f" // the manifest promised H.264
	rd.r.Width, rd.r.Height = 1280, 720

	got := checkTracks([]*renditionData{rd})
	f, ok := findIn(got, "tracks", finding.WARN)
	if !ok {
		t.Fatalf("no WARN for a codec mismatch:\n%s", dumpFindings(got))
	}
	if !strings.Contains(f.Message, "h264") || !strings.Contains(f.Message, "hevc") {
		t.Errorf("message = %q, want both the declared and the real codec", f.Message)
	}
}

// A codec the CODECS attribute does not describe cannot be compared, and the check
// has to stay silent rather than report a mismatch against a guess.
func TestCheckTracks_UnknownDeclaredCodecIsNotAMismatch(t *testing.T) {
	rd := rend("720p", withSegs(okSeg(1, media.ContainerMP4, videoTrack())))
	rd.r.Codecs = "dvh1.05.01" // Dolby Vision: not in the mapping table
	rd.r.Width, rd.r.Height = 1280, 720

	for _, f := range checkTracks([]*renditionData{rd}) {
		if strings.Contains(f.Message, "CODECS declares") {
			t.Errorf("an unmapped codec produced a mismatch finding: %s", f.Message)
		}
	}
}

// ---------- the measurement guards ----------

// unmeasurable builds a rendition whose segments parsed but state nothing that
// can be measured: no timescale, so no duration and no timeline.
func unmeasurable(name string) *renditionData {
	t := media.Track{ID: 1, Kind: media.Video, Codec: "h264"} // Timescale 0, no PTS
	rd := rend(name, withSegs(
		okSeg(1, media.ContainerTS, t),
		okSeg(2, media.ContainerTS, t),
		okSeg(3, media.ContainerTS, t),
	))
	rd.r.Width, rd.r.Height = 1280, 720
	rd.targetDuration = 4
	return rd
}

// With an unknown timescale nothing can be measured, and every check that
// measures has to stay quiet. A single BAD here would send an operator hunting a
// defect that is really segcheck failing to read a timescale.
func TestChecks_StaySilentWhenNothingCanBeMeasured(t *testing.T) {
	rends := []*renditionData{unmeasurable("720p"), unmeasurable("1080p")}
	opts := Defaults()

	for _, tc := range []struct {
		name string
		run  func() []finding.Finding
	}{
		{"continuity", func() []finding.Finding { return checkContinuity(rends, opts) }},
		{"duration", func() []finding.Finding { return checkDuration(rends, opts) }},
		{"timeline", func() []finding.Finding { return checkTimeline(rends, opts) }},
		{"bitrate", func() []finding.Finding { return checkBitrate(rends, opts) }},
		{"alignment", func() []finding.Finding { return checkAlignment(rends, opts) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, f := range tc.run() {
				if finding.AtLeast(f.Status, finding.WARN) {
					t.Errorf("%s reported %s on unmeasurable media: %s", tc.name, f.Status, f.Message)
				}
			}
		})
	}
}

// A video track with no coded resolution — an HEVC rung before the reader existed,
// or a fragment with no init segment — cannot be compared against RESOLUTION. The
// check skips it, and that silence is a known gap rather than a pass.
func TestCheckResolution_SkipsTracksWithNoCodedSize(t *testing.T) {
	noSize := media.Track{ID: 1, Kind: media.Video, Codec: "hevc", Timescale: 90000, HasPTS: true}
	rd := rend("720p", withSegs(okSeg(1, media.ContainerTS, noSize)))
	rd.r.Width, rd.r.Height = 1280, 720

	for _, f := range checkResolution([]*renditionData{rd}) {
		if finding.AtLeast(f.Status, finding.WARN) {
			t.Errorf("a track with no coded resolution produced %s: %s", f.Status, f.Message)
		}
	}
}

// A segment whose declared duration is zero or missing gives nothing to compare
// the measured duration against.
func TestCheckDuration_SkipsSegmentsWithNoDeclaredDuration(t *testing.T) {
	sd := okSeg(1, media.ContainerTS, videoTrack())
	sd.seg.Duration = 0
	rd := rend("720p", withSegs(sd))
	rd.targetDuration = 4

	for _, f := range checkDuration([]*renditionData{rd}, Defaults()) {
		if finding.AtLeast(f.Status, finding.WARN) {
			t.Errorf("a segment with no declared duration produced %s: %s", f.Status, f.Message)
		}
	}
}

// Two consecutive segments whose timescales differ cannot be compared: the tick
// counts mean different things, and subtracting them would report a gap of
// whatever the ratio happens to be.
func TestCheckContinuity_SkipsSegmentsWithDifferentTimescales(t *testing.T) {
	a := videoTrack()
	b := videoTrack()
	b.Timescale = 48000 // a different clock

	rd := rend("720p", withSegs(
		okSeg(1, media.ContainerTS, a),
		okSeg(2, media.ContainerTS, b),
	))
	for _, f := range checkContinuity([]*renditionData{rd}, Defaults()) {
		if finding.AtLeast(f.Status, finding.WARN) {
			t.Errorf("mismatched timescales produced %s: %s", f.Status, f.Message)
		}
	}
}

// A segment whose duration cannot be measured contributes nothing to the total,
// and with no measurable duration anywhere there is no average bitrate to compare.
// A single-sample segment is the real case: one timestamp is no span, and there is
// no stated duration to fall back on.
func TestCheckBitrate_SkipsWhenNoDurationCanBeMeasured(t *testing.T) {
	oneSample := media.Track{
		ID: 1, Kind: media.Video, Codec: "h264", Width: 1280, Height: 720,
		Timescale: 90000, HasPTS: true, MinPTS: 90000, MaxPTS: 90000, Samples: 1,
	}
	rd := rend("720p", withSegs(
		okSeg(1, media.ContainerTS, oneSample),
		okSeg(2, media.ContainerTS, oneSample),
	))
	rd.r.Bandwidth = 800000

	got := checkBitrate([]*renditionData{rd}, Defaults())
	if len(got) != 0 {
		t.Errorf("checkBitrate reported on a rendition whose duration is unmeasurable:\n%s", dumpFindings(got))
	}
}

// ---------- ladder ----------

// A manifest with no video rendition is not a ladder at all. This is the one
// ladder finding that is a BAD, because there is nothing to adapt between.
func TestCheckLadder_NoVideoRendition(t *testing.T) {
	pl := manifest.Playlist{
		URL: "https://cdn.example.com/hls/master.m3u8", Master: true,
		Renditions: []manifest.Rendition{
			{Name: "audio-en", Kind: manifest.Audio, Bandwidth: 128000},
		},
	}
	got := checkLadder(pl)
	f, ok := findIn(got, "ladder", finding.BAD)
	if !ok {
		t.Fatalf("no BAD for a manifest with no video:\n%s", dumpFindings(got))
	}
	if !strings.Contains(f.Message, "no video rendition") {
		t.Errorf("message = %q", f.Message)
	}
}

// Two rungs with the same resolution and bitrate add no adaptivity — the player
// gains nothing from having both, and the duplicate is usually a packaging
// mistake.
func TestCheckLadder_DuplicateRungs(t *testing.T) {
	pl := manifest.Playlist{
		URL: "https://cdn.example.com/hls/master.m3u8", Master: true,
		Renditions: []manifest.Rendition{
			{Name: "720p", Kind: manifest.Video, Width: 1280, Height: 720, Bandwidth: 2000000},
			{Name: "720p-dup", Kind: manifest.Video, Width: 1280, Height: 720, Bandwidth: 2000000},
			{Name: "360p", Kind: manifest.Video, Width: 640, Height: 360, Bandwidth: 800000},
		},
	}
	got := checkLadder(pl)
	var found bool
	for _, f := range got {
		if f.Status == finding.WARN && strings.Contains(f.Message, "share resolution and bitrate") {
			found = true
			if !strings.Contains(f.Target, "720p") {
				t.Errorf("target = %q, want it to name the duplicate rungs", f.Target)
			}
		}
	}
	if !found {
		t.Fatalf("no WARN for duplicate rungs:\n%s", dumpFindings(got))
	}
}

// A rung that costs more bandwidth for fewer pixels is an inverted ladder: the
// player will sometimes pick the more expensive, worse-looking rung.
func TestCheckLadder_InvertedRung(t *testing.T) {
	pl := manifest.Playlist{
		URL: "https://cdn.example.com/hls/master.m3u8", Master: true,
		Renditions: []manifest.Rendition{
			{Name: "360p", Kind: manifest.Video, Width: 640, Height: 360, Bandwidth: 800000},
			// More bandwidth than the 720p rung, but a lower resolution.
			{Name: "480p", Kind: manifest.Video, Width: 854, Height: 480, Bandwidth: 3000000},
			{Name: "720p", Kind: manifest.Video, Width: 1280, Height: 720, Bandwidth: 2000000},
		},
	}
	got := checkLadder(pl)
	var found bool
	for _, f := range got {
		if f.Status == finding.WARN && strings.Contains(f.Message, "lower resolution") {
			found = true
			if !strings.Contains(f.Message, "480p") {
				t.Errorf("message = %q, want it to name the inverted rung", f.Message)
			}
		}
	}
	if !found {
		t.Fatalf("no WARN for an inverted ladder:\n%s", dumpFindings(got))
	}
}

// Renditions with no resolution at all must not be reported as duplicates of each
// other: "0x0" is not a shared resolution, it is two unknowns.
func TestCheckLadder_UnknownResolutionsAreNotDuplicates(t *testing.T) {
	pl := manifest.Playlist{
		URL: "https://cdn.example.com/hls/master.m3u8", Master: true,
		Renditions: []manifest.Rendition{
			{Name: "a", Kind: manifest.Video, Bandwidth: 800000},
			{Name: "b", Kind: manifest.Video, Bandwidth: 800000},
		},
	}
	for _, f := range checkLadder(pl) {
		if strings.Contains(f.Message, "share resolution and bitrate") {
			t.Errorf("two renditions with no resolution were called duplicates: %s", f.Message)
		}
	}
}
