package manifest

import (
	"testing"
	"time"
)

// fixedNow pins the clock: a static MPD needs no live edge, but ParseDASH takes
// one and a real time.Now() would make the test depend on when it ran.
func fixedNow() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
}

// HLS states an audio rendition's channel count on its EXT-X-MEDIA entry. It is
// the only audio claim the playlist makes, so without it there is nothing for an
// audio check to compare the segments against.
func TestParseHLS_MediaChannels(t *testing.T) {
	m := `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",LANGUAGE="en",CHANNELS="2",URI="en.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="Surround",LANGUAGE="en",CHANNELS="6/JOC",URI="sur.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="Silent",LANGUAGE="de",URI="de.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1000000,AUDIO="aud"
v.m3u8
`
	pl, err := ParseHLS([]byte(m), "https://e.test/master.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	// "6/JOC" states the channel count of a *rendered* spatial presentation, not
	// the coded bed — Dolby Atmos ships as "16/JOC" over a 5.1 E-AC-3 bed. A count
	// with a spatial-coding parameter therefore describes something the segments
	// cannot be compared against, and is left unstated rather than misread.
	want := map[string]int{"English": 2, "Surround": 0, "Silent": 0}
	for _, r := range pl.Renditions {
		w, ok := want[r.Name]
		if !ok {
			continue
		}
		if r.Channels != w {
			t.Errorf("%s: channels = %d, want %d", r.Name, r.Channels, w)
		}
		delete(want, r.Name)
	}
	if len(want) != 0 {
		t.Errorf("renditions not seen: %v", want)
	}
}

// DASH states both the sampling rate and the channel count, the rate as an
// attribute and the count in a descriptor. Either may sit on the AdaptationSet
// or on the Representation, and the Representation wins.
func TestParseDASH_AudioRateAndChannels(t *testing.T) {
	m := `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT10S">
 <Period>
  <AdaptationSet contentType="audio" mimeType="audio/mp4" audioSamplingRate="48000" lang="en">
   <AudioChannelConfiguration schemeIdUri="urn:mpeg:dash:23003:3:audio_channel_configuration:2011" value="2"/>
   <SegmentTemplate media="a-$Number$.m4s" initialization="a-init.mp4" duration="2" timescale="1"/>
   <Representation id="a1" bandwidth="128000" codecs="mp4a.40.2"/>
   <Representation id="a2" bandwidth="320000" codecs="mp4a.40.2" audioSamplingRate="44100">
    <AudioChannelConfiguration schemeIdUri="urn:mpeg:dash:23003:3:audio_channel_configuration:2011" value="6"/>
   </Representation>
  </AdaptationSet>
 </Period>
</MPD>
`
	pl, err := ParseDASH([]byte(m), "https://e.test/m.mpd", fixedNow())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	// Both representations share a language, so they share a generated name;
	// bandwidth is what tells them apart.
	want := map[int][2]int{128000: {48000, 2}, 320000: {44100, 6}}
	for _, r := range pl.Renditions {
		w, ok := want[r.Bandwidth]
		if !ok {
			continue
		}
		if r.SampleRate != w[0] || r.Channels != w[1] {
			t.Errorf("%dbps: %dHz/%dch, want %dHz/%dch", r.Bandwidth, r.SampleRate, r.Channels, w[0], w[1])
		}
		delete(want, r.Bandwidth)
	}
	if len(want) != 0 {
		t.Errorf("representations not seen: %v", want)
	}
}

// A channel configuration segcheck does not understand must leave the count at
// zero rather than guess one: the audio check compares against this number, and a
// wrong claim would be reported as a defect in the media.
func TestParseDASH_UnknownChannelSchemeStaysUnknown(t *testing.T) {
	m := `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT10S">
 <Period>
  <AdaptationSet contentType="audio" mimeType="audio/mp4" audioSamplingRate="48000">
   <AudioChannelConfiguration schemeIdUri="tag:dolby.com,2014:dash:audio_channel_configuration:2011" value="F801"/>
   <SegmentTemplate media="a-$Number$.m4s" initialization="a-init.mp4" duration="2" timescale="1"/>
   <Representation id="a1" bandwidth="128000" codecs="ec-3"/>
  </AdaptationSet>
 </Period>
</MPD>
`
	pl, err := ParseDASH([]byte(m), "https://e.test/m.mpd", fixedNow())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	for _, r := range pl.Renditions {
		if r.Channels != 0 {
			t.Errorf("%s: channels = %d, want 0 for a scheme we cannot read", r.Name, r.Channels)
		}
	}
}

// CHANNELS is a string, and a string can be anything. A value that is not a
// count leaves the count unknown rather than becoming zero-as-a-claim.
func TestParseHLSChannels_Unreadable(t *testing.T) {
	for _, v := range []string{"", "stereo", "-2", "0", "/JOC", " "} {
		if got := parseHLSChannels(v); got != 0 {
			t.Errorf("parseHLSChannels(%q) = %d, want 0", v, got)
		}
	}
	if got := parseHLSChannels(" 6 "); got != 6 {
		t.Errorf("parseHLSChannels(\" 6 \") = %d, want 6", got)
	}
	// A spatial-coding parameter makes the count incomparable, not readable.
	if got := parseHLSChannels("16/JOC"); got != 0 {
		t.Errorf("parseHLSChannels(%q) = %d, want 0", "16/JOC", got)
	}
}

// An AdaptationSet may state neither mimeType nor contentType nor codecs and
// leave all of it to its Representations — the DASH-IF MultiResMPEG2 test case
// does exactly that. Falling through to "audio" then classifies every video rung
// as audio, which makes the ladder check report "no video rendition in the
// manifest" on a perfectly good stream.
func TestParseDASH_KindFromTheRepresentation(t *testing.T) {
	m := `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT10S">
 <Period>
  <AdaptationSet segmentAlignment="true" maxWidth="1920" maxHeight="1080" par="16:9">
   <SegmentTemplate media="v-$Number$.m4s" initialization="v-init.mp4" duration="2" timescale="1"/>
   <Representation id="v1" mimeType="video/mp4" codecs="avc1.640028" width="768" height="432" bandwidth="1951761"/>
   <Representation id="v2" mimeType="video/mp4" codecs="avc1.640028" width="1920" height="1080" bandwidth="7953041"/>
  </AdaptationSet>
  <AdaptationSet segmentAlignment="true">
   <SegmentTemplate media="a-$Number$.m4s" initialization="a-init.mp4" duration="2" timescale="1"/>
   <Representation id="a1" mimeType="audio/mp4" codecs="mp4a.40.5" audioSamplingRate="48000" bandwidth="64000"/>
  </AdaptationSet>
 </Period>
</MPD>
`
	pl, err := ParseDASH([]byte(m), "https://e.test/m.mpd", fixedNow())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	want := map[int]StreamKind{1951761: Video, 7953041: Video, 64000: Audio}
	for _, r := range pl.Renditions {
		w, ok := want[r.Bandwidth]
		if !ok {
			t.Errorf("unexpected rendition %s at %d bps", r.Name, r.Bandwidth)
			continue
		}
		if r.Kind != w {
			t.Errorf("%dbps: kind = %q, want %q", r.Bandwidth, r.Kind, w)
		}
		delete(want, r.Bandwidth)
	}
	if len(want) != 0 {
		t.Errorf("renditions not seen: %v", want)
	}
}

// HLS declares closed captions with EXT-X-MEDIA entries that carry no URI at all
// — the captions are muxed into the video — and a variant opts into a group with
// the CLOSED-CAPTIONS attribute, or out of captions entirely with NONE. Both were
// dropped on the floor: the parser skipped every EXT-X-MEDIA without a URI.
func TestParseHLS_ClosedCaptions(t *testing.T) {
	m := `#EXTM3U
#EXT-X-MEDIA:TYPE=CLOSED-CAPTIONS,GROUP-ID="cc",NAME="English",LANGUAGE="en",INSTREAM-ID="CC1",DEFAULT=YES
#EXT-X-MEDIA:TYPE=CLOSED-CAPTIONS,GROUP-ID="cc",NAME="Spanish",LANGUAGE="es",INSTREAM-ID="SERVICE3"
#EXT-X-MEDIA:TYPE=CLOSED-CAPTIONS,GROUP-ID="other",NAME="Unrelated",INSTREAM-ID="CC4"
#EXT-X-STREAM-INF:BANDWIDTH=1000000,RESOLUTION=1280x720,CLOSED-CAPTIONS="cc"
with-cc.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=2000000,RESOLUTION=1920x1080,CLOSED-CAPTIONS=NONE
none.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=500000,RESOLUTION=640x360
silent.m3u8
`
	pl, err := ParseHLS([]byte(m), "https://e.test/master.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	// A CLOSED-CAPTIONS entry is a claim about a variant, not a rendition to go
	// and fetch: there is nothing at the other end of it.
	for _, r := range pl.Renditions {
		if r.Kind == Text && r.URI == "" {
			t.Errorf("%s became a rendition with no URI to sample", r.Name)
		}
	}

	byBW := map[int]Rendition{}
	for _, r := range pl.Renditions {
		byBW[r.Bandwidth] = r
	}

	cc := byBW[1000000]
	if len(cc.Captions) != 2 {
		t.Fatalf("the 720p variant claims %d captions, want 2: %+v", len(cc.Captions), cc.Captions)
	}
	if cc.Captions[0].InstreamID != "CC1" || cc.Captions[0].Language != "en" {
		t.Errorf("first caption = %+v, want CC1/en", cc.Captions[0])
	}
	if cc.Captions[1].InstreamID != "SERVICE3" {
		t.Errorf("second caption = %+v, want SERVICE3", cc.Captions[1])
	}
	if cc.CaptionsNone {
		t.Error("a variant with a CLOSED-CAPTIONS group is marked as declaring none")
	}

	if none := byBW[2000000]; !none.CaptionsNone || len(none.Captions) != 0 {
		t.Errorf("CLOSED-CAPTIONS=NONE gave %+v", none)
	}
	// No attribute at all is not the same claim as NONE: the spec says nothing
	// about what the media carries, so neither does segcheck.
	if silent := byBW[500000]; silent.CaptionsNone || len(silent.Captions) != 0 {
		t.Errorf("a variant with no CLOSED-CAPTIONS attribute gave %+v", silent)
	}
}

// DASH declares them with an Accessibility descriptor on the video AdaptationSet,
// whose value maps a channel or service to a language.
func TestParseDASH_ClosedCaptions(t *testing.T) {
	m := `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT10S">
 <Period>
  <AdaptationSet contentType="video" mimeType="video/mp4">
   <Accessibility schemeIdUri="urn:scte:dash:cc:cea-608:2015" value="CC1=eng;CC3=spa"/>
   <SegmentTemplate media="v-$Number$.m4s" initialization="v-init.mp4" duration="2" timescale="1"/>
   <Representation id="v1" bandwidth="1000000" width="1280" height="720" codecs="avc1.4d401f"/>
  </AdaptationSet>
  <AdaptationSet contentType="video" mimeType="video/mp4">
   <Accessibility schemeIdUri="urn:scte:dash:cc:cea-708:2015" value="1=lang:eng"/>
   <SegmentTemplate media="w-$Number$.m4s" initialization="w-init.mp4" duration="2" timescale="1"/>
   <Representation id="v2" bandwidth="2000000" width="1920" height="1080" codecs="avc1.4d401f"/>
  </AdaptationSet>
 </Period>
</MPD>
`
	pl, err := ParseDASH([]byte(m), "https://e.test/m.mpd", fixedNow())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	want := map[int][]string{
		1000000: {"CC1", "CC3"},
		2000000: {"SERVICE1"},
	}
	for _, r := range pl.Renditions {
		w, ok := want[r.Bandwidth]
		if !ok {
			continue
		}
		var got []string
		for _, c := range r.Captions {
			got = append(got, c.InstreamID)
		}
		if len(got) != len(w) {
			t.Errorf("%dbps: captions = %v, want %v", r.Bandwidth, got, w)
			continue
		}
		for i := range w {
			if got[i] != w[i] {
				t.Errorf("%dbps: captions = %v, want %v", r.Bandwidth, got, w)
				break
			}
		}
		delete(want, r.Bandwidth)
	}
	if len(want) != 0 {
		t.Errorf("representations not seen: %v", want)
	}
}

// The Accessibility value shapes DASH allows, and the ones this parser declines to
// guess at. A descriptor it cannot read must state no claim rather than a wrong
// one: the check compares the bitstream against these names.
func TestDashCaptions_ValueShapes(t *testing.T) {
	m := func(scheme, value string) string {
		return `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT10S">
 <Period>
  <AdaptationSet contentType="video" mimeType="video/mp4">
   <Accessibility schemeIdUri="` + scheme + `" value="` + value + `"/>
   <SegmentTemplate media="v-$Number$.m4s" initialization="v-init.mp4" duration="2" timescale="1"/>
   <Representation id="v1" bandwidth="1000000" width="1280" height="720" codecs="avc1.4d401f"/>
  </AdaptationSet>
 </Period>
</MPD>
`
	}
	for _, tc := range []struct {
		name, scheme, value string
		want                []string
	}{
		// A bare language list under the 608 scheme: position gives the channel.
		{"bare languages", cea608Scheme, "eng;spa", []string{"CC1", "CC2"}},
		{"empty entries are skipped", cea608Scheme, "CC1=eng;;CC2=spa", []string{"CC1", "CC2"}},
		{"708 with a language prefix", cea708Scheme, "1=lang:eng;2=lang:spa", []string{"SERVICE1", "SERVICE2"}},
		// Shapes this parser does not recognise state nothing.
		{"a scheme we do not know", "urn:mpeg:dash:role:2011", "caption", nil},
		{"a 708 service out of range", cea708Scheme, "0=lang:eng;64=lang:spa", nil},
		{"a 708 service that is not a number", cea708Scheme, "x=lang:eng", nil},
		{"a 608 key that is not a channel", cea608Scheme, "sub1=eng", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pl, err := ParseDASH([]byte(m(tc.scheme, tc.value)), "https://e.test/m.mpd", fixedNow())
			if err != nil {
				t.Fatalf("ParseDASH: %v", err)
			}
			var got []string
			for _, c := range pl.Renditions[0].Captions {
				got = append(got, c.InstreamID)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("captions = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("captions = %v, want %v", got, tc.want)
					break
				}
			}
		})
	}
}

// A CLOSED-CAPTIONS entry with no INSTREAM-ID names nothing in the bitstream, so
// there is nothing for the check to look for.
func TestParseHLS_ClosedCaptionsWithoutInstreamID(t *testing.T) {
	m := `#EXTM3U
#EXT-X-MEDIA:TYPE=CLOSED-CAPTIONS,GROUP-ID="cc",NAME="English",LANGUAGE="en"
#EXT-X-STREAM-INF:BANDWIDTH=1000000,RESOLUTION=1280x720,CLOSED-CAPTIONS="cc"
v.m3u8
`
	pl, err := ParseHLS([]byte(m), "https://e.test/master.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if len(pl.Renditions[0].Captions) != 0 {
		t.Errorf("captions = %+v, want none", pl.Renditions[0].Captions)
	}
}

// An EXT-X-MEDIA entry that is not CLOSED-CAPTIONS and carries no URI names
// nothing to fetch, so there is no rendition to make of it either.
func TestParseHLS_MediaWithoutURIIsNotARendition(t *testing.T) {
	m := `#EXTM3U
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="sub",NAME="English",LANGUAGE="en"
#EXT-X-STREAM-INF:BANDWIDTH=1000000,RESOLUTION=1280x720
v.m3u8
`
	pl, err := ParseHLS([]byte(m), "https://e.test/master.m3u8")
	if err != nil {
		t.Fatalf("ParseHLS: %v", err)
	}
	if len(pl.Renditions) != 1 {
		t.Errorf("renditions = %d, want 1: %+v", len(pl.Renditions), pl.Renditions)
	}
}
