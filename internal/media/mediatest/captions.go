package mediatest

// Closed-caption builders.
//
// CEA-608 and CEA-708 captions ride inside the video elementary stream rather
// than in a track of their own: an SEI message of type 4,
// user_data_registered_itu_t_t35, carrying the ATSC A/53 "GA94" user data whose
// type code 0x03 is a cc_data() payload. Every builder here plants that structure
// so a reader can be asserted against captions it knows are present — and, more
// importantly, against a segment that declares captions and carries none.

// CC packet types, as cc_type is coded in cc_data().
const (
	// CCTypeField1 and CCTypeField2 are the two fields of a CEA-608 line-21
	// waveform. CC1 and CC3 share field 1, CC2 and CC4 share field 2.
	CCTypeField1 = 0
	CCTypeField2 = 1
	// CCTypeDTVCCData continues a CEA-708 DTVCC packet, CCTypeDTVCCStart begins
	// one.
	CCTypeDTVCCData  = 2
	CCTypeDTVCCStart = 3
)

// CCPacket is one cc_data_pkt triplet: a validity bit, a type and two bytes.
type CCPacket struct {
	Valid bool
	Type  int
	Data  [2]byte
}

// CC608 builds the packets a CEA-608 field carries: a pair of characters on the
// field given, which is all a presence check needs to see.
func CC608(ccType int) []CCPacket {
	return []CCPacket{
		{Valid: true, Type: ccType, Data: [2]byte{0x80 | 'A', 0x80 | 'B'}},
	}
}

// CC708 builds the packets that carry one DTVCC service block for the service
// number given. The packet layer is what names the service, so a reader that only
// notices "type 2 or 3 was present" cannot tell SERVICE1 from SERVICE6 — which is
// the whole reason this builder writes the header bytes rather than dummy data.
func CC708(service int) []CCPacket {
	// One service block: a two-byte header-and-size plus two bytes of data.
	block := []byte{byte(service&0x07)<<5 | 0x02, 0x20, 0x41}
	if service > 6 {
		// Service numbers above 6 use the extended header: 7 in the standard
		// field, then the real number in a following byte.
		block = []byte{7<<5 | 0x02, byte(service & 0x3F), 0x20, 0x41}
	}
	// The DTVCC packet header: sequence_number 0 and packet_size, which codes the
	// whole packet — its own header byte included — as 2*packet_size bytes. One
	// header plus len(block) of data rounded up to even is (len(block)+2)/2.
	pkt := append([]byte{byte((len(block)+2)/2) & 0x3F}, block...)

	var out []CCPacket
	typ := CCTypeDTVCCStart
	for i := 0; i < len(pkt); i += 2 {
		var d [2]byte
		d[0] = pkt[i]
		if i+1 < len(pkt) {
			d[1] = pkt[i+1]
		}
		out = append(out, CCPacket{Valid: true, Type: typ, Data: d})
		typ = CCTypeDTVCCData
	}
	return out
}

// H264SEICaptions builds a complete H.264 SEI NAL unit — header byte included —
// carrying the packets given as ATSC A/53 cc_data.
func H264SEICaptions(pkts []CCPacket) []byte {
	return append([]byte{0x06}, seiMessage(4, ga94UserData(pkts))...)
}

// HEVCSEICaptions is H264SEICaptions with the two-byte HEVC NAL header of a
// prefix SEI (type 39).
func HEVCSEICaptions(pkts []CCPacket) []byte {
	return append([]byte{39 << 1, 0x01}, seiMessage(4, ga94UserData(pkts))...)
}

// H264SEIOther builds an SEI NAL carrying a payload type that is not user data,
// so a reader can be asserted not to mistake any SEI for a caption SEI.
func H264SEIOther() []byte {
	return append([]byte{0x06}, seiMessage(1, []byte{0x00, 0x00, 0x00})...)
}

// seiMessage wraps a payload in the SEI RBSP framing: payload type and size as
// chains of 0xFF-terminated bytes, then the payload, then the stop bit.
func seiMessage(payloadType int, payload []byte) []byte {
	var out []byte
	for n := payloadType; ; {
		if n >= 0xFF {
			out = append(out, 0xFF)
			n -= 0xFF
			continue
		}
		out = append(out, byte(n))
		break
	}
	for n := len(payload); ; {
		if n >= 0xFF {
			out = append(out, 0xFF)
			n -= 0xFF
			continue
		}
		out = append(out, byte(n))
		break
	}
	out = append(out, payload...)
	return append(out, 0x80) // rbsp_trailing_bits
}

