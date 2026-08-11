package manifest

import (
	"net/url"
	"testing"
)

// The DASH helpers that decide what a representation is and where its segments
// live. They are pure functions over the parsed MPD, and two of them are the
// reason a DASH rendition can be read at all: firstTemplate resolves the
// three-level inheritance, and dashKind decides whether a representation is even
// looked at as video.

// SegmentTemplate is inherited Period -> AdaptationSet -> Representation, and a
// child that overrides only @media must keep the parent's @timescale. Losing it
// means every timestamp is in unknown units, which under this tool's rules makes
// the duration unmeasurable — the check goes quiet on a stream it could have
// verified.
// Every attribute is set on a level that is not the base, so each one has to be
// merged upwards for the result to be complete. The Period is the base copy and
// contributes only @media, which the Representation then overrides — so a merge
// branch that silently did nothing would show up as a missing value here.
func TestFirstTemplate_MergesInheritedAttributes(t *testing.T) {
	startNumber := 5
	timeline := &mpdSegTimeline{}

	period := &mpdSegTemplate{Media: "base/$Number$.m4s"}
	as := &mpdSegTemplate{
		Initialization:         "$RepresentationID$/init.mp4",
		Timescale:              90000,
		Duration:               360000,
		StartNumber:            &startNumber,
		PresentationTimeOffset: 1800,
		SegmentTimeline:        timeline,
	}
	// The representation overrides only @media, as real MPDs do.
	rep := &mpdSegTemplate{Media: "hi/$Number$.m4s"}

	got := firstTemplate(rep, as, period)
	if got == nil {
		t.Fatal("firstTemplate returned nothing for three levels, two of which are set")
	}
	if got.Media != "hi/$Number$.m4s" {
		t.Errorf("Media = %q, want the representation's override", got.Media)
	}
	if got.Timescale != 90000 {
		t.Errorf("Timescale = %d, want 90000 from the AdaptationSet — an unknown timescale makes every duration unmeasurable", got.Timescale)
	}
	if got.Initialization != "$RepresentationID$/init.mp4" {
		t.Errorf("Initialization = %q, want the AdaptationSet's — without it there is no init segment to fetch", got.Initialization)
	}
	if got.Duration != 360000 {
		t.Errorf("Duration = %d, want 360000 merged from the AdaptationSet", got.Duration)
	}
	if got.PresentationTimeOffset != 1800 {
		t.Errorf("PresentationTimeOffset = %d, want 1800 merged from the AdaptationSet", got.PresentationTimeOffset)
	}
	if got.StartNumber == nil || *got.StartNumber != 5 {
		t.Errorf("StartNumber = %v, want 5 merged from the AdaptationSet", got.StartNumber)
	}
	if got.SegmentTimeline != timeline {
		t.Error("SegmentTimeline was not merged: the segment list would be empty")
	}
}

// The other direction: everything stated on the Period alone still reaches the
// representation, through the base copy rather than through a merge.
func TestFirstTemplate_InheritsFromThePeriodAlone(t *testing.T) {
	period := &mpdSegTemplate{
		Media:          "$RepresentationID$/$Number$.m4s",
		Initialization: "$RepresentationID$/init.mp4",
		Timescale:      90000,
		Duration:       360000,
	}
	got := firstTemplate(nil, nil, period)
	if got == nil {
		t.Fatal("a Period-level template did not reach the representation")
	}
	if got.Media != period.Media || got.Timescale != 90000 ||
		got.Initialization != period.Initialization || got.Duration != 360000 {
		t.Errorf("inherited template = %+v, want the Period's values", got)
	}
}

// The merge must not write through to the caller's structs: the AdaptationSet
// template is shared by every representation under it, so mutating it would make
// the second representation inherit the first one's overrides.
func TestFirstTemplate_DoesNotMutateTheParent(t *testing.T) {
	parent := &mpdSegTemplate{Media: "shared/$Number$.m4s", Timescale: 90000}
	child := &mpdSegTemplate{Media: "child/$Number$.m4s"}

	got := firstTemplate(child, parent)
	if got.Media != "child/$Number$.m4s" {
		t.Fatalf("Media = %q, want the child's", got.Media)
	}
	if parent.Media != "shared/$Number$.m4s" {
		t.Errorf("the parent template was overwritten with %q: the next representation would inherit this representation's @media", parent.Media)
	}
}

