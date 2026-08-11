// Package mediatest builds synthetic MPEG-TS and fragmented-MP4 segments with
// known timestamps, so the parsers and the analyses can be tested against media
// whose correct answer is known by construction — no fixture binaries in the
// repository, and no network.
package mediatest

import "encoding/binary"

// TS builds an MPEG-TS segment carrying one H.264 video stream whose
// presentation timestamps start at startPTS (90kHz ticks) and advance by
// frameDur for frames frames.
func TS(startPTS, frameDur int64, frames int) []byte {
	const (
		pmtPID   = 0x1000
		videoPID = 0x0100
	)
	var out []byte
	out = append(out, tsPacket(0x0000, true, 0, pat(pmtPID))...)
	out = append(out, tsPacket(pmtPID, true, 0, pmt(videoPID, 0x1B))...)

	cc := 0
	for i := 0; i < frames; i++ {
		pts := startPTS + int64(i)*frameDur
		// A small payload per frame keeps the segment readable; the parser only
		// needs one PES header per frame to recover the timestamp.
		es := make([]byte, 64)
		for j := range es {
			es[j] = byte(j)
		}
		out = append(out, tsPacket(videoPID, true, cc, pes(0xE0, pts, es))...)
		cc = (cc + 1) & 0x0F
	}
	return out
}

// H.264 NAL header bytes for the opening slice. nal_ref_idc is 3 in both; only
// the low five bits, the nal_unit_type, differ.
const (
	// H264IDRSlice opens a segment on an instantaneous decoder refresh, which is
	// what makes the segment switchable into.
	H264IDRSlice = byte(0x65) // nal_unit_type 5
	// H264NonIDRSlice opens it on an ordinary slice, which is not.
	H264NonIDRSlice = byte(0x41) // nal_unit_type 1
)

// TSWithSPS is TS plus an H.264 SPS ahead of the first frame, so the parser can
// recover the coded resolution. sps is the raw SPS NAL payload (without the
// start code and without the NAL header byte). The segment opens on an IDR, as a
// well-formed one does.
func TSWithSPS(startPTS, frameDur int64, frames int, sps []byte) []byte {
	return TSWithSPSOpening(startPTS, frameDur, frames, sps, H264IDRSlice)
}

// TSWithSPSOpening is TSWithSPS with the opening slice's NAL header chosen by the
// caller, so a test can plant a segment that opens on a non-keyframe — the defect
// behind "ABR switching stutters even though the boundaries line up".
func TSWithSPSOpening(startPTS, frameDur int64, frames int, sps []byte, firstSlice byte) []byte {
	const (
		pmtPID   = 0x1000
		videoPID = 0x0100
	)
	var out []byte
	out = append(out, tsPacket(0x0000, true, 0, pat(pmtPID))...)
	out = append(out, tsPacket(pmtPID, true, 0, pmt(videoPID, 0x1B))...)

	// Annex-B: start code, NAL header (type 7 = SPS), then the SPS bytes.
	es := []byte{0x00, 0x00, 0x00, 0x01, 0x67}
	es = append(es, sps...)
	es = append(es, 0x00, 0x00, 0x00, 0x01, firstSlice)

	cc := 0
	out = append(out, tsPacket(videoPID, true, cc, pes(0xE0, startPTS, es))...)
	cc++
	for i := 1; i < frames; i++ {
		pts := startPTS + int64(i)*frameDur
		out = append(out, tsPacket(videoPID, true, cc&0x0F, pes(0xE0, pts, []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9A}))...)
		cc++
	}
	return out
}

// TSDropPacket is TS with one continuity counter deliberately skipped, which is
// what packet loss between the packager and the client looks like.
func TSDropPacket(startPTS, frameDur int64, frames int) []byte {
	const (
		pmtPID   = 0x1000
		videoPID = 0x0100
	)
	var out []byte
	out = append(out, tsPacket(0x0000, true, 0, pat(pmtPID))...)
	out = append(out, tsPacket(pmtPID, true, 0, pmt(videoPID, 0x1B))...)

	cc := 0
	for i := 0; i < frames; i++ {
		pts := startPTS + int64(i)*frameDur
		if i == 2 {
			cc = (cc + 2) & 0x0F // skip one counter value: a lost packet
		}
		out = append(out, tsPacket(videoPID, true, cc, pes(0xE0, pts, []byte{0x01, 0x02, 0x03, 0x04}))...)
		cc = (cc + 1) & 0x0F
	}
	return out
}

// ---------- MPEG-TS building blocks ----------

