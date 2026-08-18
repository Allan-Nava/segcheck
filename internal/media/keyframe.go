package media

// Whether a segment opens on a random access point (SC-16).
//
// For MPEG-TS the answer is in the bitstream: the type of the first coded slice.
// The parameter sets, access unit delimiters and SEI messages that precede it say
// nothing about switchability, so the walk has to skip them and stop at the first
// NAL that actually carries picture data.
//
// The mistake to avoid is answering from the first NAL of any kind. A segment
// that opens with an access unit delimiter, or with its SPS and PPS — which is
// what a well-formed MPEG-TS segment does — would then be judged on a header
// rather than on a picture, and the verdict would be arbitrary.

// keyframeScanNALUs bounds the walk looking for a random access point. It has to
// be far more generous than the resolution reader's, which only needs to reach the
// first parameter set: a 1080p picture is split across dozens of slices, and a
// segment whose opening picture belongs to the previous GOP puts all of them in
// front of the keyframe. Sixty-four units was not enough and made Apple's
// reference stream read as having no keyframe at all.
const keyframeScanNALUs = 4096

// H.264 nal_unit_type values, from the low five bits of the header byte.
const (
	nalH264NonIDR = 1 // an ordinary coded slice: not a random access point
	nalH264IDR    = 5 // instantaneous decoder refresh
)

// HEVC nal_unit_type values. 16 through 21 are the IRAP range — BLA_W_LP,
// BLA_W_RADL, BLA_N_LP, IDR_W_RADL, IDR_N_LP and CRA_NUT — and every one of them
// is a point a decoder can be dropped into. Recognising only IDR_W_RADL would
// report a perfectly switchable CRA-opening segment as broken, which matters
// because CRA is what some encoders emit for every segment of a live stream.
const (
	nalHEVCFirstVCL  = 0  // types 0..31 carry picture data
	nalHEVCLastVCL   = 31 //
	nalHEVCFirstIRAP = 16
	nalHEVCLastIRAP  = 21
)

// The verdict has several parts, and the reason is Apple's own bipbop reference
// stream, which is where a stricter reading of this check was caught crying wolf.
//
// Its segments are byte ranges of one main.ts, and a range boundary falls on a
// transport packet rather than on an access unit — so a segment's captured
// elementary stream can open with the last picture of the preceding one, and only
// then reach its real AUD/SPS/PPS/IDR. A player starts at the first IDR and
// discards what precedes it; the stream plays everywhere and is not defective.
//
// So "the first slice is not an IDR" is not on its own a defect. What genuinely
// cannot be switched into is a segment containing no random access point at all.
type keyframeVerdict struct {
	// Opens is true when the very first coded picture is a random access point.
	Opens bool
	// Present is true when one appears anywhere in the captured opening bytes.
	Present bool
	// Known is false when no coded picture was found at all, and the other two
	// fields must then be ignored.
	Known bool
	// Scanned records that the opening bytes really were walked looking for a
	// random access point, which is what makes Present == false mean "there is
	// none" rather than "nobody looked". An fMP4 fragment's sample flags describe
	// its first sample only, so they answer Opens without answering Present.
	Scanned bool
}

// annexBNALUs never yields a zero-length unit — it finds a unit of at least one
// byte or none at all — so the header byte is always there to read. HEVC below
// needs its own guard, because a one-byte unit is possible and its header is two.
func h264Keyframes(es []byte) keyframeVerdict {
	nals, truncated := annexBNALUsLimit(es, keyframeScanNALUs)
	v := keyframeVerdict{Scanned: !truncated}
	for _, nal := range nals {
		switch nal[0] & 0x1F {
		case nalH264IDR:
			v.Present = true
			if !v.Known {
				v.Opens, v.Known = true, true
			}
			return v
		case nalH264NonIDR, 2, 3, 4:
			// Types 2 to 4 are the partitions of a non-IDR slice. The first one
			// settles whether the segment *opens* on a keyframe; the walk carries
			// on to find out whether one arrives later.
			if !v.Known {
				v.Opens, v.Known = false, true
			}
		}
	}
	return v
}

// hevcKeyframes is the same question for HEVC, where the type sits in bits 1 to 6
// of the first of two header bytes rather than in the low five bits of one.
func hevcKeyframes(es []byte) keyframeVerdict {
	nals, truncated := annexBNALUsLimit(es, keyframeScanNALUs)
	v := keyframeVerdict{Scanned: !truncated}
	for _, nal := range nals {
		if len(nal) < 2 {
			continue
		}
		t := int(nal[0]>>1) & 0x3F
		if t < nalHEVCFirstVCL || t > nalHEVCLastVCL {
			continue // a parameter set, an SEI, an end-of-sequence marker
		}
		irap := t >= nalHEVCFirstIRAP && t <= nalHEVCLastIRAP
		if !v.Known {
			v.Opens, v.Known = irap, true
		}
		if irap {
			v.Present = true
			return v
		}
	}
	return v
}

// lengthPrefixedKeyframes is the same question over an fMP4 fragment's samples,
// which carry length-prefixed NAL units where an elementary stream uses start
// codes.
//
// It exists because real content states nothing: Apple's own trick-play
// fragments carry a trun with only a data offset, a tfhd with only a duration
// and a size, and a trex of zeroes — and a zeroed trex is the unset default,
// not an assertion that every sample is a sync sample. Reading it as one would
// call every such fragment a keyframe on no evidence. The samples are the
// evidence, and walking them is an inference, exactly as it is in MPEG-TS.
func lengthPrefixedKeyframes(samples []byte, lengthSize int, hevc bool) keyframeVerdict {
	if lengthSize < 1 || lengthSize > 4 {
		return keyframeVerdict{}
	}
	v := keyframeVerdict{Scanned: true}
	units := 0
	for pos := 0; pos+lengthSize <= len(samples) && units < keyframeScanNALUs; units++ {
		n := 0
		for i := 0; i < lengthSize; i++ {
			n = n<<8 | int(samples[pos+i])
		}
		pos += lengthSize
		if n <= 0 || pos+n > len(samples) {
			// A length that runs past the buffer means the offsets are not what
			// this reader thinks they are; stopping is honest, guessing is not.
			v.Scanned = false
			break
		}
		nal := samples[pos : pos+n]
		pos += n

		if hevc {
			if len(nal) < 2 {
				continue
			}
			t := int(nal[0]>>1) & 0x3F
			if t < nalHEVCFirstVCL || t > nalHEVCLastVCL {
				continue
			}
			irap := t >= nalHEVCFirstIRAP && t <= nalHEVCLastIRAP
			if !v.Known {
				v.Opens, v.Known = irap, true
			}
			if irap {
				v.Present = true
				return v
			}
			continue
		}

		switch nal[0] & 0x1F {
		case nalH264IDR:
			v.Present = true
			if !v.Known {
				v.Opens, v.Known = true, true
			}
			return v
		case nalH264NonIDR, 2, 3, 4:
			if !v.Known {
				v.Opens, v.Known = false, true
			}
		}
	}
	if units >= keyframeScanNALUs {
		v.Scanned = false
	}
	return v
}

// sampleIsNonSync reads sample_is_non_sync_sample out of an ISO-BMFF sample_flags
// word. It is a single bit fifteen places down from the most significant, which is
// easy to place wrongly: sample_depends_on sits at bits 6 and 7 and reads 2 for a
// picture that depends on nothing, so a reader looking at the wrong field still
// gets a plausible answer.
func sampleIsNonSync(flags uint32) bool {
	return flags&0x00010000 != 0
}
