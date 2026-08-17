package analyze

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// An audio rendition delivered at a rate other than the one the manifest states
// is a real defect: a player configures its output from the manifest before it
// has a single byte of media, so a mismatch is a pitch shift or silence. Only
// DASH makes this claim — HLS states no sampling rate anywhere — so the rate
// comparison is exercised through an MPD.
func TestRun_AudioSampleRateContradictsManifest(t *testing.T) {
	srv := newDASHAudioOrigin(t, dashAudioSpec{
		declaredRate: 48000, declaredChannels: 2,
		mediaRate: 44100, mediaChannels: 2,
	})
	res := runOn(t, srv+"/manifest.mpd")

	f, ok := findFinding(res, "audio", finding.BAD)
	if !ok {
		t.Fatalf("a 44.1kHz rendition declared at 48kHz was not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "44.1 kHz") || !strings.Contains(f.Message, "48 kHz") {
		t.Errorf("finding does not name both rates: %q", f.Message)
	}
}

// The same MPD with the media matching its claims must come back clean: the rate
// comparison has to be exact without being brittle about how a rate is spelled.
func TestRun_AudioDASHMatchingManifestHasNoProblems(t *testing.T) {
	srv := newDASHAudioOrigin(t, dashAudioSpec{
		declaredRate: 48000, declaredChannels: 2,
		mediaRate: 48000, mediaChannels: 2,
	})
	res := runOn(t, srv+"/manifest.mpd")

	for _, f := range res.Findings {
		if f.Status != finding.OK {
			t.Errorf("clean DASH audio produced %s on %s/%s: %s", f.Status, f.Check, f.Target, f.Message)
		}
	}
	if !hasCheck(res, "audio") {
		t.Error("no audio finding at all: the check did not run")
	}
}

// The channel count is the claim HLS does make, and it is the one a player uses
// to pick a rendition for the output device it has.
func TestRun_AudioChannelsContradictManifest(t *testing.T) {
	res := runOn(t, newAudioOrigin(t, audioSpec{
		channels: 6, sampleRate: 48000, declared: `CHANNELS="2"`,
	})+"/master-audio.m3u8")

	f, ok := findFinding(res, "audio", finding.BAD)
	if !ok {
		t.Fatalf("5.1 media declared as stereo was not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "5.1") || !strings.Contains(f.Message, "stereo") {
		t.Errorf("finding does not name both layouts: %q", f.Message)
	}
}

// A rendition whose audio format changes part-way through is a defect no
// manifest claim is needed to spot: a decoder configured for the first segment
// will not play the rest.
func TestRun_AudioFormatChangesMidRendition(t *testing.T) {
	res := runOn(t, newAudioOrigin(t, audioSpec{
		channels: 2, sampleRate: 48000, declared: `CHANNELS="2"`,
		monoFrom: 3, // from the third segment on, the media is mono
	})+"/master-audio.m3u8")

	f, ok := findFinding(res, "audio", finding.BAD)
	if !ok {
		t.Fatalf("a rendition that turns mono half-way through was not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "changes") {
		t.Errorf("finding does not say the format changes: %q", f.Message)
	}
}

// With nothing declared there is nothing to contradict: report what the media is
// and stay at OK. Silence would hide the measurement; a WARN would blame the
// stream for the manifest's reticence.
func TestRun_AudioUndeclaredIsReportedNotFlagged(t *testing.T) {
	res := runOn(t, newAudioOrigin(t, audioSpec{
		channels: 2, sampleRate: 48000, declared: "",
	})+"/master-audio.m3u8")

	f, ok := findFinding(res, "audio", finding.OK)
	if !ok {
		t.Fatalf("undeclared audio produced no measurement at all.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "48 kHz") || !strings.Contains(f.Message, "stereo") {
		t.Errorf("finding does not report the measurement: %q", f.Message)
	}
}

// The agreeing case: the check must not cry wolf on audio that is exactly what
// the playlist says it is.
func TestRun_AudioMatchingManifestHasNoProblems(t *testing.T) {
	res := runOn(t, newAudioOrigin(t, audioSpec{
		channels: 2, sampleRate: 48000, declared: `CHANNELS="2"`,
	})+"/master-audio.m3u8")

	for _, f := range res.Findings {
		if f.Status != finding.OK {
			t.Errorf("clean audio produced %s on %s/%s: %s", f.Status, f.Check, f.Target, f.Message)
		}
	}
	if !hasCheck(res, "audio") {
		t.Error("no audio finding at all: the check did not run")
	}
}

// ---------- harness ----------

type audioSpec struct {
	channels   int
	sampleRate int
	declared   string // extra EXT-X-MEDIA attributes, without a trailing comma
	monoFrom   int    // 1-based segment from which the media goes mono, 0 for never
}

// newAudioOrigin serves a master playlist with one video variant and a separate
// packed-audio rendition — the shape HLS uses for a demuxed presentation, and the
// one where the audio format lives in the bitstream rather than in a container.
// The video variant is there so the ladder check has a rung to look at: a master
// with no video at all is a defect of its own and would drown out the audio
// findings these tests are about.
func newAudioOrigin(t *testing.T, spec audioSpec) string {
	t.Helper()
	const segs = 4
	const framesPerSeg = 94 // ~2s of 1024-sample frames at 48kHz

	attrs := ""
	if spec.declared != "" {
		attrs = spec.declared + ","
	}

	video := variantSpec{
		name: "720p", bandwidth: syntheticBandwidth,
		width: 1280, height: 720, segments: cleanSegments(segs, 1280, 720),
	}
	mux := hlsOriginHandler([]variantSpec{video})
	mux.HandleFunc("/master-audio.m3u8", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:4\n"+
			"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"aud\",NAME=\"English\","+
			"LANGUAGE=\"en\",DEFAULT=YES,%sURI=\"audio.m3u8\"\n"+
			"#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=1280x720,CODECS=\"avc1.4d401f,mp4a.40.2\",AUDIO=\"aud\"\n"+
			"720p/index.m3u8\n", attrs, syntheticBandwidth)
	})
	mux.HandleFunc("/audio.m3u8", func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:3\n#EXT-X-MEDIA-SEQUENCE:0\n")
		for i := 1; i <= segs; i++ {
			fmt.Fprintf(&b, "#EXTINF:%.3f,\na%d.aac\n", float64(framesPerSeg)*1024/float64(spec.sampleRate), i)
		}
		b.WriteString("#EXT-X-ENDLIST\n")
		_, _ = w.Write([]byte(b.String()))
	})
	for i := 1; i <= segs; i++ {
		i := i
		mux.HandleFunc(fmt.Sprintf("/a%d.aac", i), func(w http.ResponseWriter, r *http.Request) {
			ch := spec.channels
			if spec.monoFrom > 0 && i >= spec.monoFrom {
				ch = 1
			}
			pts := int64(i-1) * int64(framesPerSeg) * 1024 * 90000 / int64(spec.sampleRate)
			_, _ = w.Write(mediatest.PackedAudioAt(pts, framesPerSeg, spec.sampleRate, ch))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

type dashAudioSpec struct {
	declaredRate, declaredChannels int // what the MPD says
	mediaRate, mediaChannels       int // what the AudioSampleEntry says
}

// newDASHAudioOrigin serves an MPD with a video and an audio AdaptationSet, the
// audio one carrying @audioSamplingRate and an AudioChannelConfiguration. In fMP4
// the container states both, so the claim and the measurement are directly
// comparable — which is the whole point of the check.
func newDASHAudioOrigin(t *testing.T, spec dashAudioSpec) string {
	t.Helper()
	segs := cleanDASHSegs(4)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/manifest.mpd", func(w http.ResponseWriter, _ *http.Request) {
		var tl strings.Builder
		for i, s := range segs {
			if i == 0 {
				fmt.Fprintf(&tl, `<S t="%d" d="%d"/>`, s.declaredT, dashSegTicks)
				continue
			}
			fmt.Fprintf(&tl, `<S d="%d"/>`, dashSegTicks)
		}
		mpd := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT%dS">
  <Period id="0" start="PT0S">
    <AdaptationSet mimeType="video/mp4" contentType="video">
      <SegmentTemplate timescale="%d" media="seg-$Number$.m4s" initialization="init.mp4" startNumber="0">
        <SegmentTimeline>%s</SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v0" bandwidth="%d" width="1280" height="720" codecs="avc1.4d401f"/>
    </AdaptationSet>
    <AdaptationSet mimeType="audio/mp4" contentType="audio" lang="en" audioSamplingRate="%d">
      <AudioChannelConfiguration schemeIdUri="urn:mpeg:dash:23003:3:audio_channel_configuration:2011" value="%d"/>
      <SegmentTemplate timescale="%d" media="aseg-$Number$.m4s" initialization="ainit.mp4" startNumber="0">
        <SegmentTimeline>%s</SegmentTimeline>
      </SegmentTemplate>
      <Representation id="a0" bandwidth="%d" codecs="mp4a.40.2"/>
    </AdaptationSet>
  </Period>
</MPD>`, len(segs)*2, dashTimescale, tl.String(), dashBandwidth,
			spec.declaredRate, spec.declaredChannels,
			dashTimescale, tl.String(), dashBandwidth)
		w.Header().Set("Content-Type", "application/dash+xml")
		_, _ = w.Write([]byte(mpd))
	})

	mux.HandleFunc("/init.mp4", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(mediatest.MP4Init(1, dashTimescale, "video", 1280, 720))
	})
	mux.HandleFunc("/ainit.mp4", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(mediatest.MP4InitAudio(1, dashTimescale, "mp4a", spec.mediaChannels, spec.mediaRate))
	})
	for i, s := range segs {
		i, s := i, s
		body := mediatest.MP4Segment(1, uint32(i), s.actualTFDT, dashSampleDur, dashSamples, dashPayload)
		mux.HandleFunc(fmt.Sprintf("/seg-%d.m4s", i), func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(body)
		})
		mux.HandleFunc(fmt.Sprintf("/aseg-%d.m4s", i), func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(body)
		})
	}
	return srv.URL
}

// The rate and layout names go into every audio finding, so a mistake here is a
// mistake in the report an operator reads.
func TestHumanAudioFormat(t *testing.T) {
	for _, tc := range []struct {
		rate, channels int
		want           string
	}{
		{48000, 2, "48 kHz stereo"},
		{44100, 2, "44.1 kHz stereo"},
		{7350, 1, "7.35 kHz mono"},
		{48000, 6, "48 kHz 5.1"},
		{48000, 8, "48 kHz 7.1"},
		{48000, 3, "48 kHz 3 channels"},
		{48000, 0, "48 kHz"},
		{0, 2, "stereo"},
		{0, 0, "unknown format"},
	} {
		if got := humanAudioFormat(tc.rate, tc.channels); got != tc.want {
			t.Errorf("humanAudioFormat(%d, %d) = %q, want %q", tc.rate, tc.channels, got, tc.want)
		}
	}
}

// Most HLS ladders mux audio into the video variants, and there the manifest
// makes no audio claim at all — but the media can still contradict itself. A
// variant whose audio turns mono half-way through is broken whether or not any
// playlist attribute mentions audio, so the check has to look at muxed tracks
// and not only at separate audio renditions.
func TestRun_AudioMuxedFormatChangeIsFound(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	for i := range segs {
		segs[i].audioRate, segs[i].audioChannels = 48000, 2
	}
	segs[2].audioChannels = 1
	segs[3].audioChannels = 1

	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: 80_000, width: 1280, height: 720, segments: segs},
	})
	res := runOn(t, srv.URL+"/master.m3u8")

	f, ok := findFinding(res, "audio", finding.BAD)
	if !ok {
		t.Fatalf("a muxed variant that turns mono half-way through was not reported.\n%s", dump(res))
	}
	if !strings.Contains(f.Message, "stereo") || !strings.Contains(f.Message, "mono") {
		t.Errorf("finding does not name both layouts: %q", f.Message)
	}
}

// And the muxed variant that stays consistent must stay quiet above OK.
func TestRun_AudioMuxedConsistentHasNoProblems(t *testing.T) {
	segs := cleanSegments(4, 1280, 720)
	for i := range segs {
		segs[i].audioRate, segs[i].audioChannels = 48000, 2
	}
	srv := newHLSOrigin(t, []variantSpec{
		{name: "720p", bandwidth: 80_000, width: 1280, height: 720, segments: segs},
	})
	res := runOn(t, srv.URL+"/master.m3u8")

	for _, f := range res.Findings {
		if f.Status != finding.OK {
			t.Errorf("clean muxed audio produced %s on %s/%s: %s", f.Status, f.Check, f.Target, f.Message)
		}
	}
	if !hasCheck(res, "audio") {
		t.Error("no audio finding at all: the muxed track was not looked at")
	}
}

// A rendition that carries audio but states no format at all gets an honest
// OK-level "not verified": segcheck could not look, which is a limit of this tool
// and of the bytes, not a defect in the stream. A rendition with no audio track
// gets nothing, because there is nothing to say.
func TestCheckAudio_UnstatedAndAbsent(t *testing.T) {
	// An MPEG-TS AC-3 track is exactly this shape: the PMT names the codec but
	// nothing segcheck reads states the rate or the layout, so the track arrives
	// with neither.
	rd := &renditionData{
		r: manifest.Rendition{Name: "a", Kind: manifest.Audio},
		segs: []segmentData{{parsed: true, info: media.SegmentInfo{
			Tracks: []media.Track{{Kind: media.Audio, Codec: "ac3"}},
		}}},
	}
	out := checkAudio([]*renditionData{rd})
	if len(out) != 1 || out[0].Status != finding.OK || !strings.Contains(out[0].Message, "not verified") {
		t.Fatalf("want one OK 'not verified' finding, got %+v", out)
	}

	// A rendition that failed to load, and a video-only one: both silent.
	quiet := []*renditionData{
		{r: manifest.Rendition{Name: "broken", Kind: manifest.Audio}, err: errUnusable},
		{r: manifest.Rendition{Name: "v", Kind: manifest.Video},
			segs: []segmentData{{info: media.SegmentInfo{Tracks: []media.Track{{Kind: media.Video}}}, parsed: true}}},
	}
	if out := checkAudio(quiet); len(out) != 0 {
		t.Errorf("want no findings, got %+v", out)
	}
}

var errUnusable = errors.New("could not be loaded")

// Both halves wrong at once reports both, and carries the rate as the machine
// -readable value: a consumer parsing Value must not have to guess which of the
// two numbers it got.
func TestCheckAudio_BothClaimsWrong(t *testing.T) {
	init := mediatest.MP4InitAudio(1, 90000, "mp4a", 2, 44100)
	frag := mediatest.MP4Segment(1, 1, 0, 3600, 50, 2000)
	info, err := media.ParseMP4(frag, init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	rd := &renditionData{
		r:    manifest.Rendition{Name: "a", Kind: manifest.Audio, SampleRate: 48000, Channels: 6},
		segs: []segmentData{{info: info, parsed: true}},
	}
	out := checkAudio([]*renditionData{rd})
	if len(out) != 1 || out[0].Status != finding.BAD {
		t.Fatalf("want one BAD finding, got %+v", out)
	}
	if !strings.Contains(out[0].Message, "44.1 kHz") || !strings.Contains(out[0].Message, "5.1") {
		t.Errorf("finding does not name both mismatches: %q", out[0].Message)
	}
	if out[0].Unit != "Hz" {
		t.Errorf("Unit = %q, want Hz: the rate is the primary measurement", out[0].Unit)
	}
}

// The channel count alone being wrong makes the count the reported value.
func TestCheckAudio_ChannelsOnlyCarriesTheCount(t *testing.T) {
	init := mediatest.MP4InitAudio(1, 90000, "mp4a", 2, 48000)
	frag := mediatest.MP4Segment(1, 1, 0, 3600, 50, 2000)
	info, err := media.ParseMP4(frag, init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	rd := &renditionData{
		r:    manifest.Rendition{Name: "a", Kind: manifest.Audio, Channels: 6},
		segs: []segmentData{{info: info, parsed: true}},
	}
	out := checkAudio([]*renditionData{rd})
	if len(out) != 1 || out[0].Unit != "channels" {
		t.Fatalf("want one finding measured in channels, got %+v", out)
	}
	if out[0].Value == nil || *out[0].Value != 2 {
		t.Errorf("Value = %v, want 2", out[0].Value)
	}
}

// HE-AAC codes at half the rate it plays: SBR reconstructs the top octave, so a
// mp4a.40.5 track whose AudioSampleEntry says 24 kHz outputs 48 kHz, and that is
// the rate DASH's @audioSamplingRate states. Sony's DASH-IF reference stream is
// exactly this, and calling it a defect would flag a large share of the world's
// AAC audio.
func TestCheckAudio_HEAACDeclaresTheOutputRate(t *testing.T) {
	init := mediatest.MP4InitAudio(1, 90000, "mp4a", 2, 24000)
	frag := mediatest.MP4Segment(1, 1, 0, 3600, 50, 2000)
	info, err := media.ParseMP4(frag, init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	for _, codec := range []string{"mp4a.40.5", "mp4a.40.29", "mp4a.40.05"} {
		rd := &renditionData{
			r: manifest.Rendition{Name: "a", Kind: manifest.Audio,
				Codecs: codec, SampleRate: 48000, Channels: 2},
			segs: []segmentData{{info: info, parsed: true}},
		}
		out := checkAudio([]*renditionData{rd})
		if len(out) != 1 || out[0].Status != finding.OK {
			t.Errorf("%s: want one OK finding, got %+v", codec, out)
			continue
		}
		if !strings.Contains(out[0].Message, "24 kHz") {
			t.Errorf("%s: the finding should report the coded rate: %q", codec, out[0].Message)
		}
	}

	// Plain AAC-LC gets no such allowance: half the declared rate is a defect.
	rd := &renditionData{
		r: manifest.Rendition{Name: "a", Kind: manifest.Audio,
			Codecs: "mp4a.40.2", SampleRate: 48000, Channels: 2},
		segs: []segmentData{{info: info, parsed: true}},
	}
	if out := checkAudio([]*renditionData{rd}); len(out) != 1 || out[0].Status != finding.BAD {
		t.Errorf("AAC-LC at half the declared rate: want one BAD finding, got %+v", out)
	}

	// A CODECS value that is not an AAC object type at all, and one whose object
	// type is not a number: neither signals SBR, and neither may panic.
	for _, codecs := range []string{"avc1.640028", "mp4a.40.x", "", "avc1.4d401f,mp4a.40.2"} {
		if codecSignalsSBR(codecs) {
			t.Errorf("codecSignalsSBR(%q) = true", codecs)
		}
	}

	// And SBR doubles; it does not treble.
	rd.r.Codecs, rd.r.SampleRate = "mp4a.40.5", 72000
	if out := checkAudio([]*renditionData{rd}); len(out) != 1 || out[0].Status != finding.BAD {
		t.Errorf("HE-AAC at a third of the declared rate: want one BAD finding, got %+v", out)
	}
}

// A rendition that declares one audio codec and ships another is a rendition a
// player will not decode at all: CODECS is what it checks before it commits, so
// an ec-3 declaration over an mp4a track is silence on a device that has no
// E-AC-3 decoder and would have played the AAC happily.
func TestRun_AudioCodecContradictsManifest(t *testing.T) {
	init := mediatest.MP4InitAudio(1, 90000, "mp4a", 2, 48000)
	frag := mediatest.MP4Segment(1, 1, 0, 3600, 50, 2000)
	info, err := media.ParseMP4(frag, init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	rd := &renditionData{
		r: manifest.Rendition{Name: "a", Kind: manifest.Audio,
			Codecs: "ec-3", SampleRate: 48000, Channels: 2},
		segs: []segmentData{{info: info, parsed: true}},
	}
	out := checkAudio([]*renditionData{rd})
	if len(out) != 1 || out[0].Status != finding.BAD {
		t.Fatalf("want one BAD finding, got %+v", out)
	}
	if !strings.Contains(out[0].Message, "aac") || !strings.Contains(out[0].Message, "ec-3") {
		t.Errorf("finding does not name both codecs: %q", out[0].Message)
	}
}

// The declarations that legitimately name the same codec must not be flagged: a
// CODECS value carries a profile the track does not state, a video variant lists
// its video codec alongside the audio one, and E-AC-3 media is declared ec-3.
func TestCheckAudio_CodecsThatAgree(t *testing.T) {
	frag := mediatest.MP4Segment(1, 1, 0, 3600, 50, 2000)
	for _, tc := range []struct {
		codecs string
		init   []byte
	}{
		{"mp4a.40.2", mediatest.MP4InitAudio(1, 90000, "mp4a", 2, 48000)},
		{"avc1.4d401f,mp4a.40.2", mediatest.MP4InitAudio(1, 90000, "mp4a", 2, 48000)},
		{"ec-3", mediatest.MP4InitEAC3(1, 90000, 6, 48000, 0)},
		{"ac-3", mediatest.MP4InitAC3(1, 90000, 6, 48000)},
		// Nothing declared cannot be contradicted.
		{"", mediatest.MP4InitAudio(1, 90000, "mp4a", 2, 48000)},
	} {
		info, err := media.ParseMP4(frag, tc.init)
		if err != nil {
			t.Fatalf("%s: ParseMP4: %v", tc.codecs, err)
		}
		rd := &renditionData{
			r:    manifest.Rendition{Name: "a", Kind: manifest.Audio, Codecs: tc.codecs},
			segs: []segmentData{{info: info, parsed: true}},
		}
		for _, f := range checkAudio([]*renditionData{rd}) {
			if f.Status != finding.OK {
				t.Errorf("CODECS=%q produced %s: %s", tc.codecs, f.Status, f.Message)
			}
		}
	}
}

// What a CODECS value does and does not amount to a comparable audio claim.
func TestDeclaredAudioCodec(t *testing.T) {
	for _, tc := range []struct {
		codecs string
		want   string
		as     string
		ok     bool
	}{
		{"mp4a.40.2", "aac", "mp4a.40.2", true},
		{"avc1.4d401f,mp4a.40.2", "aac", "mp4a.40.2", true},
		{"ec-3", "eac3", "ec-3", true},
		{"fLaC", "flac", "fLaC", true},
		{"mp4a.6B", "mp3", "mp4a.6B", true},
		{"mp4a.69", "mp3", "mp4a.69", true},
		{"dtsc", "dts", "dtsc", true},
		// A rendition cannot be two audio codecs at once, so a value naming two
		// states nothing to compare. Neither does a video-only value, a codec this
		// table does not know, an empty one, or a token too short to read.
		{"mp4a.40.2,ec-3", "", "", false},
		{"avc1.4d401f", "", "", false},
		{"zzzz", "", "", false},
		{"", "", "", false},
		{"a,,b", "", "", false},
	} {
		name, as, ok := declaredAudioCodec(tc.codecs)
		if name != tc.want || as != tc.as || ok != tc.ok {
			t.Errorf("declaredAudioCodec(%q) = %q/%q/%v, want %q/%q/%v",
				tc.codecs, name, as, ok, tc.want, tc.as, tc.ok)
		}
	}
}
