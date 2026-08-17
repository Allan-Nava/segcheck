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

// TSWithAAC builds an MPEG-TS segment carrying one AAC audio stream — stream type
// 0x0F — as ADTS frames inside the PES payload.
//
// MPEG-TS states no sampling rate or channel count anywhere in the container:
// both live in the ADTS header of every frame, which is why an MPEG-TS audio
// rendition reported neither until the elementary-stream capture stopped being
// video-only.
// TSMuxed builds a segment carrying video and AAC audio on separate PIDs of one
// program — the shape most HLS transport-stream ladders actually use, and the one
// where the audio format is only readable from the ADTS headers inside the audio
// PID's PES payload.
func TSMuxed(startPTS, frameDur int64, frames int, sps []byte, sampleRate, channels int) []byte {
	const (
		pmtPID   = 0x1000
		videoPID = 0x0100
		audioPID = 0x0101
	)
	var out []byte
	out = append(out, tsPacket(0x0000, true, 0, pat(pmtPID))...)
	out = append(out, tsPacket(pmtPID, true, 0, pmt2(videoPID, 0x1B, audioPID, 0x0F))...)

	es := []byte{0x00, 0x00, 0x00, 0x01, 0x67}
	es = append(es, sps...)
	es = append(es, 0x00, 0x00, 0x00, 0x01, H264IDRSlice)

	vcc, acc := 0, 0
	out = append(out, tsPacket(videoPID, true, vcc, pes(0xE0, startPTS, es))...)
	vcc++
	out = append(out, tsPacket(audioPID, true, acc, pes(0xC0, startPTS, ADTSFrame(sampleRate, channels, 64)))...)
	acc++
	for i := 1; i < frames; i++ {
		pts := startPTS + int64(i)*frameDur
		out = append(out, tsPacket(videoPID, true, vcc&0x0F, pes(0xE0, pts, []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9A}))...)
		vcc++
		out = append(out, tsPacket(audioPID, true, acc&0x0F, pes(0xC0, pts, ADTSFrame(sampleRate, channels, 64)))...)
		acc++
	}
	return out
}

