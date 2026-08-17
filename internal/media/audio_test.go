package media

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// SC-18: what the audio track actually runs at.
//
// A rendition whose sampling rate or channel count changes mid-stream forces a
// decoder reset, which most players show as a gap in the audio — and nothing in
// the manifest reveals it, because the manifest states one value for the whole
// rendition. The same numbers are what the manifest's own claims get compared
// against: HLS `CHANNELS`, DASH `@audioSamplingRate` and
// `AudioChannelConfiguration`.
//
// Three containers state it in three places, and until now segcheck read none of
// them onto the track: fMP4 in the AudioSampleEntry, packed audio in the ADTS
// header, MPEG-TS in the ADTS frames inside the PES payload.

func TestParseMP4_AudioSampleEntryStatesRateAndChannels(t *testing.T) {
	tests := []struct {
		entry    string
		channels int
		rate     int
	}{
		{"mp4a", 2, 48000},
		{"mp4a", 6, 48000}, // 5.1
		{"mp4a", 8, 48000}, // 7.1
		{"mp4a", 1, 44100}, // mono at the CD rate
	}
	// Deliberately no ac-3/ec-3 rows: those codecs misstate the channelcount
	// field, and their layout is read from dac3/dec3 instead. See the tests below.
	for _, tc := range tests {
		init := mediatest.MP4InitAudio(1, uint32(tc.rate), tc.entry, tc.channels, tc.rate)
		frag := mediatest.MP4Segment(1, 1, 0, 1024, 50, 2000)

		info, err := ParseMP4(frag, init)
		if err != nil {
			t.Fatalf("%s: ParseMP4: %v", tc.entry, err)
		}
		tr, ok := info.Track(Audio)
		if !ok {
			t.Fatalf("%s: no audio track", tc.entry)
		}
		if tr.Channels != tc.channels {
			t.Errorf("%s: channels = %d, want %d", tc.entry, tr.Channels, tc.channels)
		}
		if tr.SampleRate != tc.rate {
			t.Errorf("%s: sample rate = %d, want %d", tc.entry, tr.SampleRate, tc.rate)
		}
	}
}

// The sampling rate is 16.16 fixed point in the sample entry. Taking the whole
// word reports 48000<<16 — a bit over 3 billion Hz — which is the kind of number
// that survives every guard and lands in a finding.
func TestParseMP4_SampleRateIsFixedPoint(t *testing.T) {
	init := mediatest.MP4InitAudio(1, 48000, "mp4a", 2, 48000)
	info, err := ParseMP4(mediatest.MP4Segment(1, 1, 0, 1024, 50, 2000), init)
	if err != nil {
		t.Fatal(err)
	}
	tr, _ := info.Track(Audio)
	if tr.SampleRate > 384000 {
		t.Errorf("sample rate = %d — the 16.16 fixed point was read whole", tr.SampleRate)
	}
}

// A video sample entry has a channel count nowhere, and reading one out of the
// frame size would report 1080 channels.
func TestParseMP4_VideoTrackStatesNoAudioFields(t *testing.T) {
	init := mediatest.MP4Init(1, 90000, "video", 1920, 1080)
	info, err := ParseMP4(mediatest.MP4Segment(1, 1, 0, 3600, 25, 2000), init)
	if err != nil {
		t.Fatal(err)
	}
	tr, _ := info.Track(Video)
	if tr.Channels != 0 || tr.SampleRate != 0 {
		t.Errorf("video track reported %d channels at %dHz", tr.Channels, tr.SampleRate)
	}
}

// Packed audio states both in every ADTS header. The rate was already read to
// convert the duration; it was simply never kept.
func TestParsePackedAudio_StatesRateAndChannels(t *testing.T) {
	info, err := ParsePackedAudio(mediatest.PackedAudio(90000, 10))
	if err != nil {
		t.Fatalf("ParsePackedAudio: %v", err)
	}
	tr, ok := info.Track(Audio)
	if !ok {
		t.Fatal("no audio track")
	}
	if tr.SampleRate != 48000 {
		t.Errorf("sample rate = %d, want 48000", tr.SampleRate)
	}
	if tr.Channels != 2 {
		t.Errorf("channels = %d, want 2", tr.Channels)
	}
}

// MPEG-TS carries AAC as ADTS inside the PES payload, so the numbers are in the
// bitstream rather than in the container. The elementary-stream capture used to be
// video-only, which is why an MPEG-TS audio rendition reported neither.
func TestParseTS_AudioRateAndChannelsFromADTS(t *testing.T) {
	info, err := ParseTS(mediatest.TSWithAAC(0, 3600, 8, 44100, 2))
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	tr, ok := info.Track(Audio)
	if !ok {
		t.Fatal("no audio track")
	}
	if tr.SampleRate != 44100 {
		t.Errorf("sample rate = %d, want 44100", tr.SampleRate)
	}
	if tr.Channels != 2 {
		t.Errorf("channels = %d, want 2", tr.Channels)
	}
}