func TestFirstTemplate_StartNumberZeroIsKeptWhenExplicit(t *testing.T) {
	zero := 0
	// startNumber="0" is legal and different from absent: a pointer is used
	// precisely so the zero value can be told from "not stated".
	got := firstTemplate(&mpdSegTemplate{StartNumber: &zero}, &mpdSegTemplate{Media: "x"})
	if got.StartNumber == nil {
		t.Fatal("an explicit startNumber=\"0\" was dropped")
	}
	if *got.StartNumber != 0 {
		t.Errorf("StartNumber = %d, want 0", *got.StartNumber)
	}
}

func TestFirstTemplate_NoTemplateAtAnyLevel(t *testing.T) {
	if got := firstTemplate(nil, nil, nil); got != nil {
		t.Errorf("firstTemplate with nothing set = %+v, want nil", got)
	}
	if got := firstTemplate(); got != nil {
		t.Errorf("firstTemplate() = %+v, want nil", got)
	}
}

// dashKind decides whether a representation is treated as a video rung. An MPD
// that states neither mimeType nor contentType is common, so the codecs string
// and then the height have to carry the decision.
func TestDashKind(t *testing.T) {
	tests := []struct {
		name string
		as   mpdAdaptation
		want StreamKind
	}{
		{"mimeType video", mpdAdaptation{MimeType: "video/mp4"}, Video},
		{"mimeType audio", mpdAdaptation{MimeType: "audio/mp4"}, Audio},
		{"mimeType text", mpdAdaptation{MimeType: "text/vtt"}, Text},
		{"mimeType upper case", mpdAdaptation{MimeType: "VIDEO/MP4"}, Video},
		{"contentType video", mpdAdaptation{ContentType: "video"}, Video},
		{"contentType audio", mpdAdaptation{ContentType: "audio"}, Audio},
		{"contentType text", mpdAdaptation{ContentType: "text"}, Text},
		{"contentType subtitle", mpdAdaptation{ContentType: "subtitle"}, Text},

		// Neither attribute stated: the codecs string decides.
		{"codecs avc", mpdAdaptation{Codecs: "avc1.640028"}, Video},
		{"codecs hev", mpdAdaptation{Codecs: "hev1.2.4.L153.B0"}, Video},
		{"codecs hvc", mpdAdaptation{Codecs: "hvc1.2.4.L153.B0"}, Video},
		{"codecs vp09", mpdAdaptation{Codecs: "vp09.00.10.08"}, Video},
		{"codecs av01", mpdAdaptation{Codecs: "av01.0.08M.08"}, Video},
		{"codecs mp4a", mpdAdaptation{Codecs: "mp4a.40.2"}, Audio},
		{"codecs ac-3", mpdAdaptation{Codecs: "ac-3"}, Audio},
		{"codecs ec-3", mpdAdaptation{Codecs: "ec-3"}, Audio},
		{"codecs opus", mpdAdaptation{Codecs: "opus"}, Audio},

		// Nothing but a height.
		{"height only", mpdAdaptation{Height: 1080}, Video},
		// An audio codec alongside a stray height: the codec is the stronger
		// statement, and reading this as a video rung would report its absent
		// video track as a defect.
		{"audio codec beats a stray height", mpdAdaptation{Codecs: "mp4a.40.2", Height: 1080}, Audio},
		// Nothing at all: audio is the safe answer, because calling it video
		// would make its absent video track look like a defect.
		{"nothing stated", mpdAdaptation{}, Audio},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := dashKind(tc.as, 0); got != tc.want {
				t.Errorf("dashKind = %s, want %s", got, tc.want)
			}
		})
	}
}

