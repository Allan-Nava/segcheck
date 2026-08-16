package media

import (
	"encoding/binary"
	"fmt"
)

// ISO-BMFF / CMAF parsing.
//
// A fragmented-MP4 media segment carries timestamps but says nothing about
// what the media is: the timescale, codec and resolution live in the
// initialisation segment (EXT-X-MAP for HLS, the SegmentTemplate initialisation
// for DASH). Parse both together, and the media fragment's tfdt/trun become a
// real timeline in seconds.

// initTrack is what the initialisation segment tells us about one track.
type initTrack struct {
	id        uint32
	kind      TrackKind
	codec     string
	width     int
	height    int
	timescale uint32
	encrypted bool
	// trexDuration and trexSize are the movie-extends defaults. A fragment may
	// state no sample duration at all — not per sample, not in its tfhd — and rely
	// on these, which is how a large share of on-demand DASH is packaged. Ignoring
	// them makes every sample zero ticks long, so the segment reports a zero
	// duration and every boundary after it looks like a gap.
	trexDuration uint32
	trexSize     uint32
}

// fragTrack is what the media fragments tell us about one track.
type fragTrack struct {
	id          uint32
	baseDecode  int64
	haveBase    bool
	samples     int
	sumDuration int64
	sumSize     int64
	minCTOffset int64
	haveCT      bool
	sequence    uint32
	// firstFlags is the sample_flags word of the fragment's first sample, from
	// trun's first-sample-flags, else its per-sample flags, else the tfhd default.
	// A fragment need carry none of them, which is why haveFirstFlags exists: the
	// absence of the flag is not the absence of a keyframe.
	firstFlags     uint32
	haveFirstFlags bool
}

// noteFirstFlags records the sample_flags of the fragment's first sample, and
// only the first: several traf boxes may describe one track, and a later
// fragment's flags say nothing about where this segment opens.
func (f *fragTrack) noteFirstFlags(flags uint32) {
	if !f.haveFirstFlags {
		f.firstFlags, f.haveFirstFlags = flags, true
	}
}

// ParseMP4 parses a fragmented MP4 media segment, using init (which may be nil,
// or may itself be the whole thing when only an init segment is given).
func ParseMP4(data, init []byte) (SegmentInfo, error) {
	inits := map[uint32]*initTrack{}
	// The init segment may arrive separately or be prepended to the media data;
	// reading moov from both covers each case, and a self-initialising segment.
	for _, src := range [][]byte{init, data} {
		if len(src) == 0 {
			continue
		}
		if moov, ok := findBox(src, "moov"); ok {
			parseMoov(moov, inits)
		}
	}

	// The movie-extends defaults have to be known before the fragments are read,
	// because a fragment that states nothing inherits from them.
	defaults := map[uint32]fragDefaults{}
	for id, it := range inits {
		defaults[id] = fragDefaults{duration: it.trexDuration, size: it.trexSize}
	}
	frags, sequence := parseMoofs(data, defaults)

	info := SegmentInfo{Container: ContainerMP4, Bytes: int64(len(data)), Sequence: sequence}

	// Emit one track per fragment track, enriched with the init metadata. When
	// there are no fragments at all (an init segment on its own) fall back to
	// describing the tracks the init declares.
	if len(frags) == 0 {
		if len(inits) == 0 {
			return info, ErrUnknownContainer
		}
		for _, id := range sortedInitIDs(inits) {
			info.Tracks = append(info.Tracks, inits[id].track())
		}
		return info, nil
	}

	for _, id := range sortedFragIDs(frags) {
		f := frags[id]
		var t Track
		if it, ok := inits[id]; ok {
			t = it.track()
		} else if len(inits) == 1 && len(frags) == 1 {
			// A single-track fragment whose track_ID disagrees with the init
			// segment: trust the pairing over the identifier.
			for _, it := range inits {
				t = it.track()
			}
		} else {
			t = Track{ID: id, Kind: Other, Timescale: 0}
		}
		t.ID = id
		t.Samples = f.samples
		t.StatedDur = f.sumDuration
		if f.haveFirstFlags {
			// The flag describes the first sample only, so it settles whether the
			// fragment opens on a sync sample without saying whether a later sample
			// is one. KeyframeScanned stays false for exactly that reason.
			sync := !sampleIsNonSync(f.firstFlags)
			t.OpensOnKeyframe, t.HasKeyframe, t.KeyframeKnown = sync, sync, true
		}
		if f.samples > 0 && f.sumDuration > 0 {
			t.FrameDur = f.sumDuration / int64(f.samples)
		}
		if f.haveBase {
			start := f.baseDecode
			if f.haveCT {
				// Composition offsets shift presentation relative to decode;
				// the smallest one is where presentation actually begins.
				start += f.minCTOffset
			}
			t.HasPTS = true
			t.MinPTS = start
			t.MaxPTS = start + f.sumDuration
			if t.MaxPTS > t.MinPTS && t.FrameDur > 0 {
				// MaxPTS is the end of the interval; keep it as the last
				// timestamp so it means the same thing as in the TS parser.
				t.MaxPTS -= t.FrameDur
			}
		}
		info.Tracks = append(info.Tracks, t)
	}
	if len(info.Tracks) == 0 {
		return info, fmt.Errorf("ISO-BMFF with no track")
	}
	return info, nil
}