func TSWithAAC(startPTS, frameDur int64, frames int, sampleRate, channels int) []byte {
	const (
		pmtPID   = 0x1000
		audioPID = 0x0101
		aacType  = 0x0F
	)
	var out []byte
	out = append(out, tsPacket(0x0000, true, 0, pat(pmtPID))...)
	out = append(out, tsPacket(pmtPID, true, 0, pmt(audioPID, aacType))...)

	cc := 0
	for i := 0; i < frames; i++ {
		pts := startPTS + int64(i)*frameDur
		out = append(out, tsPacket(audioPID, true, cc, pes(0xC0, pts, ADTSFrame(sampleRate, channels, 64)))...)
		cc = (cc + 1) & 0x0F
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

// pmt2 is pmt with two elementary streams, for a muxed segment.
func pmt2(pid1 uint16, type1 byte, pid2 uint16, type2 byte) []byte {
	sec := []byte{
		0x02,       // table_id
		0xB0, 0x17, // section_length 23: 9 fixed + 2x5 ES + 4 CRC
		0x00, 0x01, // program_number
		0xC1,       // version, current_next
		0x00, 0x00, // section_number, last_section_number
		0xE0 | byte(pid1>>8), byte(pid1 & 0xFF), // PCR_PID
		0xF0, 0x00, // program_info_length 0
		type1,
		0xE0 | byte(pid1>>8), byte(pid1 & 0xFF),
		0xF0, 0x00, // ES_info_length 0
		type2,
		0xE0 | byte(pid2>>8), byte(pid2 & 0xFF),
		0xF0, 0x00, // ES_info_length 0
		0xDE, 0xAD, 0xBE, 0xEF, // CRC32
	}
	return append([]byte{0x00}, sec...)
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
	return MP4InitCodec(trackID, timescale, "hvc1", width, height)
}

// MP4InitCodec is MP4Init with the visual sample entry type chosen by the caller
// — `av01`, `vp09`, `avc1` and so on.
//
// The resolution of every one of them comes from the sample entry rather than
// from a bitstream reader, so what a test built with this asserts is that the
// codec is *recognised as visual*: a type missing from that list reports no
// resolution at all, and the check then skips the rung in silence.
func MP4InitCodec(trackID, timescale uint32, sampleEntryType string, width, height int) []byte {
	return mp4InitWith(trackID, timescale, "video", sampleEntryType, width, height)
}

// MP4InitTrex is MP4Init with an `mvex`/`trex` stating default sample values, the
// way a real on-demand DASH file does. Fragments may then carry no durations at
// all — neither per sample nor in the tfhd — and rely on these.
func MP4InitTrex(trackID, timescale uint32, width, height int, defaultDuration, defaultSize uint32) []byte {
	trex := box("trex", concat(
		u32(0), // version + flags
		u32(trackID),
		u32(1),               // default_sample_description_index
		u32(defaultDuration), // default_sample_duration
		u32(defaultSize),     // default_sample_size
		u32(0),               // default_sample_flags
	))
	return mp4InitWithExtra(trackID, timescale, "video", "avc1", width, height, box("mvex", trex))
}

// MP4SegmentNoDurations is a fragment that states no sample duration anywhere:
// not per sample, not in the tfhd. Everything it runs at comes from the trex in
// the initialisation segment, which is how a large share of real on-demand DASH
// is packaged.
func MP4SegmentNoDurations(trackID, sequence uint32, baseDecodeTime int64, samples, payloadBytes int) []byte {
	mfhd := box("mfhd", concat(u32(0), u32(sequence)))
	tfhd := box("tfhd", concat(u32(0), u32(trackID))) // no flags at all
	tfdt := box("tfdt", concat([]byte{0x01, 0, 0, 0}, u64(uint64(baseDecodeTime))))
	trun := box("trun", concat(u32(0), u32(uint32(samples))))
	traf := box("traf", concat(tfhd, tfdt, trun))
	moof := box("moof", concat(mfhd, traf))
	return concat(box("styp", []byte("msdhmsdhmsix")), moof, box("mdat", make([]byte, payloadBytes)))
}

func mp4InitWith(trackID, timescale uint32, kind, sampleEntryType string, width, height int) []byte {
	return mp4InitWithExtra(trackID, timescale, kind, sampleEntryType, width, height, nil)
}

// mp4InitWithExtra is mp4InitWith plus any additional boxes to place inside moov.
func mp4InitAudioWith(trackID, timescale uint32, sampleEntryType string, channels, sampleRate int) []byte {
	// An audio track states no frame size, so the tkhd carries zeros.
	return mp4InitFrom(trackID, timescale, "soun", audioSampleEntryAt(sampleEntryType, channels, sampleRate), nil, 0, 0)
}

func mp4InitWithExtra(trackID, timescale uint32, kind, sampleEntryType string, width, height int, extra []byte) []byte {
	handler := "vide"
	sampleEntry := visualSampleEntry(sampleEntryType, width, height)
	if kind == "audio" {
		handler = "soun"
		sampleEntry = audioSampleEntry("mp4a")
	}
	return mp4InitFrom(trackID, timescale, handler, sampleEntry, extra, width, height)
}

func mp4InitFrom(trackID, timescale uint32, handler string, sampleEntry, extra []byte, width, height int) []byte {
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
	moov := box("moov", concat(mvhd, trak, extra))
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
	return audioSampleEntryAt(typ, 2, 48000)
}

// audioSampleEntryAt writes an AudioSampleEntry stating a channel count and a
// sampling rate. The rate is 16.16 fixed point, which is the field a reader is
// most likely to take whole and report as 3.1 billion Hz.
func audioSampleEntryAt(typ string, channels, sampleRate int) []byte {
	return box(typ, audioSampleEntryFixed(channels, sampleRate))
}

// audioSampleEntryFixed is the 28 bytes every AudioSampleEntry starts with,
// before whatever codec box follows.
func audioSampleEntryFixed(channels, sampleRate int) []byte {
	return concat(
		make([]byte, 6),    // reserved
		[]byte{0x00, 0x01}, // data_reference_index
		make([]byte, 8),    // reserved
		u16(uint16(channels)),
		u16(16),         // samplesize
		make([]byte, 2), // pre_defined
		make([]byte, 2), // reserved
		u32(uint32(sampleRate)<<16),
	)
}

// MP4InitAudio is an initialisation segment for one audio track stating a channel
// count and a sampling rate, so a test can assert that both are read from the
// container rather than guessed.
func MP4InitAudio(trackID, timescale uint32, sampleEntryType string, channels, sampleRate int) []byte {
	return mp4InitAudioWith(trackID, timescale, sampleEntryType, channels, sampleRate)
}

// ac3Acmod maps a channel count to the (acmod, lfeon) pair AC-3 states it with.
// Only the layouts a test needs are here; anything else panics rather than
// planting a defect the test did not mean to plant.
func ac3Acmod(channels int) (acmod, lfeon int) {
	switch channels {
	case 1:
		return 1, 0 // 1/0
	case 2:
		return 2, 0 // 2/0
	case 6:
		return 7, 1 // 3/2 plus LFE
	case 8:
		return 7, 1 // 3/2 plus LFE, the rest in dependent substreams
	}
	panic("mediatest: unsupported AC-3 channel count")
}

// MP4InitAC3 builds an fMP4 audio init whose AudioSampleEntry claims stereo — the
// way real AC-3 encoders write it — while the dac3 box states the true layout.
// That disagreement is the whole reason the dac3 box has to be read: trusting the
// sample entry reports every 5.1 AC-3 track as stereo.
func MP4InitAC3(trackID, timescale uint32, channels, sampleRate int) []byte {
	acmod, lfeon := ac3Acmod(channels)
	entry := box("ac-3", concat(
		audioSampleEntryFixed(2, sampleRate), // the misleading channelcount
		box("dac3", ac3SpecificBox(fscodFor(sampleRate), acmod, lfeon)),
	))
	return mp4InitFrom(trackID, timescale, "soun", entry, nil, 0, 0)
}

// MP4InitEAC3 is MP4InitAC3 for Enhanced AC-3, whose dec3 box describes one or
// more substreams rather than a single bit stream.
func MP4InitEAC3(trackID, timescale uint32, channels, sampleRate, dependentSubstreams int) []byte {
	acmod, lfeon := ac3Acmod(channels)
	entry := box("ec-3", concat(
		audioSampleEntryFixed(2, sampleRate),
		box("dec3", eac3SpecificBox(fscodFor(sampleRate), acmod, lfeon, dependentSubstreams)),
	))
	return mp4InitFrom(trackID, timescale, "soun", entry, nil, 0, 0)
}

// fscodFor is the sampling_rate_code AC-3 states a rate with.
func fscodFor(sampleRate int) int {
	switch sampleRate {
	case 48000:
		return 0
	case 44100:
		return 1
	case 32000:
		return 2
	}
	return 3 // reserved in AC-3; in E-AC-3 it means a half rate in fscod2
}

// ac3SpecificBox is the three bytes of an AC3SpecificBox: fscod(2) bsid(5)
// bsmod(3) acmod(3) lfeon(1) bit_rate_code(5) reserved(5).
func ac3SpecificBox(fscod, acmod, lfeon int) []byte {
	var w bitWriter
	w.u(2, uint32(fscod))
	w.u(5, 8) // bsid: 8 is standard AC-3
	w.u(3, 0) // bsmod: complete main
	w.u(3, uint32(acmod))
	w.u(1, uint32(lfeon))
	w.u(5, 10) // bit_rate_code: 384 kbps
	w.u(5, 0)  // reserved
	return w.bytes()
}

// eac3SpecificBox is an EC3SpecificBox with one independent substream:
// data_rate(13) num_ind_sub(3), then fscod(2) bsid(5) reserved(1) asvc(1)
// bsmod(3) acmod(3) lfeon(1) reserved(3) num_dep_sub(4) and, only when there are
// dependent substreams, chan_loc(9).
func eac3SpecificBox(fscod, acmod, lfeon, dependentSubstreams int) []byte {
	var w bitWriter
	w.u(13, 192) // data_rate in kbit/s
	w.u(3, 0)    // num_ind_sub: 0 means one independent substream
	w.u(2, uint32(fscod))
	w.u(5, 16) // bsid: 16 is E-AC-3
	w.u(1, 0)  // reserved
	w.u(1, 0)  // asvc
	w.u(3, 0)  // bsmod
	w.u(3, uint32(acmod))
	w.u(1, uint32(lfeon))
	w.u(3, 0) // reserved
	w.u(4, uint32(dependentSubstreams))
	if dependentSubstreams > 0 {
		// chan_loc: the left and right wide pair, which is what takes 5.1 to 7.1.
		w.u(9, 1<<6)
	} else {
		w.u(1, 0) // reserved
	}
	return w.bytes()
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

// SIDXEntry is one reference in a segment index.
type SIDXEntry struct {
	// Size is referenced_size: the byte length of the subsegment.
	Size uint32
	// Duration is subsegment_duration, in the index's timescale.
	Duration uint32
	// StartsWithSAP marks a subsegment a player can begin decoding at.
	StartsWithSAP bool
	// Reference is reference_type: 1 means this entry points at another index
	// rather than at media, which a reader that ignores the bit will follow into
	// the wrong bytes.
	Reference bool
}

// SIDX builds a segment index box.
//
// version 0 states the earliest presentation time and first offset as 32-bit
// fields, version 1 as 64-bit ones — the same widening as tfdt, and the same
// place to lose four bytes and read every reference from the wrong offset.
func SIDX(version byte, timescale uint32, earliest, firstOffset uint64, entries []SIDXEntry) []byte {
	body := []byte{version, 0, 0, 0}
	body = append(body, u32(1)...) // reference_ID
	body = append(body, u32(timescale)...)
	if version == 0 {
		body = append(body, u32(uint32(earliest))...)
		body = append(body, u32(uint32(firstOffset))...)
	} else {
		body = append(body, u64(earliest)...)
		body = append(body, u64(firstOffset)...)
	}
	body = append(body, u16(0)...)                    // reserved
	body = append(body, u16(uint16(len(entries)))...) // reference_count
	for _, e := range entries {
		ref := e.Size & 0x7FFFFFFF
		if e.Reference {
			ref |= 0x80000000
		}
		body = append(body, u32(ref)...)
		body = append(body, u32(e.Duration)...)
		sap := uint32(0)
		if e.StartsWithSAP {
			sap = 0x80000000 | (1 << 28) // starts_with_SAP, SAP_type 1
		}
		body = append(body, u32(sap)...)
	}
	return box("sidx", body)
}

// SingleFileDASH builds the shape a SegmentBase representation has: an ftyp and
// moov, then a sidx describing the subsegments, then the media itself. The
// returned offsets are where the index box sits, which is what @indexRange
// addresses.
func SingleFileDASH(initTrackID, timescale uint32, width, height int, entries []SIDXEntry) (file []byte, indexStart, indexEnd int) {
	head := concat(box("ftyp", []byte("isom\x00\x00\x00\x00isomiso6")), MP4Init(initTrackID, timescale, "video", width, height))
	idx := SIDX(0, timescale, 0, 0, entries)
	indexStart = len(head)
	indexEnd = indexStart + len(idx) - 1

	out := concat(head, idx)
	for i, e := range entries {
		// Each subsegment is a fragment of exactly the size the index promised.
		seg := MP4Segment(initTrackID, uint32(i+1), int64(i)*int64(e.Duration), e.Duration/25, 25, 0)
		if len(seg) < int(e.Size) {
			seg = append(seg, make([]byte, int(e.Size)-len(seg))...)
		}
		out = append(out, seg[:e.Size]...)
	}
	return out, indexStart, indexEnd
}

// HierarchicalSIDX builds a two-level index, which is what real on-demand DASH
// files carry: a top-level index whose references all point at leaf indexes, each
// leaf describing the media subsegments of its portion.
//
// A reader that stops at the top level finds only index references and concludes
// the file describes no media — which is exactly what happens if the recursion is
// missing.
func HierarchicalSIDX(timescale uint32, leaves [][]SIDXEntry) []byte {
	// Each leaf index, and the media that follows it, form one referenced run.
	var leafBlocks [][]byte
	for _, entries := range leaves {
		block := SIDX(0, timescale, 0, 0, entries)
		for _, e := range entries {
			block = append(block, make([]byte, e.Size)...)
		}
		leafBlocks = append(leafBlocks, block)
	}

	top := make([]SIDXEntry, 0, len(leafBlocks))
	for i, b := range leafBlocks {
		var dur uint32
		for _, e := range leaves[i] {
			dur += e.Duration
		}
		top = append(top, SIDXEntry{Size: uint32(len(b)), Duration: dur, Reference: true})
	}

	out := SIDX(0, timescale, 0, 0, top)
	for _, b := range leafBlocks {
		out = append(out, b...)
	}
	return out
}