// dashName is what every finding's Target says, so it has to be stable and
// recognisable: an operator reading "audio-en" or "1080p" knows which rung to
// look at, and "p0-as1-r2" is the last resort rather than the norm.
func TestDashName(t *testing.T) {
	tests := []struct {
		name string
		rep  mpdRepresentation
		as   mpdAdaptation
		kind StreamKind
		want string
	}{
		{"representation height", mpdRepresentation{Height: 1080}, mpdAdaptation{}, Video, "1080p"},
		{"inherited height", mpdRepresentation{}, mpdAdaptation{Height: 720}, Video, "720p"},
		{"representation wins", mpdRepresentation{Height: 360}, mpdAdaptation{Height: 720}, Video, "360p"},
		{"audio by language", mpdRepresentation{}, mpdAdaptation{Lang: "en"}, Audio, "audio-en"},
		{"audio by bitrate", mpdRepresentation{Bandwidth: 128000}, mpdAdaptation{}, Audio, "audio-128kbps"},
		{"language beats bitrate", mpdRepresentation{Bandwidth: 128000}, mpdAdaptation{Lang: "it"}, Audio, "audio-it"},
		{"falls back to the id", mpdRepresentation{ID: "video_1"}, mpdAdaptation{}, Video, "video_1"},
		{"last resort is positional", mpdRepresentation{}, mpdAdaptation{}, Video, "p0-as1-r2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := dashName(tc.rep, tc.as, tc.kind, 0, 1, 2); got != tc.want {
				t.Errorf("dashName = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDashKeyMethod(t *testing.T) {
	if got := dashKeyMethod(true); got != "CENC" {
		t.Errorf("dashKeyMethod(true) = %q, want CENC", got)
	}
	// Unprotected content must report no method at all rather than a name that
	// would read as encryption in the output.
	if got := dashKeyMethod(false); got != "" {
		t.Errorf("dashKeyMethod(false) = %q, want the empty string", got)
	}
}

func TestFirstNonZero(t *testing.T) {
	tests := []struct {
		in   []int
		want int
	}{
		{[]int{0, 0, 720}, 720},
		{[]int{1080, 720}, 1080},
		{[]int{0}, 0},
		{nil, 0},
		// A negative value is still "stated", so it wins over a later zero
		// rather than being skipped as if it were absent.
		{[]int{-1, 720}, -1},
	}
	for _, tc := range tests {
		if got := firstNonZero(tc.in...); got != tc.want {
			t.Errorf("firstNonZero(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// streamInfKind is the HLS side of the same decision. An audio-only bottom rung
// declared without RESOLUTION must not be read as video, or its missing video
// track is reported as a defect in a perfectly ordinary ladder.
func TestStreamInfKind(t *testing.T) {
	tests := []struct {
		name   string
		attrs  map[string]string
		height int
		want   StreamKind
	}{
		{"video codec", map[string]string{"CODECS": "avc1.4d401f,mp4a.40.2"}, 0, Video},
		{"audio codec only", map[string]string{"CODECS": "mp4a.40.2"}, 0, Audio},
		{"resolution, no codecs", map[string]string{}, 1080, Video},
		{"nothing declared", map[string]string{}, 0, Video},
		// CODECS wins over the absence of a resolution.
		{"audio codec with a height", map[string]string{"CODECS": "mp4a.40.2"}, 0, Audio},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := streamInfKind(tc.attrs, tc.height); got != tc.want {
				t.Errorf("streamInfKind = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	base, _ := url.Parse("https://cdn.example.com/hls/master.m3u8")
	tests := []struct {
		name string
		base *url.URL
		ref  string
		want string
	}{
		{"relative", base, "720p/index.m3u8", "https://cdn.example.com/hls/720p/index.m3u8"},
		{"absolute path", base, "/v2/index.m3u8", "https://cdn.example.com/v2/index.m3u8"},
		{"absolute url", base, "https://other.example.com/a.m3u8", "https://other.example.com/a.m3u8"},
		{"parent", base, "../audio/index.m3u8", "https://cdn.example.com/audio/index.m3u8"},
		{"query preserved", base, "seg.ts?token=abc", "https://cdn.example.com/hls/seg.ts?token=abc"},
		// With no base there is nothing to resolve against, and the reference is
		// returned untouched rather than mangled.
		{"no base", nil, "720p/index.m3u8", "720p/index.m3u8"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Resolve(tc.base, tc.ref); got != tc.want {
				t.Errorf("Resolve = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestApplyBaseURLs(t *testing.T) {
	base, _ := url.Parse("https://cdn.example.com/dash/manifest.mpd")

	// In document order, each BaseURL resolved against the running base.
	got := applyBaseURLs(base, []string{"v1/"})
	if got.String() != "https://cdn.example.com/dash/v1/" {
		t.Errorf("one BaseURL = %q", got.String())
	}

	got = applyBaseURLs(base, []string{"v1/", "hi/"})
	if got.String() != "https://cdn.example.com/dash/v1/hi/" {
		t.Errorf("two BaseURLs = %q, want them applied in order", got.String())
	}

	// Blank and whitespace-only elements are ignored rather than resetting the
	// base to the manifest directory.
	got = applyBaseURLs(base, []string{"v1/", "   ", ""})
	if got.String() != "https://cdn.example.com/dash/v1/" {
		t.Errorf("blank BaseURL changed the base: %q", got.String())
	}

	if got := applyBaseURLs(base, nil); got.String() != base.String() {
		t.Errorf("no BaseURL changed the base: %q", got.String())
	}
}