// AC-3 encoders write 2 into the AudioSampleEntry channelcount whatever the real
// layout is — Apple's own reference stream does it for a 5.1 track. The layout
// lives in the dac3 box, so trusting the sample entry reports every surround AC-3
// rendition as stereo and contradicts a manifest that correctly says CHANNELS=6.
func TestParseMP4_AC3ChannelsComeFromDAC3(t *testing.T) {
	info, err := ParseMP4(mediatest.MP4Segment(1, 1, 0, 1024, 50, 2000), mediatest.MP4InitAC3(1, 90000, 6, 48000))
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, ok := info.Track(Audio)
	if !ok {
		t.Fatal("no audio track")
	}
	if tr.Channels != 6 {
		t.Errorf("channels = %d, want 6 from the dac3 box", tr.Channels)
	}
	if tr.SampleRate != 48000 {
		t.Errorf("sample rate = %d, want 48000", tr.SampleRate)
	}
}

// E-AC-3 states the same thing in a dec3 box, per substream.
func TestParseMP4_EAC3ChannelsComeFromDEC3(t *testing.T) {
	info, err := ParseMP4(mediatest.MP4Segment(1, 1, 0, 1024, 50, 2000), mediatest.MP4InitEAC3(1, 90000, 6, 48000, 0))
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, ok := info.Track(Audio)
	if !ok {
		t.Fatal("no audio track")
	}
	if tr.Channels != 6 {
		t.Errorf("channels = %d, want 6 from the dec3 box", tr.Channels)
	}
}

// With dependent substreams the independent one's acmod no longer describes the
// whole programme — 7.1 is 5.1 plus a dependent pair. Counting only the
// independent substream would report 5.1 for a 7.1 track, so the count stays
// unknown instead: a confident wrong number is worse than no number.
func TestParseMP4_EAC3DependentSubstreamsLeaveChannelsUnknown(t *testing.T) {
	info, err := ParseMP4(mediatest.MP4Segment(1, 1, 0, 1024, 50, 2000), mediatest.MP4InitEAC3(1, 90000, 8, 48000, 1))
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, ok := info.Track(Audio)
	if !ok {
		t.Fatal("no audio track")
	}
	if tr.Channels != 0 {
		t.Errorf("channels = %d, want 0 with dependent substreams present", tr.Channels)
	}
	if tr.SampleRate != 48000 {
		t.Errorf("sample rate = %d, want 48000: the rate is stated regardless", tr.SampleRate)
	}
}

// And an AC-3 entry with no dac3 box at all must leave the count unknown rather
// than fall back to the channelcount field the codec is known to misstate.
func TestParseMP4_AC3WithoutDAC3LeavesChannelsUnknown(t *testing.T) {
	info, err := ParseMP4(mediatest.MP4Segment(1, 1, 0, 1024, 50, 2000), mediatest.MP4InitAudio(1, 90000, "ac-3", 2, 48000))
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, ok := info.Track(Audio)
	if !ok {
		t.Fatal("no audio track")
	}
	if tr.Channels != 0 {
		t.Errorf("channels = %d, want 0: the ac-3 channelcount field is not authoritative", tr.Channels)
	}
}

// The AC-3 rate table, and what happens at the edges of it. A rate AC-3 has no
// fscod for leaves the codec box silent about it, and the sample entry's own
// samplerate field — which AC-3 does write honestly — stands.
func TestParseMP4_AC3RatesAndLayouts(t *testing.T) {
	frag := mediatest.MP4Segment(1, 1, 0, 1024, 50, 2000)
	for _, tc := range []struct {
		name           string
		rate, channels int
		wantRate       int
		wantChannels   int
	}{
		{"48k 5.1", 48000, 6, 48000, 6},
		{"44.1k stereo", 44100, 2, 44100, 2},
		{"32k mono", 32000, 1, 32000, 1},
		// 24 kHz has no fscod: the dac3 box says nothing about the rate, so the
		// sample entry's own samplerate field stands. (Rates above 65535 cannot
		// be stated in that field at all — its integer part is 16 bits — which is
		// why this case uses one that fits.)
		{"24k has no fscod", 24000, 2, 24000, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := ParseMP4(frag, mediatest.MP4InitAC3(1, 90000, tc.channels, tc.rate))
			if err != nil {
				t.Fatalf("ParseMP4: %v", err)
			}
			tr, ok := info.Track(Audio)
			if !ok {
				t.Fatal("no audio track")
			}
			if tr.SampleRate != tc.wantRate || tr.Channels != tc.wantChannels {
				t.Errorf("%dHz/%dch, want %dHz/%dch", tr.SampleRate, tr.Channels, tc.wantRate, tc.wantChannels)
			}
		})
	}
}

