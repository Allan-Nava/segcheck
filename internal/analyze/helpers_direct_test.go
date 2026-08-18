package analyze

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Allan-Nava/segcheck/internal/fetch"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// The decision helpers behind the checks added for the live edge, conformance,
// protection and codec strings. They are small enough to look obviously right,
// which is exactly why they are worth asserting directly: a synthetic origin can
// plant a defect in the media, but not a codec string in every grammar, nor a
// truncated box, nor a rendition list with nothing readable in it.

// ---------- codec strings ----------

// Each grammar is genuinely different, and guessing one for another yields
// plausible numbers rather than an error — which is why nothing here falls back.
func TestParseCodecString(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		codecs               string
		wantOK               bool
		family               string
		profile, level, tier int
		hasTier              bool
	}{
		{
			name: "avc1 in hex", codecs: "avc1.640028", wantOK: true,
			family: "H.264", profile: 0x64, level: 0x28,
		},
		{
			name: "avc3 in hex", codecs: "avc3.4d401f", wantOK: true,
			family: "H.264", profile: 0x4d, level: 0x1f,
		},
		{
			// The older dotted form is decimal. Reading it as hex would turn
			// level 30 into 48.
			name: "the older dotted decimal form", codecs: "avc1.66.30", wantOK: true,
			family: "H.264", profile: 66, level: 30,
		},
		{
			name: "hvc1 with a main-tier level", codecs: "hvc1.1.6.L93.B0", wantOK: true,
			family: "HEVC", profile: 1, level: 93, hasTier: true, tier: 0,
		},
		{
			name: "hvc1 with a high-tier level", codecs: "hev1.2.4.H153.B0", wantOK: true,
			family: "HEVC", profile: 2, level: 153, hasTier: true, tier: 1,
		},
		{
			// A general_profile_space prefix is a letter and not part of the number.
			name: "hvc1 with a profile space prefix", codecs: "hvc1.A1.6.L93", wantOK: true,
			family: "HEVC", profile: 1, level: 93, hasTier: true,
		},
		{
			name: "av01 main tier", codecs: "av01.0.13M.08", wantOK: true,
			family: "AV1", profile: 0, level: 13, hasTier: true, tier: 0,
		},
		{
			name: "av01 high tier", codecs: "av01.1.15H.10", wantOK: true,
			family: "AV1", profile: 1, level: 15, hasTier: true, tier: 1,
		},
		{
			name: "vp09", codecs: "vp09.02.41.08", wantOK: true,
			family: "VP9", profile: 2, level: 41,
		},
		{
			// The video component is found among the others.
			name: "an audio component first", codecs: "mp4a.40.2,avc1.640028", wantOK: true,
			family: "H.264", profile: 0x64, level: 0x28,
		},
		{name: "a bare four-character code", codecs: "avc1"},
		{name: "audio only", codecs: "mp4a.40.2"},
		{name: "empty", codecs: ""},
		{name: "a codec segcheck does not decompose", codecs: "dvh1.05.01"},
		{name: "avc1 with a non-hex six", codecs: "avc1.zzzzzz"},
		{name: "av01 with one component", codecs: "av01.0"},
		{name: "vp09 with one component", codecs: "vp09.02"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, family, ok := parseCodecString(tc.codecs)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if family != tc.family {
				t.Errorf("family = %q, want %q", family, tc.family)
			}
			if got.profile != tc.profile || got.level != tc.level {
				t.Errorf("profile/level = %d/%d, want %d/%d", got.profile, got.level, tc.profile, tc.level)
			}
			if got.hasTier != tc.hasTier || (tc.hasTier && got.tier != tc.tier) {
				t.Errorf("tier = %d present=%v, want %d present=%v", got.tier, got.hasTier, tc.tier, tc.hasTier)
			}
		})
	}
}

