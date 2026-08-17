package mediatest

// MPEG audio builders.
//
// HLS allows an audio rendition to be delivered as packed MP3 as well as packed
// AAC. The frame header states the version, the layer, the bitrate and the sampling
// rate, and the frame's length follows from all four — so a builder that wrote a
// fixed size would agree with any reader, however wrong.

// MP3Options controls what the builder plants.
type MP3Options struct {
	// Version: 3 is MPEG-1, 2 is MPEG-2, 0 is MPEG-2.5, as the two header bits code
	// them.
	Version int
	// Layer: 1 is Layer III, 2 is Layer II, 3 is Layer I, again as coded.
	Layer int
	// BitrateIndex and RateIndex are the table indexes, not the values.
	BitrateIndex int
	RateIndex    int
	// Padding sets the padding bit, which adds a byte (four, in Layer I).
	Padding bool
	// Frames is how many to write.
	Frames int
}

// MP3Frames builds a bare sequence of MPEG audio frames.
func MP3Frames(opts MP3Options) []byte {
	if opts.Frames == 0 {
		opts.Frames = 1
	}
	var out []byte
	for i := 0; i < opts.Frames; i++ {
		out = append(out, mp3Frame(opts)...)
	}
	return out
}

// PackedMP3 builds an MP3 segment preceded by an ID3 tag stating startPTS on the
// 90kHz clock, which is how HLS gives a packed-audio rendition a timeline.
func PackedMP3(startPTS int64, opts MP3Options) []byte {
	return append(ID3Timestamp(startPTS), MP3Frames(opts)...)
}

// MP3Frame builds a single frame, for a caller that needs one rather than a segment.
func MP3Frame(opts MP3Options) []byte {
	opts.Frames = 0 // one, by the default MP3Frames applies
	return MP3Frames(opts)
}

// MP3Default is a 128 kbps 44.1 kHz MPEG-1 Layer III frame — the shape of nearly
// every MP3 rendition in the wild.
func MP3Default(frames int) MP3Options {
	return MP3Options{Version: 3, Layer: 1, BitrateIndex: 9, RateIndex: 0, Frames: frames}
}

// mp3Frame builds one frame: a four-byte header followed by enough zeroes to make
// up the length the header declares.
func mp3Frame(opts MP3Options) []byte {
	h := make([]byte, 4)
	h[0] = 0xFF
	h[1] = 0xE0 | byte(opts.Version&0x03)<<3 | byte(opts.Layer&0x03)<<1 | 0x01 // protection absent
	h[2] = byte(opts.BitrateIndex&0x0F)<<4 | byte(opts.RateIndex&0x03)<<2
	if opts.Padding {
		h[2] |= 0x02
	}
	h[3] = 0xC0 // mono, no emphasis

	size := mp3FrameSize(opts)
	if size < 4 {
		size = 4
	}
	return append(h, make([]byte, size-4)...)
}

// mp3FrameSize computes the length the header above declares. The tables are
// written out here rather than shared with the parser on purpose: a builder that
// derived its answer from the reader under test would agree with it however wrong
// the reader was.
func mp3FrameSize(opts MP3Options) int {
	mpeg1 := opts.Version == 3
	// Index 15 is reserved, so the table has to hold it: a [15]int panics on it, and
	// a builder that cannot express a reserved value cannot plant one to test.
	var bitrates [16]int
	switch {
	case mpeg1 && opts.Layer == 3:
		bitrates = [16]int{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 0}
	case mpeg1 && opts.Layer == 2:
		bitrates = [16]int{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 0}
	case mpeg1:
		bitrates = [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	case opts.Layer == 3:
		bitrates = [16]int{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 0}
	default:
		bitrates = [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
	}
	// Index 3 is reserved here too.
	rates := [4]int{44100, 48000, 32000, 0}
	if opts.Version == 2 {
		rates = [4]int{22050, 24000, 16000, 0}
	} else if opts.Version == 0 {
		rates = [4]int{11025, 12000, 8000, 0}
	}

	bitrate := bitrates[opts.BitrateIndex&0x0F] * 1000
	rate := rates[opts.RateIndex&0x03]
	if bitrate == 0 || rate == 0 {
		return 0
	}
	pad := 0
	if opts.Padding {
		pad = 1
	}
	if opts.Layer == 3 { // Layer I counts in four-byte slots
		return (12*bitrate/rate + pad) * 4
	}
	samples := 1152
	if !mpeg1 && opts.Layer != 3 {
		samples = 576
	}
	return samples/8*bitrate/rate + pad
}