func (it *initTrack) track() Track {
	return Track{
		ID:        it.id,
		Kind:      it.kind,
		Codec:     it.codec,
		Width:     it.width,
		Height:    it.height,
		Timescale: it.timescale,
		Encrypted: it.encrypted,
	}
}

// ---------- init segment (moov) ----------

func parseMoov(moov []byte, out map[uint32]*initTrack) {
	// A pssh box anywhere in moov means the content is protected even if the
	// sample entries were not rewritten to encv/enca.
	_, hasPSSH := findBox(moov, "pssh")

	// mvex/trex states what a fragment may omit. There is one trex per track.
	trex := map[uint32]fragDefaults{}
	if mvex, ok := findBox(moov, "mvex"); ok {
		for _, tx := range findBoxes(mvex, "trex") {
			if len(tx) < 20 {
				continue
			}
			trex[be32(tx[4:])] = fragDefaults{duration: be32(tx[12:]), size: be32(tx[16:])}
		}
	}

	for _, trak := range findBoxes(moov, "trak") {
		t := &initTrack{}
		if tkhd, ok := findBox(trak, "tkhd"); ok {
			t.id, t.width, t.height = parseTkhd(tkhd)
		}
		mdia, ok := findBox(trak, "mdia")
		if !ok {
			continue
		}
		if mdhd, ok := findBox(mdia, "mdhd"); ok {
			t.timescale = parseMdhd(mdhd)
		}
		if hdlr, ok := findBox(mdia, "hdlr"); ok {
			t.kind = parseHdlr(hdlr)
		}
		if minf, ok := findBox(mdia, "minf"); ok {
			if stbl, ok := findBox(minf, "stbl"); ok {
				if stsd, ok := findBox(stbl, "stsd"); ok {
					codec, w, h, enc := parseStsd(stsd)
					t.codec = codec
					if w > 0 && h > 0 {
						// The sample entry is the coded size; tkhd is a display
						// size that a packager may have left at a stale value.
						t.width, t.height = w, h
					}
					t.encrypted = enc
				}
			}
		}
		if hasPSSH {
			t.encrypted = true
		}
		if t.id == 0 {
			t.id = uint32(len(out) + 1)
		}
		if d, ok := trex[t.id]; ok {
			t.trexDuration, t.trexSize = d.duration, d.size
		}
		out[t.id] = t
	}
}

func parseTkhd(b []byte) (id uint32, width, height int) {
	if len(b) < 4 {
		return 0, 0, 0
	}
	version := b[0]
	idOff := 12
	if version == 1 {
		idOff = 20
	}
	if len(b) >= idOff+4 {
		id = be32(b[idOff:])
	}
	// width and height are 16.16 fixed point in the last 8 bytes.
	if len(b) >= 8 {
		w := int(be32(b[len(b)-8:]) >> 16)
		h := int(be32(b[len(b)-4:]) >> 16)
		// A malformed box yields a number rather than a failure, so it is bounded
		// here the way the bitstream readers bound theirs: unknown beats wrong.
		if plausibleResolution(w, h) {
			width, height = w, h
		}
	}
	return id, width, height
}