func TestAudioComponentOf(t *testing.T) {
	for _, tc := range []struct {
		name       string
		codecs     string
		wantOK     bool
		family     string
		objectType int
		hasAOT     bool
	}{
		{"AAC-LC", "mp4a.40.2", true, "aac", 2, true},
		{"HE-AAC", "mp4a.40.5", true, "aac", 5, true},
		{"HE-AAC v2", "mp4a.40.29", true, "aac", 29, true},
		{"a bare mp4a states no object type", "mp4a", true, "aac", 0, false},
		{"mp4a with a non-40 indication", "mp4a.6b", true, "aac", 0, false},
		{"AC-3", "ac-3", true, "ac3", 0, false},
		{"E-AC-3", "ec-3", true, "eac3", 0, false},
		{"Opus", "opus", true, "opus", 0, false},
		{"FLAC", "flac", true, "flac", 0, false},
		{"the audio component after the video one", "avc1.640028,mp4a.40.2", true, "aac", 2, true},
		{"video only", "avc1.640028", false, "", 0, false},
		{"empty", "", false, "", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := audioComponentOf(tc.codecs)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got.family != tc.family {
				t.Errorf("family = %q, want %q", got.family, tc.family)
			}
			if got.hasObjectType != tc.hasAOT || (tc.hasAOT && got.objectType != tc.objectType) {
				t.Errorf("object type = %d present=%v, want %d present=%v",
					got.objectType, got.hasObjectType, tc.objectType, tc.hasAOT)
			}
		})
	}
}

// "Object type 5" means nothing to most readers and "with SBR" means everything.
func TestSBRNote(t *testing.T) {
	for _, tc := range []struct {
		cfg  media.AudioConfig
		want string
	}{
		{media.AudioConfig{SBR: true, PS: true}, " (SBR and Parametric Stereo)"},
		{media.AudioConfig{SBR: true}, " (SBR)"},
		{media.AudioConfig{}, " (no SBR)"},
	} {
		if got := sbrNote(tc.cfg); got != tc.want {
			t.Errorf("sbrNote(%+v) = %q, want %q", tc.cfg, got, tc.want)
		}
	}
}

func TestTierNameAndIsHex(t *testing.T) {
	if tierName(1) != "high" || tierName(0) != "main" {
		t.Errorf("tierName = %q/%q, want high/main", tierName(1), tierName(0))
	}
	for _, tc := range []struct {
		s    string
		want bool
	}{
		{"640028", true}, {"ABCDEF", true}, {"abcdef", true},
		{"64002g", false}, {"", false}, {"64 028", false},
	} {
		if got := isHex(tc.s); got != tc.want {
			t.Errorf("isHex(%q) = %v, want %v", tc.s, got, tc.want)
		}
	}
}

// ---------- the Apple profile helpers ----------

// A rung is judged against the tier nearest its pixel count, so 1024x576 is
// measured against 960x540 rather than against nothing.
func TestNearestAppleTier(t *testing.T) {
	for _, tc := range []struct {
		name      string
		w, h      int
		wantWidth int
		wantOK    bool
	}{
		{"an exact tier", 1280, 720, 1280, true},
		{"between two tiers", 1024, 576, 960, true},
		{"above the table", 7680, 4320, 3840, true},
		{"below the table", 256, 144, 416, true},
		{"no resolution at all", 0, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := nearestAppleTier(tc.w, tc.h)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got.width != tc.wantWidth {
				t.Errorf("tier = %dx%d, want width %d", got.width, got.height, tc.wantWidth)
			}
		})
	}
}

