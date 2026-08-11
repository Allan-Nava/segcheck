package media

import "testing"

// The accessors on SegmentInfo are small, and they decide which track every
// cross-segment assertion is made against. Timeline in particular is the
// promise that a continuity or drift check never compares a video start against
// an audio start — a mistake that reports a phantom gap on a perfectly healthy
// stream — so every fallback step it takes is pinned here rather than left to
// whichever parser test happens to exercise it.

func TestSegmentInfo_TimelinePrefersVideoOverAudio(t *testing.T) {
	s := SegmentInfo{Tracks: []Track{
		{ID: 2, Kind: Audio, HasPTS: true, MinPTS: 5000},
		{ID: 1, Kind: Video, HasPTS: true, MinPTS: 900},
	}}
	tr, ok := s.Timeline()
	if !ok {
		t.Fatal("no timeline track in a segment with both video and audio")
	}
	// Declared second, and still the one that must win.
	if tr.Kind != Video {
		t.Errorf("timeline kind = %s, want video", tr.Kind)
	}
	if tr.MinPTS != 900 {
		t.Errorf("timeline MinPTS = %d, want 900 (the video track's)", tr.MinPTS)
	}
}

// A video track with no timestamps cannot define a timeline, so audio has to
// take over. Returning the video track here would hand every timing check a
// MinPTS of zero and make the segment look like it started at the origin.
func TestSegmentInfo_TimelineFallsBackToAudioWhenVideoHasNoPTS(t *testing.T) {
	s := SegmentInfo{Tracks: []Track{
		{ID: 1, Kind: Video, HasPTS: false},
		{ID: 2, Kind: Audio, HasPTS: true, MinPTS: 5000},
	}}
	tr, ok := s.Timeline()
	if !ok {
		t.Fatal("no timeline track when only the audio track has timestamps")
	}
	if tr.Kind != Audio || tr.MinPTS != 5000 {
		t.Errorf("timeline = %s MinPTS %d, want audio MinPTS 5000", tr.Kind, tr.MinPTS)
	}
}

func TestSegmentInfo_TimelineFallsBackToAnyTrackWithTimestamps(t *testing.T) {
	s := SegmentInfo{Tracks: []Track{
		{ID: 1, Kind: Video, HasPTS: false},
		{ID: 3, Kind: Other, HasPTS: true, MinPTS: 77},
	}}
	tr, ok := s.Timeline()
	if !ok {
		t.Fatal("no timeline track when only an 'other' track has timestamps")
	}
	if tr.ID != 3 || tr.MinPTS != 77 {
		t.Errorf("timeline = track %d MinPTS %d, want track 3 MinPTS 77", tr.ID, tr.MinPTS)
	}
}

// No track with timestamps means no timeline, and the bool is the only correct
// way to say so: a zero-value Track would be read as a segment starting at 0.
func TestSegmentInfo_TimelineReportsWhenNothingHasTimestamps(t *testing.T) {
	for _, s := range []SegmentInfo{
		{},
		{Tracks: []Track{{Kind: Video}, {Kind: Audio}}},
	} {
		if _, ok := s.Timeline(); ok {
			t.Errorf("Timeline claimed a track for %d tracks with no timestamps", len(s.Tracks))
		}
	}
}

func TestSegmentInfo_TrackReturnsFirstOfKind(t *testing.T) {
	s := SegmentInfo{Tracks: []Track{
		{ID: 1, Kind: Audio, Codec: "aac"},
		{ID: 2, Kind: Audio, Codec: "ac3"},
	}}
	tr, ok := s.Track(Audio)
	if !ok {
		t.Fatal("no audio track")
	}
	if tr.ID != 1 {
		t.Errorf("Track(Audio) = %d, want the first one, 1", tr.ID)
	}
	if _, ok := s.Track(Video); ok {
		t.Error("Track(Video) found a video track in an audio-only segment")
	}
}

