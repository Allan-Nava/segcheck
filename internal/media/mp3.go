package media

// MPEG audio (MP3) frames.
//
// HLS delivers an audio rendition as packed MP3 as often as packed AAC. Recognising
// the format and stopping was honest but left the duration check with nothing to
// compare against, so a rendition declaring six seconds a segment and shipping four
// went unreported.
//
// A frame's length follows from four fields together — version, layer, bitrate index
// and sampling rate index — and getting any one of them wrong walks off into the
// middle of a frame and counts a plausible wrong number of them. That is why the
// reserved values are refused rather than defaulted: a frame this reader cannot
// measure leaves the segment an unsupported container, where a duration of zero would
// have the duration check report a stream eight seconds short.

// mp3BitratesMPEG1 is indexed by [layer][bitrate_index], layer being 1 for Layer III,
// 2 for Layer II and 3 for Layer I, as the header codes them. Index 0 is free format
// and 15 is reserved; both mean this reader cannot compute a length.
var mp3BitratesMPEG1 = map[int][16]int{
	3: {0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 0}, // Layer I
	2: {0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 0},    // Layer II
	1: {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},     // Layer III
}

// mp3BitratesMPEG2 covers MPEG-2 and MPEG-2.5, where Layers II and III share a table.
var mp3BitratesMPEG2 = map[int][16]int{
	3: {0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 0}, // Layer I
	2: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},      // Layer II
	1: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},      // Layer III
}

// mp3Rates is indexed by [version][rate_index]. Index 3 is reserved.
var mp3Rates = map[int][4]int{
	3: {44100, 48000, 32000, 0}, // MPEG-1
	2: {22050, 24000, 16000, 0}, // MPEG-2
	0: {11025, 12000, 8000, 0},  // MPEG-2.5
}

// mp3Frame is one frame header, decoded.
type mp3Frame struct {
	size       int
	samples    int
	sampleRate int
	channels   int
}

// parseMP3Frame reads a frame header. It reports false for anything it cannot
// measure, which includes every reserved value: a length guessed from a reserved
// field is a length that walks into the next frame.
func parseMP3Frame(b []byte) (mp3Frame, bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return mp3Frame{}, false
	}
	version := int(b[1] >> 3 & 0x03)
	layer := int(b[1] >> 1 & 0x03)
	bitrateIdx := int(b[2] >> 4 & 0x0F)
	rateIdx := int(b[2] >> 2 & 0x03)
	padding := b[2]>>1&0x01 == 1
	mode := int(b[3] >> 6 & 0x03)

	// Version 1 and layer 0 are both reserved.
	if version == 1 || layer == 0 {
		return mp3Frame{}, false
	}
	table := mp3BitratesMPEG2
	if version == 3 {
		table = mp3BitratesMPEG1
	}
	bitrate := table[layer][bitrateIdx] * 1000
	rate := mp3Rates[version][rateIdx]
	if bitrate == 0 || rate == 0 {
		return mp3Frame{}, false
	}

	pad := 0
	if padding {
		pad = 1
	}
	f := mp3Frame{sampleRate: rate, channels: 2}
	if mode == 3 {
		f.channels = 1 // single channel
	}
	switch layer {
	case 3: // Layer I counts its length in four-byte slots
		f.samples = 384
		f.size = (12*bitrate/rate + pad) * 4
	case 2:
		f.samples = 1152
		f.size = 1152/8*bitrate/rate + pad
	default: // Layer III
		f.samples = 1152
		if version != 3 {
			// MPEG-2 and MPEG-2.5 halve the samples per frame in Layer III, and a
			// reader that did not would report every such rendition at twice its
			// real duration.
			f.samples = 576
		}
		f.size = f.samples/8*bitrate/rate + pad
	}
	if f.size < 4 {
		return mp3Frame{}, false
	}
	return f, true
}

// scanMP3 walks the frame headers, returning the frame count, the total sample count,
// the sampling rate and the channel count.
func scanMP3(b []byte) (frames, samples, rate, channels int, ok bool) {
	for off := 0; off+4 <= len(b); {
		f, good := parseMP3Frame(b[off:])
		if !good {
			// Lost sync. A trailing partial frame is normal at the end of a segment;
			// anything earlier means this is not what it claims, or carries a field
			// this reader will not guess at.
			break
		}
		if off+f.size > len(b) {
			break // a truncated final frame
		}
		if frames == 0 {
			rate, channels = f.sampleRate, f.channels
		}
		frames++
		samples += f.samples
		// No frame cap is needed: every frame is at least four bytes and off only
		// ever advances, so the walk is bounded by the segment itself.
		off += f.size
	}
	if frames == 0 {
		return 0, 0, 0, 0, false
	}
	return frames, samples, rate, channels, true
}
