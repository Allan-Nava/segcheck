package media

// Locating a fragment's samples.
//
// A track's samples are not in any header: they are bytes in the mdat, and which
// bytes belong to which track is stated indirectly. The tfhd names a base — either an
// explicit offset or, in every CMAF segment in practice, the first byte of the
// enclosing moof — and each trun states an offset from that base followed by the
// sizes of the samples that begin there.
//
// Everything that reads a sample's contents needs this: a CEA-608 caption track's
// cdat boxes, a subtitle track's TTML documents. Without it a two-track segment can
// only be described from its headers, which is how a caption track carrying data and
// one carrying none looked identical.

// maxSampleWalk bounds how many samples are located per track. A segment with more
// than this is malformed rather than dense, and every caller only needs enough to
// answer a yes-or-no question.
const maxSampleWalk = 8192

// sampleRange is one sample's extent within the segment.
type sampleRange struct {
	start, end int
}

// trackSamples returns each track's sample byte ranges within data, in the order the
// truns declare them.
//
// A range that falls outside the data is dropped rather than clamped: a truncated
// segment should yield the samples that arrived, not a short version of one that did
// not.
func trackSamples(data []byte, defaults map[uint32]fragDefaults) map[uint32][]sampleRange {
	out := map[uint32][]sampleRange{}
	for _, b := range boxesIn(data) {
		if b.typ != "moof" {
			continue
		}
		// The moof's own first byte is the base every offset inside it counts from.
		moofStart := b.start
		for _, traf := range boxesIn(b.payload) {
			if traf.typ != "traf" {
				continue
			}
			trackID, ranges := trafSamples(traf.payload, moofStart, len(data), defaults)
			if trackID == 0 || len(ranges) == 0 {
				continue
			}
			out[trackID] = append(out[trackID], ranges...)
		}
	}
	return out
}

// trafSamples locates one traf's samples.
func trafSamples(traf []byte, moofStart, dataLen int, defaults map[uint32]fragDefaults) (uint32, []sampleRange) {
	tfhd, ok := findBox(traf, "tfhd")
	if !ok || len(tfhd) < 8 {
		return 0, nil
	}
	flags := be32(tfhd[:4]) & 0x00FFFFFF
	trackID := be32(tfhd[4:])

	off := 8
	// base is where this traf's data offsets count from. An explicit
	// base-data-offset wins; otherwise it is the enclosing moof, which is what
	// default-base-is-moof states and what every CMAF segment relies on.
	base := moofStart
	if flags&0x000001 != 0 {
		if len(tfhd) >= off+8 {
			base = int(be64(tfhd[off:]))
		}
		off += 8
	}
	if flags&0x000002 != 0 {
		off += 4 // sample-description-index
	}

	var defaultSize uint32
	if d, ok := defaults[trackID]; ok {
		defaultSize = d.size
	}
	if flags&0x000008 != 0 {
		off += 4 // default-sample-duration
	}
	if flags&0x000010 != 0 {
		if len(tfhd) >= off+4 {
			defaultSize = be32(tfhd[off:])
		}
		off += 4
	}

	var out []sampleRange
	for _, trun := range findBoxes(traf, "trun") {
		out = append(out, trunSamples(trun, base, dataLen, defaultSize, len(out))...)
	}
	return trackID, out
}

// trunSamples walks one trun's sample sizes, turning them into byte ranges.
func trunSamples(trun []byte, base, dataLen int, defaultSize uint32, already int) []sampleRange {
	if len(trun) < 8 {
		return nil
	}
	flags := be32(trun[:4]) & 0x00FFFFFF
	count := int(be32(trun[4:]))
	off := 8

	// data-offset is signed: a fragment may place its samples before the moof it is
	// described by, and reading it unsigned puts them four gigabytes away.
	at := base
	if flags&0x000001 != 0 {
		if len(trun) < off+4 {
			return nil
		}
		at = base + int(int32(be32(trun[off:])))
		off += 4
	}
	if flags&0x000004 != 0 {
		off += 4 // first-sample-flags
	}

	perSample := 0
	sizeAt := -1
	for _, f := range []struct {
		bit  uint32
		size int
	}{
		{0x000100, 4}, // sample-duration
		{0x000200, 4}, // sample-size
		{0x000400, 4}, // sample-flags
		{0x000800, 4}, // sample-composition-time-offset
	} {
		if flags&f.bit == 0 {
			continue
		}
		if f.bit == 0x000200 {
			sizeAt = perSample
		}
		perSample += f.size
	}

	if perSample > 0 {
		if avail := (len(trun) - off) / perSample; count > avail {
			count = avail
		}
	}
	// No sign check: int is 64 bits here, so a 32-bit count cannot come out
	// negative. What bounds a declared count of four billion is the walk below.
	if count > maxSampleWalk {
		count = maxSampleWalk
	}

	var out []sampleRange
	for i := 0; i < count && already+i < maxSampleWalk; i++ {
		size := int(defaultSize)
		if sizeAt >= 0 {
			p := off + i*perSample + sizeAt
			if len(trun) < p+4 {
				break
			}
			size = int(be32(trun[p:]))
		}
		if size <= 0 {
			// A zero-length sample carries nothing to read. It is not an error —
			// an empty subtitle sample is how CMAF says "nothing said here" — but
			// there is no range to hand back.
			continue
		}
		if at < 0 || at+size > dataLen {
			break // past the end of what arrived
		}
		out = append(out, sampleRange{start: at, end: at + size})
		at += size
	}
	return out
}
