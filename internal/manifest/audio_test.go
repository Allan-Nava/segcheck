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