// ga94UserData builds the ATSC A/53 user_data_registered_itu_t_t35 payload: the
// country and provider codes, the GA94 identifier, user_data_type_code 3, then
// cc_data().
func ga94UserData(pkts []CCPacket) []byte {
	out := []byte{
		0xB5,       // itu_t_t35_country_code: United States
		0x00, 0x31, // itu_t_t35_provider_code: ATSC
		0x47, 0x41, 0x39, 0x34, // user_identifier: "GA94"
		0x03,                        // user_data_type_code: cc_data
		byte(0x40 | len(pkts)&0x1F), // process_em_data_flag 0, cc_count
		0xFF,                        // em_data
	}
	for _, p := range pkts {
		v := byte(0x00)
		if p.Valid {
			v = 0x04
		}
		out = append(out, 0xF8|v|byte(p.Type&0x03), p.Data[0], p.Data[1])
	}
	return append(out, 0xFF) // marker_bits
}

// TSWithSEI builds an MPEG-TS segment whose first access unit carries the SEI NAL
// given ahead of its parameter set and slice.
func TSWithSEI(startPTS, frameDur int64, frames int, sps, sei []byte) []byte {
	return tsWithExtraNALU(startPTS, frameDur, frames, sps, sei, 0x1B)
}

// TSWithHEVCSEI is TSWithSEI for an HEVC elementary stream, which the PMT names
// with a different stream type and whose parameter set is read by a different
// reader entirely.
func TSWithHEVCSEI(startPTS, frameDur int64, frames int, sps, sei []byte) []byte {
	return tsWithExtraHEVCNALU(startPTS, frameDur, frames, sps, sei)
}

// MP4InitHEVCWithSEI builds an HEVC video init, so a fragment carrying an HEVC
// prefix SEI can be read through the length-prefixed walk rather than Annex-B.
func MP4InitHEVCWithSEI(trackID, timescale uint32, width, height int) []byte {
	return mp4InitFrom(trackID, timescale, "vide",
		visualSampleEntry("hvc1", width, height), nil, width, height)
}

// MP4InitWithCaptionTrack builds an init declaring a video track and a CMAF
// closed-caption track beside it — a c608 or c708 sample entry under the `clcp`
// handler, which is how Apple's own fMP4 reference stream carries CEA-608 rather
// than in the video SEI.
func MP4InitWithCaptionTrack(videoID, captionID, timescale uint32, width, height int, entry string) []byte {
	video := mp4Trak(videoID, timescale, "vide", visualSampleEntry("avc1", width, height), width, height)
	// c608 and c708 extend SampleEntry directly: six reserved bytes and a data
	// reference index, and nothing else.
	caption := mp4Trak(captionID, timescale, "clcp",
		box(entry, concat(make([]byte, 6), []byte{0x00, 0x01})), 0, 0)
	mvhd := box("mvhd", concat(u32(0), make([]byte, 96)))
	moov := box("moov", concat(mvhd, video, caption))
	ftyp := box("ftyp", concat([]byte("isom"), u32(0), []byte("isomiso5")))
	return concat(ftyp, moov)
}

// MP4SegmentTwoTracks builds a fragment with a traf per track, which is how a
// CMAF segment carrying a caption track beside its video is laid out. A caption
// track with no samples is exactly what "the encoder stopped emitting captions"
// looks like on the wire.
func MP4SegmentTwoTracks(videoID, captionID, sequence uint32, baseDecodeTime int64, sampleDuration uint32, videoSamples, captionSamples int, payload []byte) []byte {
	mfhd := box("mfhd", concat(u32(0), u32(sequence)))
	traf := func(id uint32, samples int) []byte {
		tfhd := box("tfhd", concat(u32(0x000008), u32(id), u32(sampleDuration)))
		tfdt := box("tfdt", concat([]byte{0x01, 0x00, 0x00, 0x00}, u64(uint64(baseDecodeTime))))
		trun := box("trun", concat(u32(0), u32(uint32(samples))))
		return box("traf", concat(tfhd, tfdt, trun))
	}
	moof := box("moof", concat(mfhd, traf(videoID, videoSamples), traf(captionID, captionSamples)))
	styp := box("styp", concat([]byte("msdh"), u32(0), []byte("msdhmsix")))
	return concat(styp, moof, box("mdat", payload))
}

// MP4SegmentWithNALUs builds a fragment whose mdat holds the NAL units given, in
// the length-prefixed form fMP4 uses instead of Annex-B start codes.
func MP4SegmentWithNALUs(trackID, sequence uint32, baseDecodeTime int64, sampleDuration uint32, samples int, nalus [][]byte) []byte {
	var payload []byte
	for _, n := range nalus {
		payload = append(payload, u32(uint32(len(n)))...)
		payload = append(payload, n...)
	}
	return mp4SegmentWithPayload(trackID, sequence, baseDecodeTime, sampleDuration, samples, payload)
}
