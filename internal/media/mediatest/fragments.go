package mediatest

// Fragments whose samples are really where their headers say they are.
//
// Everything that reads a sample's contents — a caption track's cdat boxes, a
// subtitle track's TTML documents — depends on the trun data offset actually pointing
// at the bytes. A builder that wrote a plausible-looking offset would let a reader
// that computed the base wrongly still find something, so this one measures the moof
// it has just built and writes the real distance.

// TrackSamples is one track's contribution to a fragment.
type TrackSamples struct {
	TrackID        uint32
	BaseDecodeTime int64
	SampleDuration uint32
	// Samples are the bytes of each sample, in order. An empty entry is a sample of
	// no length, which is how CMAF says "nothing said here".
	Samples [][]byte
}

// MP4SegmentSamples builds a fragment carrying the tracks given, with each traf's
// trun stating a data offset that genuinely locates that track's samples in the mdat.
func MP4SegmentSamples(sequence uint32, tracks ...TrackSamples) []byte {
	styp := box("styp", concat([]byte("msdh"), u32(0), []byte("msdhmsix")))

	// Where each track's samples begin inside the mdat payload, and the payload
	// itself.
	var payload []byte
	offsets := make([]int, len(tracks))
	for i, t := range tracks {
		offsets[i] = len(payload)
		for _, s := range t.Samples {
			payload = append(payload, s...)
		}
	}

	// The data offset a trun states is measured from the enclosing moof, so it
	// depends on the moof's own size — which depends on the truns. Build it once with
	// placeholder offsets to learn the size, then again with the real ones: the field
	// is fixed-width, so the size does not change.
	moofSize := len(buildMoof(sequence, tracks, offsets, 0))
	dataStart := moofSize + 8 // the mdat header follows the moof

	moof := buildMoof(sequence, tracks, offsets, dataStart)
	return concat(styp, moof, box("mdat", payload))
}

func buildMoof(sequence uint32, tracks []TrackSamples, offsets []int, dataStart int) []byte {
	mfhd := box("mfhd", concat(u32(0), u32(sequence)))
	body := mfhd
	for i, t := range tracks {
		// tfhd flags 0x020008: default-base-is-moof and default-sample-duration.
		tfhd := box("tfhd", concat(u32(0x020008), u32(t.TrackID), u32(t.SampleDuration)))
		tfdt := box("tfdt", concat([]byte{0x01, 0x00, 0x00, 0x00}, u64(uint64(t.BaseDecodeTime))))

		// trun flags 0x000201: data-offset-present and sample-size-present.
		trunBody := concat(u32(0x000201), u32(uint32(len(t.Samples))), u32(uint32(dataStart+offsets[i])))
		for _, s := range t.Samples {
			trunBody = append(trunBody, u32(uint32(len(s)))...)
		}
		body = append(body, box("traf", concat(tfhd, tfdt, box("trun", trunBody)))...)
	}
	return box("moof", body)
}

// CDATSample builds a c608 sample: a cdat box for CEA-608 field 1, or cdt2 for
// field 2, carrying byte pairs the way a caption track does.
func CDATSample(field int, pairs ...[2]byte) []byte {
	typ := "cdat"
	if field == 2 {
		typ = "cdt2"
	}
	var body []byte
	for _, p := range pairs {
		body = append(body, p[0], p[1])
	}
	return box(typ, body)
}

// VTTCSample builds a wvtt sample: a vttc box holding one cue's payload. An empty
// sample — a vtte box — is how the format says nothing is displayed.
func VTTCSample(text string) []byte {
	return box("vttc", box("payl", []byte(text)))
}

// VTTESample builds the empty-cue box, which carries no cue at all.
func VTTESample() []byte {
	return box("vtte", nil)
}