func TestJoinAnd(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"one"}, "one"},
		{[]string{"one", "two"}, "one and two"},
		{[]string{"one", "two", "three"}, "one, two and three"},
	} {
		if got := joinAnd(tc.in); got != tc.want {
			t.Errorf("joinAnd(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The measurement helpers have to stay quiet on renditions with nothing readable
// in them, because every check above them treats a zero as "no claim".
func TestMeasurementHelpersOnAnEmptyRendition(t *testing.T) {
	rd := &renditionData{r: manifest.Rendition{Name: "720p", Kind: manifest.Video}}
	if _, _, _, ok := measuredBitrates(rd); ok {
		t.Error("measuredBitrates reported a bitrate for a rendition with no segments")
	}
	if got := measuredDurations(rd); len(got) != 0 {
		t.Errorf("measuredDurations = %v, want none", got)
	}
	if w, h := codedSize(rd); w != 0 || h != 0 {
		t.Errorf("codedSize = %dx%d, want 0x0", w, h)
	}
	if _, ok := videoCodecOf(rd); ok {
		t.Error("videoCodecOf named a codec for a rendition with no segments")
	}
	if _, ok := codecProfileOf(rd); ok {
		t.Error("codecProfileOf reported a profile for a rendition with no segments")
	}
	if _, ok := audioCodecOf(rd); ok {
		t.Error("audioCodecOf named a codec for a rendition with no segments")
	}
	if cfg := audioConfigOf(rd); cfg.Stated {
		t.Error("audioConfigOf reported a configuration for a rendition with no segments")
	}
	if _, ok := colourOf(rd); ok {
		t.Error("colourOf reported a colour for a rendition with no segments")
	}
	if _, ok := firstMediaStart(rd.segs); ok {
		t.Error("firstMediaStart reported a start for a rendition with no segments")
	}
	if _, ok := parsedSegment(rd, 0); ok {
		t.Error("parsedSegment found a segment in a rendition with none")
	}
	if _, _, _, _, _, _, ok := containerScheme(rd); ok {
		t.Error("containerScheme reported a scheme for a rendition with no segments")
	}
	if _, _, _, known := sampleEncryptionOf(rd); known {
		t.Error("sampleEncryptionOf reported a sample state for a rendition with no segments")
	}
	if _, ok := clearLeadSeconds(rd, 4); ok {
		t.Error("clearLeadSeconds turned a sample count into a length with no timescale")
	}
	if got := segmentSeconds(rd); got != 0 {
		t.Errorf("segmentSeconds = %v, want 0", got)
	}
	if claimsProtection(rd) {
		t.Error("claimsProtection said yes about an empty unprotected rendition")
	}
}

// claimsProtection has four independent reasons to say yes, and each is the only
// evidence on some real stream.
func TestClaimsProtection(t *testing.T) {
	for _, tc := range []struct {
		name string
		rd   *renditionData
		want bool
	}{
		{"nothing at all", &renditionData{}, false},
		{"the manifest states a method", &renditionData{r: manifest.Rendition{KeyMethod: "CENC"}}, true},
		{"the manifest states a scheme", &renditionData{r: manifest.Rendition{KeyScheme: "cbcs"}}, true},
		{"the manifest names a system", &renditionData{r: manifest.Rendition{DRMSystems: []string{"x"}}}, true},
		{
			"a segment names a key",
			&renditionData{segs: []segmentData{{seg: manifest.Segment{KeyMethod: "AES-128"}}}},
			true,
		},
		{
			"the media says so and the manifest does not",
			&renditionData{segs: []segmentData{{
				parsed: true,
				info:   media.SegmentInfo{Tracks: []media.Track{{Kind: media.Video, Encrypted: true}}},
			}}},
			true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := claimsProtection(tc.rd); got != tc.want {
				t.Errorf("claimsProtection = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------- the DRM set helpers ----------

func TestDRMSetHelpers(t *testing.T) {
	widevine := "edef8ba9-79d6-4ace-a3c8-27dcd51d21ed"
	if got := namesOf([]string{widevine, "11111111-2222-3333-4444-555555555555"}); len(got) != 2 ||
		got[0] != "widevine" || got[1] != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("namesOf = %v, want the name then the bare UUID", got)
	}
	if got := presentList(nil); got != "none" {
		t.Errorf("presentList(nil) = %q, want none", got)
	}
	if got := presentList([]media.DRMSystem{media.DRMSystemFor(widevine)}); got != "widevine" {
		t.Errorf("presentList = %q", got)
	}
	if got := difference([]string{"a", "b"}, []string{"b"}); len(got) != 1 || got[0] != "a" {
		t.Errorf("difference = %v, want [a]", got)
	}
	if got := lowerAll([]string{"ABC"}); got[0] != "abc" {
		t.Errorf("lowerAll = %v", got)
	}
	if got := systemIDs([]media.DRMSystem{{SystemID: widevine}}); len(got) != 1 || got[0] != widevine {
		t.Errorf("systemIDs = %v", got)
	}
}

// ---------- the availability helpers ----------

func TestAvailabilityHelpers(t *testing.T) {
	if got := absDuration(-3 * time.Second); got != 3*time.Second {
		t.Errorf("absDuration(-3s) = %v", got)
	}
	if got := absDuration(3 * time.Second); got != 3*time.Second {
		t.Errorf("absDuration(3s) = %v", got)
	}
	if got := signedSeconds(-1500 * time.Millisecond); got != "-1.5s" {
		t.Errorf("signedSeconds = %q, want -1.5s", got)
	}
	// The ellipsis is one rune of three bytes, so the length is counted in runes.
	if got := truncateForError(strings.Repeat("x", 50)); len([]rune(got)) != 41 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncateForError = %q, want forty characters and an ellipsis", got)
	}
	if got := truncateForError("short"); got != "short" {
		t.Errorf("truncateForError shortened a short string to %q", got)
	}
}

// A UTCTiming source answers in one of several ISO spellings, and one of them is
// not a time at all.
func TestParsePDTLike(t *testing.T) {
	for _, tc := range []struct {
		name   string
		in     string
		wantOK bool
	}{
		{"RFC3339 with a zone", "2026-08-10T12:00:00Z", true},
		{"with fractional seconds", "2026-08-10T12:00:00.123456Z", true},
		{"with an offset", "2026-08-10T12:00:00.5+02:00", true},
		{"with no zone at all", "2026-08-10T12:00:00", true},
		{"surrounded by whitespace", "  2026-08-10T12:00:00Z\n", true},
		{"not a time", "tomorrow", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parsePDTLike(tc.in)
			if (err == nil) != tc.wantOK {
				t.Errorf("parsePDTLike(%q) err = %v, want ok=%v", tc.in, err, tc.wantOK)
			}
		})
	}
}

// ---------- the watch helpers ----------

// The re-read interval comes from what the manifest states, and the bool is what
// keeps segcheck from judging a stream against its own default.
func TestPollInterval(t *testing.T) {
	for _, tc := range []struct {
		name     string
		pl       manifest.Playlist
		first    observation
		want     time.Duration
		wantSaid bool
	}{
		{
			name: "HLS TARGETDURATION",
			pl:   manifest.Playlist{TargetDuration: 6},
			want: 6 * time.Second, wantSaid: true,
		},
		{
			name: "a DASH minimumUpdatePeriod below it wins",
			pl:   manifest.Playlist{TargetDuration: 6, UpdatePeriod: 2},
			want: 2 * time.Second, wantSaid: true,
		},
		{
			name:  "a rendition's own target beats the playlist's",
			pl:    manifest.Playlist{TargetDuration: 2},
			first: observation{edges: []edgeState{{target: 4}}},
			want:  4 * time.Second, wantSaid: true,
		},
		{
			name:  "failing both, the declared segment durations",
			first: observation{edges: []edgeState{{count: 2, span: 8}}},
			want:  4 * time.Second, wantSaid: true,
		},
		{
			name: "a sub-second interval is floored",
			pl:   manifest.Playlist{UpdatePeriod: 0.2},
			want: minPollInterval, wantSaid: true,
		},
		{
			name: "nothing states anything",
			want: defaultPollInterval, wantSaid: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, said := pollInterval(tc.pl, tc.first)
			if got != tc.want || said != tc.wantSaid {
				t.Errorf("pollInterval = %v/%v, want %v/%v", got, said, tc.want, tc.wantSaid)
			}
		})
	}
}

func TestEdgeTarget(t *testing.T) {
	if got := edgeTarget([]edgePoint{{target: 2}, {target: 6}, {}}); got != 6 {
		t.Errorf("edgeTarget = %v, want the largest stated, 6", got)
	}
	if got := edgeTarget([]edgePoint{{}, {}}); got != 0 {
		t.Errorf("edgeTarget = %v, want 0 when nothing states one", got)
	}
}

// A manifest that stopped loading part-way through is a hole in the coverage
// rather than a verdict, and one that never loaded is not a verdict either.
func TestWatchFindings_ManifestThatWouldNotLoad(t *testing.T) {
	opts := Defaults()
	opts.Watch = 10 * time.Second

	allFailed := []observation{{err: errFake("dial tcp")}, {err: errFake("dial tcp")}}
	got := watchFindings("https://cdn.example/live.m3u8", allFailed, time.Second, true, opts)
	if len(got) != 1 || got[0].Status != finding.ERROR {
		t.Fatalf("every poll failing produced %v", got)
	}

	// Some failed: an ERROR for the hole, and the edge still judged on the rest.
	mixed := []observation{
		{at: time.Unix(0, 0), edges: []edgeState{{name: "720p", newest: "a", target: 2}}},
		{err: errFake("dial tcp")},
		{at: time.Unix(4, 0), edges: []edgeState{{name: "720p", newest: "b", target: 2}}},
	}
	got = watchFindings("https://cdn.example/live.m3u8", mixed, time.Second, true, opts)
	var sawError, sawEdge bool
	for _, f := range got {
		if f.Status == finding.ERROR {
			sawError = true
		}
		if f.Target == "720p" {
			sawEdge = true
		}
	}
	if !sawError || !sawEdge {
		t.Errorf("a partial failure produced %v, want a coverage ERROR and an edge verdict", got)
	}

	// Nothing failed and nothing had an edge: also not a verdict about the stream.
	empty := []observation{{at: time.Unix(0, 0)}, {at: time.Unix(2, 0)}}
	got = watchFindings("https://cdn.example/live.m3u8", empty, time.Second, true, opts)
	if len(got) != 1 || got[0].Status != finding.ERROR {
		t.Fatalf("no rendition with an edge produced %v", got)
	}
}

// One look settles nothing, and a rendition whose playlist never loaded is a
// hole rather than a stalled edge.
func TestEdgeFindings_TooLittleToSay(t *testing.T) {
	opts := Defaults()
	opts.Watch = 10 * time.Second

	got := edgeFindings("720p", []edgePoint{{at: time.Unix(0, 0), newest: "a"}}, time.Second, true, opts)
	if len(got) != 1 || got[0].Status != finding.OK || !strings.Contains(got[0].Message, "at least two") {
		t.Errorf("one observation produced %v", got)
	}

	got = edgeFindings("720p", []edgePoint{{err: errFake("404")}, {err: errFake("404")}}, time.Second, true, opts)
	if len(got) != 1 || got[0].Status != finding.ERROR {
		t.Errorf("a rendition that never loaded produced %v", got)
	}

	// A window in which the playlist carried no segments at all.
	empty := []edgePoint{{at: time.Unix(0, 0)}, {at: time.Unix(20, 0)}}
	got = edgeFindings("720p", empty, time.Second, true, opts)
	if len(got) != 1 || got[0].Status != finding.BAD || !strings.Contains(got[0].Message, "no segments") {
		t.Errorf("an empty window produced %v", got)
	}

	// An edge that advanced, with no interval stated to judge the gaps against.
	advanced := []edgePoint{
		{at: time.Unix(0, 0), newest: "a"},
		{at: time.Unix(2, 0), newest: "b"},
	}
	got = edgeFindings("720p", advanced, 2*time.Second, false, opts)
	if len(got) != 1 || got[0].Status != finding.OK || !strings.Contains(got[0].Message, "no re-read interval") {
		t.Errorf("an unjudgeable edge produced %v", got)
	}
}

// ---------- the DVR and profile helpers ----------

func TestCheckDVR_NotProbed(t *testing.T) {
	if got := checkDVR(nil); got != nil {
		t.Errorf("checkDVR(nil) = %v, want nothing", got)
	}
	if got := checkDVR(&dvrProbe{}); got != nil {
		t.Errorf("an unprobed window produced %v", got)
	}
	// A probe that neither fetched, failed nor parsed says nothing rather than
	// guessing which of the three it was.
	if got := checkDVR(&dvrProbe{probed: true, depth: 60}); got != nil {
		t.Errorf("a probe with no outcome produced %v", got)
	}
}

func TestProfileRules(t *testing.T) {
	rules, name, implemented := profileRules(ProfileApple)
	if !implemented || name != "apple" || len(rules) == 0 {
		t.Errorf("apple: %d rules, name %q, implemented %v", len(rules), name, implemented)
	}
	if _, _, implemented := profileRules(ProfileDASHIF); implemented {
		t.Error("the DASH-IF rule set reported itself as implemented")
	}
	if _, _, implemented := profileRules("netflix"); implemented {
		t.Error("an unknown profile reported itself as implemented")
	}
	for _, s := range []string{"", ProfileNone, ProfileApple, ProfileDASHIF} {
		if !ValidProfile(s) {
			t.Errorf("ValidProfile(%q) = false", s)
		}
	}
	if ValidProfile("netflix") {
		t.Error("ValidProfile accepted an unknown rule set")
	}
}

// ---------- small renderers ----------

func TestGapKindAndPartLabel(t *testing.T) {
	if got := gapKind(0.2); !strings.Contains(got, "gap") {
		t.Errorf("gapKind(+0.2) = %q, want a gap", got)
	}
	if got := gapKind(-0.2); !strings.Contains(got, "overlap") {
		t.Errorf("gapKind(-0.2) = %q, want an overlap", got)
	}
	if got := partLabel("720p", manifest.Part{Sequence: 12, Index: 3}); got != "720p seg 12 part 3" {
		t.Errorf("partLabel = %q", got)
	}
}

func TestPlaylistWindowAndHasParts(t *testing.T) {
	// A VOD playlist promises no window: every segment is permanent.
	vod := manifest.Playlist{Segments: []manifest.Segment{{Duration: 2}}}
	if oldest, span := playlistWindow(vod); oldest != nil || span != 0 {
		t.Errorf("a VOD playlist produced a window of %v", span)
	}
	live := manifest.Playlist{Live: true, Segments: []manifest.Segment{{URI: "a", Duration: 2}, {URI: "b", Duration: 4}}}
	oldest, span := playlistWindow(live)
	if oldest == nil || oldest.URI != "a" || span != 6 {
		t.Errorf("playlistWindow = %v/%v, want the first segment and 6s", oldest, span)
	}
	if oldest, _ := playlistWindow(manifest.Playlist{Live: true}); oldest != nil {
		t.Error("an empty live playlist produced an oldest segment")
	}

	// PART-TARGET alone is not parts: a playlist can state the interval and have
	// aged every part out of the window.
	if playlistHasParts(manifest.Playlist{PartTarget: 0.33}) {
		t.Error("a playlist with a PART-TARGET and no parts reported parts")
	}
	if !playlistHasParts(manifest.Playlist{PendingParts: []manifest.Part{{URI: "p"}}}) {
		t.Error("pending parts were not counted")
	}
	if !playlistHasParts(manifest.Playlist{Segments: []manifest.Segment{{Parts: []manifest.Part{{URI: "p"}}}}}) {
		t.Error("a segment's parts were not counted")
	}
}

// errFake is an error with a message and nothing else, for the branches that only
// quote one.
type errFake string

func (e errFake) Error() string { return string(e) }

// ---------- one guard each, for the branches nothing else reaches ----------

// Every Apple rule has to stay silent on a rendition it cannot measure, and each
// has its own copy of that decision.
func TestAppleRules_SilentOnWhatTheyCannotMeasure(t *testing.T) {
	empty := rend("720p")
	broken := rend("720p")
	broken.err = errFake("playlist 404")
	textRung := rend("subs")
	textRung.r.Kind = manifest.Text
	audioRung := rend("audio")
	audioRung.r.Kind = manifest.Audio

	for _, rule := range appleRules {
		for _, rd := range []*renditionData{empty, broken, textRung, audioRung} {
			ctx := profileContext{rends: []*renditionData{rd}, opts: Defaults()}
			for _, f := range rule.run(ctx) {
				if f.Status != finding.OK {
					t.Errorf("%s reported %s on a rendition it cannot measure: %s", rule.id, f.Status, f.Message)
				}
			}
		}
	}

	// And a rule set whose every rule stays silent still says the profile ran.
	got := checkProfile(manifest.Playlist{URL: "https://cdn.example/m.m3u8"},
		[]*renditionData{empty}, optsWithProfile(ProfileApple))
	if len(got) == 0 {
		t.Error("a profile whose rules all stayed silent said nothing at all")
	}
}

func optsWithProfile(p string) Options {
	o := Defaults()
	o.Profile = p
	return o
}

// A rendition whose segments are not adjacent in the playlist gives no comparable
// pair, and one with a single stamped segment gives nothing to compare at all.
func TestPDTFindings_NothingComparable(t *testing.T) {
	at := func(seq int, sec float64) pdtPoint {
		return pdtPoint{
			sd:    segmentData{seg: manifest.Segment{Sequence: seq, URI: "s.ts"}},
			at:    time.Unix(int64(sec), 0),
			start: sec,
		}
	}
	rd := rend("720p")
	if got := pdtFindings(rd, "720p", []pdtPoint{at(0, 0)}, 0.1); len(got) != 0 {
		t.Errorf("one stamped segment produced %v", got)
	}
	// Sequences 0 and 5: a fetch failure in between makes the pair meaningless.
	if got := pdtFindings(rd, "720p", []pdtPoint{at(0, 0), at(5, 10)}, 0.1); len(got) != 0 {
		t.Errorf("non-adjacent segments produced %v", got)
	}
}

// The ladder half skips renditions that are not video, and needs two rungs at one
// index before it can compare anything.
func TestPDTLadderFindings_SkipsAndOrders(t *testing.T) {
	audioRung := rend("audio")
	audioRung.r.Kind = manifest.Audio
	byRendition := map[string][]pdtPoint{
		"audio": {{sd: segmentData{seg: manifest.Segment{Sequence: 0}}, at: time.Unix(0, 0)}},
	}
	if got := pdtLadderFindings([]*renditionData{audioRung}, byRendition, []string{"audio"}, 0.1); got != nil {
		t.Errorf("an audio rendition was compared as a rung: %v", got)
	}

	// Two rungs, the second stamped earlier: the ordering picks the extremes.
	a, b := rend("720p"), rend("1080p")
	byRendition = map[string][]pdtPoint{
		"720p":  {{sd: segmentData{seg: manifest.Segment{Sequence: 0}}, at: time.Unix(10, 0), start: 0}},
		"1080p": {{sd: segmentData{seg: manifest.Segment{Sequence: 0}}, at: time.Unix(0, 0), start: 0}},
	}
	got := pdtLadderFindings([]*renditionData{a, b}, byRendition, []string{"720p", "1080p"}, 0.1)
	if len(got) != 1 || got[0].Status != finding.BAD {
		t.Fatalf("two rungs ten seconds apart produced %v", got)
	}
	if !strings.Contains(got[0].Message, "1080p") || !strings.Contains(got[0].Message, "720p") {
		t.Errorf("the finding does not name both rungs: %q", got[0].Message)
	}
}

// Parts of an encrypted segment are ciphertext, and a byte range of a CBC stream
// cannot be decrypted on its own: they are not selected at all.
func TestSelectParts_SkipsEncryptedSegments(t *testing.T) {
	rd := rend("720p")
	rd.segs = []segmentData{{seg: manifest.Segment{
		Sequence: 0, KeyMethod: "AES-128",
		Parts: []manifest.Part{{URI: "p0.m4s"}},
	}}}
	if got := selectParts(rd, Defaults()); len(got) != 0 {
		t.Errorf("parts of an AES-128 segment were selected: %v", got)
	}
}

// The parts timeline comparison needs both halves readable, and each missing half
// leaves it quiet rather than comparing against a zero.
func TestPartFindings_TimelessHalves(t *testing.T) {
	timeless := videoTrack()
	timeless.HasPTS = false
	noDur := videoTrack()
	noDur.FrameDur, noDur.MaxPTS = 0, 0

	rd := rend("720p")
	rd.hasParts = true
	// A part with no duration, beside a readable one.
	rd.parts = []partData{
		{part: manifest.Part{URI: "p0.m4s", Sequence: 0, Index: 0}, parsed: true,
			info: media.SegmentInfo{Container: media.ContainerMP4, Tracks: []media.Track{noDur}}},
		{part: manifest.Part{URI: "p1.m4s", Sequence: 0, Index: 1}, parsed: true,
			info: media.SegmentInfo{Container: media.ContainerMP4, Tracks: []media.Track{videoTrack()}}},
	}
	// The segment those parts belong to states no timeline.
	rd.segs = []segmentData{{
		seg: manifest.Segment{Sequence: 0}, parsed: true,
		info: media.SegmentInfo{Container: media.ContainerMP4, Tracks: []media.Track{timeless}},
	}}
	for _, f := range partFindings(rd, "720p", 0.1, Defaults()) {
		if f.Status == finding.BAD && strings.Contains(f.Message, "cover their segment") {
			t.Errorf("a segment stating no timeline was compared against: %s", f.Message)
		}
	}
}

// The clear-lead check has a branch for a lead it cannot measure and one for a
// lead that is exactly what was asked for.
func TestCheckClear_LeadBranches(t *testing.T) {
	// Clear samples, and no frame duration to turn the count into a length.
	timeless := videoTrack()
	timeless.FrameDur = 0
	timeless.ClearSamples, timeless.EncryptedSamples = 4, 6
	timeless.LeadingClearSamples, timeless.SampleStateKnown = 4, true
	rd := rend("720p", withSegs(okSeg(0, media.ContainerMP4, timeless)))
	rd.r.KeyScheme = "cenc"
	got := checkClear([]*renditionData{rd}, Defaults())
	if len(got) != 1 || !strings.Contains(got[0].Message, "no sample duration") {
		t.Errorf("a lead with no duration to convert by produced %v", got)
	}

	// A lead that matches what was asked for, to the frame.
	lead := videoTrack()
	lead.ClearSamples, lead.EncryptedSamples = 50, 50
	lead.LeadingClearSamples, lead.SampleStateKnown = 50, true
	rd = rend("720p", withSegs(okSeg(0, media.ContainerMP4, lead)))
	rd.r.KeyScheme = "cenc"
	opts := Defaults()
	opts.ClearLead = 2 * time.Second // 50 samples at 3600 ticks on a 90kHz clock
	got = checkClear([]*renditionData{rd}, opts)
	if len(got) != 1 || got[0].Status != finding.OK || !strings.Contains(got[0].Message, "as asked for") {
		t.Errorf("a lead of exactly the length asked for produced %v", got)
	}
}

// A cbcs video track stating no crypt pattern contradicts itself, and it is the
// half of the check that needs no manifest at all.
func TestCheckScheme_CbcsVideoWithNoPattern(t *testing.T) {
	tr := videoTrack()
	tr.Protection = "cbcs"
	rd := rend("720p", withSegs(okSeg(0, media.ContainerMP4, tr)))
	var said bool
	for _, f := range checkScheme([]*renditionData{rd}) {
		if strings.Contains(f.Message, "no crypt pattern") {
			said = true
			if f.Status != finding.BAD {
				t.Errorf("cbcs video with no pattern was reported %s", f.Status)
			}
		}
	}
	if !said {
		t.Error("a cbcs video track with no crypt pattern was not reported")
	}
}

// The availability diagnosis skips renditions it cannot judge.
func TestMissingEdgeFindings_Guards(t *testing.T) {
	broken := rend("720p")
	broken.err = errFake("404")
	if got := missingEdgeFindings([]*renditionData{broken, rend("1080p")}, Defaults()); len(got) != 0 {
		t.Errorf("renditions with nothing sampled produced %v", got)
	}
}

// Run fills in the two injected functions when a caller leaves them nil, which is
// every caller that does not need a fixed clock.
func TestRun_DefaultsTheInjectedFunctions(t *testing.T) {
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: syntheticBandwidth, width: 1280, height: 720, segments: cleanSegments(2, 1280, 720)},
	})
	opts := Defaults()
	opts.Now = nil
	opts.Sleep = nil
	opts.Segments = 2
	res := Run(context.Background(), fetch.New(fetch.Options{Timeout: 5 * time.Second}), srv.URL+"/master.m3u8", opts)
	if len(res.Findings) == 0 {
		t.Error("a run with no clock or sleep injected produced no findings")
	}
	if res.Started.IsZero() {
		t.Error("the run recorded no start time")
	}
}