func parseMdhd(b []byte) uint32 {
	if len(b) < 4 {
		return 0
	}
	off := 12
	if b[0] == 1 {
		off = 20
	}
	if len(b) < off+4 {
		return 0
	}
	return be32(b[off:])
}

func parseHdlr(b []byte) TrackKind {
	if len(b) < 12 {
		return Other
	}
	switch string(b[8:12]) {
	case "vide":
		return Video
	case "soun":
		return Audio
	default:
		return Other
	}
}

// The fixed fields every sample entry starts with, before any child box. Both
// begin with the 8-byte SampleEntry (6 reserved plus data_reference_index);
// VisualSampleEntry adds the pre_defined/reserved block, width, height,
// resolutions, frame count, compressor name and depth, and AudioSampleEntry the
// channel count, sample size and sample rate.
const (
	visualSampleEntrySize = 78
	audioSampleEntrySize  = 28
)

// parseStsd reads the first sample entry: its type is the codec, and for video
// its width/height are the coded resolution.
func parseStsd(b []byte) (codec string, width, height int, encrypted bool) {
	if len(b) < 8 {
		return "", 0, 0, false
	}
	entries := boxesIn(b[8:])
	if len(entries) == 0 {
		return "", 0, 0, false
	}
	e := entries[0]
	typ := e.typ
	if typ == "encv" || typ == "enca" {
		encrypted = true
		// sinf/frma names the original format the encryption replaced. Recovering
		// it matters twice over: left as "encv" the tracks check compares the
		// manifest's declared codec against it and reports a mismatch on media
		// that is correct, and the resolution is never read because "encv" is not
		// a visual sample entry type.
		//
		// The child boxes follow the sample entry's fixed fields, and those are
		// not boxes: starting the search at byte 0 reads the leading reserved
		// zeros as a box of declared size 0, which swallows the entry whole.
		prefix := visualSampleEntrySize
		if typ == "enca" {
			prefix = audioSampleEntrySize
		}
		if len(e.payload) > prefix {
			if sinf, ok := findBox(e.payload[prefix:], "sinf"); ok {
				if frma, ok := findBox(sinf, "frma"); ok && len(frma) >= 4 {
					typ = string(frma[:4])
				}
			}
		}
	}
	codec = mp4Codec(typ)
	if isVisualSampleEntry(typ) && len(e.payload) >= 28 {
		w := int(be16(e.payload[24:]))
		h := int(be16(e.payload[26:]))
		if plausibleResolution(w, h) {
			width, height = w, h
		}
	}
	return codec, width, height, encrypted
}

func isVisualSampleEntry(typ string) bool {
	switch typ {
	case "avc1", "avc3", "hvc1", "hev1", "vvc1", "vvi1", "vp08", "vp09", "av01", "dvh1", "dvhe", "mp4v":
		return true
	}
	return false
}

func mp4Codec(typ string) string {
	switch typ {
	case "avc1", "avc3":
		return "h264"
	case "hvc1", "hev1":
		return "hevc"
	case "dvh1", "dvhe":
		return "dolbyvision"
	case "vvc1", "vvi1":
		return "vvc"
	case "vp08":
		return "vp8"
	case "vp09":
		return "vp9"
	case "av01":
		return "av1"
	case "mp4v":
		return "mpeg4video"
	case "mp4a":
		return "aac"
	case "ac-3":
		return "ac3"
	case "ec-3":
		return "eac3"
	case "ac-4":
		return "ac4"
	case "Opus", "opus":
		return "opus"
	case "fLaC":
		return "flac"
	case "alac":
		return "alac"
	case "dtsc", "dtse", "dtsh", "dtsl":
		return "dts"
	default:
		return typ
	}
}