// One protected track is enough to make the segment encrypted: the encryption
// check reports that segcheck could not look inside, and it must not be fooled
// by a segment whose video is clear and whose audio is not.
func TestSegmentInfo_EncryptedIsTrueForAnyProtectedTrack(t *testing.T) {
	tests := []struct {
		name string
		in   SegmentInfo
		want bool
	}{
		{"nothing encrypted", SegmentInfo{Tracks: []Track{{Kind: Video}, {Kind: Audio}}}, false},
		{"audio only", SegmentInfo{Tracks: []Track{{Kind: Video}, {Kind: Audio, Encrypted: true}}}, true},
		{"video only", SegmentInfo{Tracks: []Track{{Kind: Video, Encrypted: true}, {Kind: Audio}}}, true},
		{"no tracks at all", SegmentInfo{}, false},
	}
	for _, tc := range tests {
		if got := tc.in.Encrypted(); got != tc.want {
			t.Errorf("%s: Encrypted() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Codecs is display order, video first, whatever order the container listed the
// tracks in. Tracks with no codec are left out rather than rendered as blanks.
func TestSegmentInfo_CodecsListsVideoFirstAndSkipsUnnamed(t *testing.T) {
	s := SegmentInfo{Tracks: []Track{
		{Kind: Audio, Codec: "aac"},
		{Kind: Other, Codec: "scte35"},
		{Kind: Video, Codec: "hevc"},
		{Kind: Other}, // no codec: nothing to show
	}}
	got := s.Codecs()
	want := []string{"hevc", "aac", "scte35"}
	if len(got) != len(want) {
		t.Fatalf("Codecs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Codecs() = %v, want %v", got, want)
		}
	}
	if s := (SegmentInfo{}).Codecs(); len(s) != 0 {
		t.Errorf("Codecs() on an empty segment = %v, want nothing", s)
	}
}

func TestTrack_Describe(t *testing.T) {
	tests := []struct {
		in   Track
		want string
	}{
		{Track{Codec: "h264", Kind: Video, Width: 1920, Height: 1080}, "h264 1920x1080"},
		{Track{Codec: "aac", Kind: Audio}, "aac"},
		// An HEVC rung in MPEG-TS before the parameter set is read: the codec is
		// known and the resolution is not, and a half-known size is not printed.
		{Track{Codec: "hevc", Kind: Video, Width: 1920}, "hevc"},
		// No codec at all falls back to the kind, so the output never says "".
		{Track{Kind: Video}, "video"},
	}
	for _, tc := range tests {
		if got := tc.in.Describe(); got != tc.want {
			t.Errorf("Describe() = %q, want %q", got, tc.want)
		}
	}
}

func TestTrackKind_String(t *testing.T) {
	for _, k := range []TrackKind{Video, Audio, Other} {
		if k.String() != string(k) {
			t.Errorf("%v.String() = %q", k, k.String())
		}
	}
}

// The two measurement contracts on Track, stated as tests because a wrong
// number here is worse than none: StartSec and DurationSec must return false
// rather than a zero value whenever the segment did not say enough.
func TestTrack_StartSecRequiresTimestampsAndATimescale(t *testing.T) {
	tests := []struct {
		name string
		in   Track
		want float64
		ok   bool
	}{
		{"good", Track{HasPTS: true, Timescale: 90000, MinPTS: 90000}, 1, true},
		{"no timestamps", Track{Timescale: 90000}, 0, false},
		{"unknown timescale", Track{HasPTS: true, MinPTS: 90000}, 0, false},
	}
	for _, tc := range tests {
		got, ok := tc.in.StartSec()
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: StartSec() = %v, %v; want %v, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestTrack_DurationSecPrefersTheStatedDuration(t *testing.T) {
	// The stated duration is 2s. The PTS span deliberately implies something
	// quite different — 1.04s even after the final frame is added — so that a
	// reader which ignored StatedDur could not accidentally land on the same
	// answer.
	tr := Track{
		Timescale: 90000, HasPTS: true, Samples: 50,
		MinPTS: 0, MaxPTS: 90000, FrameDur: 3600,
		StatedDur: 180000,
	}
	got, ok := tr.DurationSec()
	if !ok {
		t.Fatal("DurationSec reported nothing for a track that states its duration")
	}
	if got != 2 {
		t.Errorf("DurationSec() = %v, want 2 (the stated duration, not the 1.04s PTS span)", got)
	}
}

// Without a stated duration the answer is the PTS span plus one frame. Dropping
// the frame understates a 25fps segment by 40ms, which is enough on its own to
// trip a 1% drift check against a 4s target duration.
func TestTrack_DurationSecAddsTheFinalFrameToThePTSSpan(t *testing.T) {
	tr := Track{Timescale: 90000, HasPTS: true, Samples: 50, MinPTS: 0, MaxPTS: 176400, FrameDur: 3600}
	got, ok := tr.DurationSec()
	if !ok {
		t.Fatal("DurationSec reported nothing for a track with a full PTS span")
	}
	if got != 2 {
		t.Errorf("DurationSec() = %v, want 2 — the 1.96s span plus one 40ms frame", got)
	}
}

func TestTrack_DurationSecStaysSilentWhenItCannotMeasure(t *testing.T) {
	tests := []struct {
		name string
		in   Track
	}{
		{"unknown timescale", Track{HasPTS: true, Samples: 50, MaxPTS: 176400, FrameDur: 3600}},
		{"no timestamps", Track{Timescale: 90000, Samples: 50}},
		// One sample gives a zero-length span and no frame duration to add: a
		// single-frame segment is not a zero-duration segment.
		{"a single sample", Track{Timescale: 90000, HasPTS: true, Samples: 1}},
	}
	for _, tc := range tests {
		if got, ok := tc.in.DurationSec(); ok {
			t.Errorf("%s: DurationSec() = %v, true; want the unmeasurable answer", tc.name, got)
		}
	}
}