// tsPacket wraps a payload in one 188-byte transport packet, stuffing the rest
// with an adaptation field.
func tsPacket(pid uint16, pusi bool, cc int, payload []byte) []byte {
	if len(payload) > 184 {
		payload = payload[:184]
	}
	pkt := make([]byte, 0, 188)
	b1 := byte(pid>>8) & 0x1F
	if pusi {
		b1 |= 0x40
	}
	afc := byte(0x01) // payload only
	stuffing := 184 - len(payload)
	if stuffing > 0 {
		afc = 0x03 // adaptation field + payload
	}
	pkt = append(pkt, 0x47, b1, byte(pid&0xFF), afc<<4|byte(cc&0x0F))
	if stuffing > 0 {
		afLen := stuffing - 1
		pkt = append(pkt, byte(afLen))
		if afLen > 0 {
			pkt = append(pkt, 0x00) // no adaptation flags set
			for i := 0; i < afLen-1; i++ {
				pkt = append(pkt, 0xFF)
			}
		}
	}
	pkt = append(pkt, payload...)
	return pkt
}

func pat(pmtPID uint16) []byte {
	sec := []byte{
		0x00,       // table_id
		0xB0, 0x0D, // section_syntax_indicator + section_length 13
		0x00, 0x01, // transport_stream_id
		0xC1,       // version, current_next
		0x00, 0x00, // section_number, last_section_number
		0x00, 0x01, // program_number 1
		0xE0 | byte(pmtPID>>8), byte(pmtPID & 0xFF),
		0xDE, 0xAD, 0xBE, 0xEF, // CRC32 (not verified)
	}
	return append([]byte{0x00}, sec...) // pointer_field
}

func pmt(esPID uint16, streamType byte) []byte {
	sec := []byte{
		0x02,       // table_id
		0xB0, 0x12, // section_length 18
		0x00, 0x01, // program_number
		0xC1,       // version, current_next
		0x00, 0x00, // section_number, last_section_number
		0xE0 | byte(esPID>>8), byte(esPID & 0xFF), // PCR_PID
		0xF0, 0x00, // program_info_length 0
		streamType,
		0xE0 | byte(esPID>>8), byte(esPID & 0xFF),
		0xF0, 0x00, // ES_info_length 0
		0xDE, 0xAD, 0xBE, 0xEF, // CRC32
	}
	return append([]byte{0x00}, sec...)
}

// pes builds a PES packet with a PTS and the given elementary-stream bytes.
func pes(streamID byte, pts int64, es []byte) []byte {
	header := []byte{
		0x00, 0x00, 0x01, streamID,
		0x00, 0x00, // PES_packet_length: 0 = unbounded, legal for video
		0x80, // marker bits
		0x80, // PTS_DTS_flags = PTS only
		0x05, // PES_header_data_length
	}
	header = append(header, encodePTS(pts)...)
	return append(header, es...)
}

// encodePTS writes a 33-bit timestamp in the five-byte form with its marker
// bits, the exact inverse of what the parser decodes.
func encodePTS(pts int64) []byte {
	return []byte{
		0x20 | byte(((pts>>30)&0x07)<<1) | 0x01,
		byte((pts >> 22) & 0xFF),
		byte(((pts>>15)&0x7F)<<1) | 0x01,
		byte((pts >> 7) & 0xFF),
		byte((pts&0x7F)<<1) | 0x01,
	}
}

// ---------- fragmented MP4 building blocks ----------

// MP4Init builds an initialisation segment for one track.
func MP4Init(trackID, timescale uint32, kind string, width, height int) []byte {
	return mp4InitWith(trackID, timescale, kind, "avc1", width, height)
}

// MP4InitHEVC is MP4Init with an `hvc1` visual sample entry, so a test can
// assert that fMP4 HEVC reports its resolution from the container rather than
// needing the bitstream reader MPEG-TS does.
func MP4InitHEVC(trackID, timescale uint32, width, height int) []byte {
	return mp4InitWith(trackID, timescale, "video", "hvc1", width, height)
}

func mp4InitWith(trackID, timescale uint32, kind, sampleEntryType string, width, height int) []byte {
	handler := "vide"
	sampleEntry := visualSampleEntry(sampleEntryType, width, height)
	if kind == "audio" {
		handler = "soun"
		sampleEntry = audioSampleEntry("mp4a")
	}
	stsd := box("stsd", concat(
		u32(0), // version + flags
		u32(1), // entry_count
		sampleEntry,
	))
	stbl := box("stbl", stsd)
	minf := box("minf", stbl)
	mdhd := box("mdhd", concat(
		u32(0),         // version + flags
		u32(0), u32(0), // creation, modification
		u32(timescale),
		u32(0),             // duration
		[]byte{0x55, 0xC4}, // language "und"
		[]byte{0x00, 0x00}, // pre_defined
	))
	hdlr := box("hdlr", concat(
		u32(0),
		u32(0),
		[]byte(handler),
		make([]byte, 12),
		[]byte{0x00},
	))
	mdia := box("mdia", concat(mdhd, hdlr, minf))
	tkhd := box("tkhd", concat(
		u32(0),         // version + flags
		u32(0), u32(0), // creation, modification
		u32(trackID),
		u32(0),           // reserved
		u32(0),           // duration
		make([]byte, 8),  // reserved
		make([]byte, 2),  // layer
		make([]byte, 2),  // alternate_group
		make([]byte, 2),  // volume
		make([]byte, 2),  // reserved
		make([]byte, 36), // matrix
		u32(uint32(width)<<16),
		u32(uint32(height)<<16),
	))
	trak := box("trak", concat(tkhd, mdia))
	mvhd := box("mvhd", concat(u32(0), make([]byte, 96)))
	moov := box("moov", concat(mvhd, trak))
	ftyp := box("ftyp", concat([]byte("isom"), u32(0), []byte("isomiso5")))
	return concat(ftyp, moov)
}