// ---------- media fragments (moof) ----------

// fragDefaults is what the initialisation segment says a fragment may leave out.
type fragDefaults struct {
	duration uint32
	size     uint32
}

func parseMoofs(data []byte, defaults map[uint32]fragDefaults) (map[uint32]*fragTrack, uint32) {
	out := map[uint32]*fragTrack{}
	var sequence uint32
	for _, moof := range findBoxes(data, "moof") {
		if mfhd, ok := findBox(moof, "mfhd"); ok && len(mfhd) >= 8 {
			if sequence == 0 {
				sequence = be32(mfhd[4:])
			}
		}
		for _, traf := range findBoxes(moof, "traf") {
			parseTraf(traf, out, defaults)
		}
	}
	return out, sequence
}

func parseTraf(traf []byte, out map[uint32]*fragTrack, defaults map[uint32]fragDefaults) {
	tfhd, ok := findBox(traf, "tfhd")
	if !ok || len(tfhd) < 8 {
		return
	}
	flags := be32(tfhd[:4]) & 0x00FFFFFF
	trackID := be32(tfhd[4:])
	off := 8
	if flags&0x000001 != 0 {
		off += 8 // base-data-offset
	}
	if flags&0x000002 != 0 {
		off += 4 // sample-description-index
	}
	// The trex defaults are the floor: whatever the tfhd states overrides them,
	// and whatever a trun states overrides that.
	var defaultDuration, defaultSize uint32
	if d, ok := defaults[trackID]; ok {
		defaultDuration, defaultSize = d.duration, d.size
	}
	if flags&0x000008 != 0 {
		if len(tfhd) >= off+4 {
			defaultDuration = be32(tfhd[off:])
		}
		off += 4
	}
	if flags&0x000010 != 0 {
		if len(tfhd) >= off+4 {
			defaultSize = be32(tfhd[off:])
		}
		off += 4
	}
	var defaultFlags uint32
	haveDefaultFlags := false
	if flags&0x000020 != 0 {
		if len(tfhd) >= off+4 {
			defaultFlags, haveDefaultFlags = be32(tfhd[off:]), true
		}
		off += 4
	}

	f := out[trackID]
	if f == nil {
		f = &fragTrack{id: trackID}
		out[trackID] = f
	}

	if tfdt, ok := findBox(traf, "tfdt"); ok && len(tfdt) >= 8 {
		var base int64
		if tfdt[0] == 1 {
			if len(tfdt) >= 12 {
				base = int64(be64(tfdt[4:]))
			}
		} else {
			base = int64(be32(tfdt[4:]))
		}
		// Several traf boxes for one track: the earliest decode time is the
		// start of the segment.
		if !f.haveBase || base < f.baseDecode {
			f.baseDecode, f.haveBase = base, true
		}
	}

	if _, ok := findBox(traf, "senc"); ok {
		// Sample-level encryption present; the init segment usually says so too.
		if it := out[trackID]; it != nil {
			_ = it // encryption is reported from the init segment's sample entry
		}
	}

	for _, trun := range findBoxes(traf, "trun") {
		parseTrun(trun, f, defaultDuration, defaultSize)
	}

	// The tfhd default is the last word on the first sample's flags: a trun that
	// states them outright has already been read, and this must not overwrite it.
	if haveDefaultFlags {
		f.noteFirstFlags(defaultFlags)
	}
}