// A truncated or missing codec box must leave the channel count unknown rather
// than read past the end of it.
func TestAudioSampleEntryFields_Edges(t *testing.T) {
	if ch, rate := audioSampleEntryFields("mp4a", make([]byte, 12)); ch != 0 || rate != 0 {
		t.Errorf("a sample entry shorter than its fixed fields gave %dch/%dHz, want 0/0", ch, rate)
	}
	// An ac-3 entry whose only child box is something else: no dac3, no count.
	entry := append(make([]byte, audioSampleEntrySize), 0x00, 0x00, 0x00, 0x08, 'b', 't', 'r', 't')
	if ch, _ := audioSampleEntryFields("ac-3", entry); ch != 0 {
		t.Errorf("an ac-3 entry with no dac3 gave %d channels, want 0", ch)
	}
	if _, _, ok := parseDAC3([]byte{0x0c, 0x3d}); ok {
		t.Error("a two-byte dac3 was accepted")
	}
	if _, _, ok := parseDEC3([]byte{0x00, 0xc0, 0x20, 0x0f}); ok {
		t.Error("a four-byte dec3 was accepted")
	}
}

// A rate index the ADTS tables reserve, and a channel configuration that defers
// the layout to the AudioSpecificConfig, both mean "not stated here".
func TestADTSHeaderFields_Unreadable(t *testing.T) {
	// A well-formed sync word with sampling_frequency_index 13 (reserved).
	reserved := []byte{0xFF, 0xF1, 0x00 | 13<<2, 0x40, 0x00, 0x00, 0xFC}
	if _, _, ok := adtsHeaderFields(reserved); ok {
		t.Error("a reserved sampling_frequency_index was reported as a rate")
	}
	// channel_configuration 0: the layout is in the AudioSpecificConfig.
	cfgZero := []byte{0xFF, 0xF1, byte(4 << 2), 0x00, 0x00, 0x00, 0xFC}
	if _, _, ok := adtsHeaderFields(cfgZero); ok {
		t.Error("channel_configuration 0 was reported as a channel count")
	}
	if _, _, ok := adtsHeaderFields([]byte{0x00, 0x01, 0x02}); ok {
		t.Error("bytes with no sync word were reported as an ADTS header")
	}
}

// 7.1 is the one channel count whose ADTS configuration is not its own number,
// and a rate the table does not hold must not silently become a wrong one.
func TestADTSFrame_Edges(t *testing.T) {
	info, err := ParsePackedAudio(mediatest.PackedAudioAt(0, 4, 48000, 8))
	if err != nil {
		t.Fatalf("ParsePackedAudio: %v", err)
	}
	tr, _ := info.Track(Audio)
	if tr.Channels != 8 {
		t.Errorf("channels = %d, want 8 from channel_configuration 7", tr.Channels)
	}
	// 12345 Hz has no index; the builder falls back to its default rate rather
	// than writing a reserved index a parser would have to reject.
	info, err = ParsePackedAudio(mediatest.PackedAudioAt(0, 4, 12345, 2))
	if err != nil {
		t.Fatalf("ParsePackedAudio: %v", err)
	}
	tr, _ = info.Track(Audio)
	if tr.SampleRate != 48000 {
		t.Errorf("sample rate = %d, want the builder default 48000", tr.SampleRate)
	}
}

// The bounds and the scan, at the places a malformed segment reaches them.
func TestAudioFieldBounds(t *testing.T) {
	// A channelcount no real programme has: the field is 16 bits wide, so a
	// corrupt or misaligned entry can state thousands. Unknown beats absurd.
	frag := mediatest.MP4Segment(1, 1, 0, 1024, 50, 2000)
	info, err := ParseMP4(frag, mediatest.MP4InitAudio(1, 90000, "mp4a", 9999, 48000))
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	if tr, ok := info.Track(Audio); !ok || tr.Channels != 0 {
		t.Errorf("channels = %d, want 0 for an implausible count", tr.Channels)
	}

	// The ADTS scan skips leading bytes that are not a sync word rather than
	// giving up: an MPEG-TS PES payload can begin mid-alignment.
	b := append([]byte{0x00, 0x00, 0x00, 0x00}, mediatest.ADTSHeaderBytes(48000, 2)...)
	rate, ch, ok := adtsHeaderFields(b)
	if !ok || rate != 48000 || ch != 2 {
		t.Errorf("adtsHeaderFields after four stray bytes = %d/%d/%v, want 48000/2/true", rate, ch, ok)
	}
}