// MP4Segment builds a media fragment whose decode time starts at baseDecodeTime
// and which carries samples samples of sampleDuration each, in timescale units.
func MP4Segment(trackID uint32, sequence uint32, baseDecodeTime int64, sampleDuration uint32, samples int, payloadBytes int) []byte {
	mfhd := box("mfhd", concat(u32(0), u32(sequence)))
	// tfhd flags 0x000008: default-sample-duration-present.
	tfhd := box("tfhd", concat(u32(0x000008), u32(trackID), u32(sampleDuration)))
	tfdt := box("tfdt", concat(
		[]byte{0x01, 0x00, 0x00, 0x00}, // version 1
		u64(uint64(baseDecodeTime)),
	))
	// trun flags 0: sample count only, durations come from the tfhd default.
	trun := box("trun", concat(u32(0), u32(uint32(samples))))
	traf := box("traf", concat(tfhd, tfdt, trun))
	moof := box("moof", concat(mfhd, traf))
	mdat := box("mdat", make([]byte, payloadBytes))
	styp := box("styp", concat([]byte("msdh"), u32(0), []byte("msdhmsix")))
	return concat(styp, moof, mdat)
}

// MP4SegmentSync is MP4Segment that states, in trun's first-sample-flags,
// whether the segment's first sample is a sync sample.
//
// MP4Segment states nothing either way, which is the honest "not verifiable"
// case: an fMP4 fragment need not carry the flag at all, and a reader must not
// take its absence for a defect. This builder is the one that lets a test plant
// a fragment which says outright that it opens on a non-sync sample.
func MP4SegmentSync(trackID, sequence uint32, baseDecodeTime int64, sampleDuration uint32, samples, payloadBytes int, sync bool) []byte {
	mfhd := box("mfhd", concat(u32(0), u32(sequence)))
	tfhd := box("tfhd", concat(u32(0x000008), u32(trackID), u32(sampleDuration)))
	tfdt := box("tfdt", concat(
		[]byte{0x01, 0x00, 0x00, 0x00},
		u64(uint64(baseDecodeTime)),
	))
	// trun flags 0x000004: first-sample-flags-present. In a sample_flags word
	// sample_is_non_sync_sample is bit 15 counting from the most significant, and
	// sample_depends_on is bits 6 and 7 — 2 meaning "depends on nothing", which is
	// what an I-frame is.
	flags := uint32(0x02000000) // sample_depends_on = 2
	if !sync {
		flags = 0x01000000 | 0x00010000 // depends on others, and not a sync sample
	}
	trun := box("trun", concat(u32(0x000004), u32(uint32(samples)), u32(flags)))
	traf := box("traf", concat(tfhd, tfdt, trun))
	moof := box("moof", concat(mfhd, traf))
	mdat := box("mdat", make([]byte, payloadBytes))
	styp := box("styp", concat([]byte("msdh"), u32(0), []byte("msdhmsix")))
	return concat(styp, moof, mdat)
}

func visualSampleEntry(typ string, width, height int) []byte {
	return box(typ, concat(
		make([]byte, 6),    // reserved
		[]byte{0x00, 0x01}, // data_reference_index
		make([]byte, 2),    // pre_defined
		make([]byte, 2),    // reserved
		make([]byte, 12),   // pre_defined
		u16(uint16(width)),
		u16(uint16(height)),
		u32(0x00480000),  // horizresolution 72dpi
		u32(0x00480000),  // vertresolution
		u32(0),           // reserved
		u16(1),           // frame_count
		make([]byte, 32), // compressorname
		u16(0x0018),      // depth
		[]byte{0xFF, 0xFF},
	))
}

func audioSampleEntry(typ string) []byte {
	return box(typ, concat(
		make([]byte, 6),
		[]byte{0x00, 0x01},
		make([]byte, 8),
		u16(2),  // channelcount
		u16(16), // samplesize
		make([]byte, 2),
		make([]byte, 2),
		u32(48000<<16),
	))
}

// ---------- byte helpers ----------

func box(typ string, payload []byte) []byte {
	out := make([]byte, 0, 8+len(payload))
	out = append(out, u32(uint32(8+len(payload)))...)
	out = append(out, []byte(typ)...)
	return append(out, payload...)
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func u16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func u64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}