func parseTrun(trun []byte, f *fragTrack, defaultDuration, defaultSize uint32) {
	if len(trun) < 8 {
		return
	}
	version := trun[0]
	flags := be32(trun[:4]) & 0x00FFFFFF
	count := int(be32(trun[4:]))
	off := 8
	if flags&0x000001 != 0 {
		off += 4 // data-offset
	}
	if flags&0x000004 != 0 {
		// first-sample-flags exists precisely to say that the fragment opens on a
		// sync sample while the rest of its samples do not, so it is the most
		// direct answer to SC-16's question.
		if len(trun) >= off+4 {
			f.noteFirstFlags(be32(trun[off:]))
		}
		off += 4
	}

	perSample := 0
	if flags&0x000100 != 0 {
		perSample += 4
	}
	if flags&0x000200 != 0 {
		perSample += 4
	}
	if flags&0x000400 != 0 {
		perSample += 4
	}
	if flags&0x000800 != 0 {
		perSample += 4
	}

	// A count that does not fit the box is a malformed segment; trust the bytes
	// present rather than the declared count.
	if perSample > 0 {
		if avail := (len(trun) - off) / perSample; count > avail {
			count = avail
		}
	}
	if count < 0 {
		return
	}
	f.samples += count

	if perSample == 0 {
		// Everything comes from the tfhd defaults.
		f.sumDuration += int64(defaultDuration) * int64(count)
		f.sumSize += int64(defaultSize) * int64(count)
		return
	}

	for i := 0; i < count; i++ {
		p := off + i*perSample
		cur := p
		if flags&0x000100 != 0 {
			f.sumDuration += int64(be32(trun[cur:]))
			cur += 4
		} else {
			f.sumDuration += int64(defaultDuration)
		}
		if flags&0x000200 != 0 {
			f.sumSize += int64(be32(trun[cur:]))
			cur += 4
		} else {
			f.sumSize += int64(defaultSize)
		}
		if flags&0x000400 != 0 {
			// Per-sample flags. Only sample 0 answers where the segment opens, and
			// first-sample-flags takes precedence if it was present — noteFirstFlags
			// keeps whichever arrived first, and that read happens above.
			if i == 0 && len(trun) >= cur+4 {
				f.noteFirstFlags(be32(trun[cur:]))
			}
			cur += 4 // sample-flags
		}
		if flags&0x000800 != 0 {
			var ct int64
			if version == 0 {
				ct = int64(be32(trun[cur:]))
			} else {
				ct = int64(int32(be32(trun[cur:])))
			}
			if !f.haveCT || ct < f.minCTOffset {
				f.minCTOffset, f.haveCT = ct, true
			}
		}
	}
}

// ---------- box plumbing ----------

type mp4box struct {
	typ     string
	payload []byte
}

// boxesIn iterates the boxes at one level. A box whose declared size does not
// fit stops the walk: the bytes after it cannot be trusted to be boxes.
func boxesIn(data []byte) []mp4box {
	var out []mp4box
	for off := 0; off+8 <= len(data); {
		size := int(be32(data[off:]))
		typ := string(data[off+4 : off+8])
		header := 8
		switch size {
		case 1:
			if off+16 > len(data) {
				return out
			}
			size = int(be64(data[off+8:]))
			header = 16
		case 0:
			size = len(data) - off // extends to the end
		}
		if size < header || off+size > len(data) {
			return out
		}
		out = append(out, mp4box{typ: typ, payload: data[off+header : off+size]})
		off += size
	}
	return out
}

// findBox returns the payload of the first box of type typ at this level.
func findBox(data []byte, typ string) ([]byte, bool) {
	for _, b := range boxesIn(data) {
		if b.typ == typ {
			return b.payload, true
		}
	}
	return nil, false
}

// findBoxes returns the payloads of every box of type typ at this level.
func findBoxes(data []byte, typ string) [][]byte {
	var out [][]byte
	for _, b := range boxesIn(data) {
		if b.typ == typ {
			out = append(out, b.payload)
		}
	}
	return out
}

func be16(b []byte) uint16 {
	if len(b) < 2 {
		return 0
	}
	return binary.BigEndian.Uint16(b)
}

func be32(b []byte) uint32 {
	if len(b) < 4 {
		return 0
	}
	return binary.BigEndian.Uint32(b)
}

func be64(b []byte) uint64 {
	if len(b) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}

func sortedInitIDs(m map[uint32]*initTrack) []uint32 {
	ids := make([]uint32, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sortUint32(ids)
	return ids
}

func sortedFragIDs(m map[uint32]*fragTrack) []uint32 {
	ids := make([]uint32, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sortUint32(ids)
	return ids
}

func sortUint32(a []uint32) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}
