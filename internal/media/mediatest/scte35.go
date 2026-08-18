package mediatest

// SCTE-35 builders.
//
// An ad break is signalled by a splice_info_section: in MPEG-TS on a PID the PMT
// declares with stream_type 0x86, carried as a private section rather than a PES;
// in fMP4 inside an `emsg` box. The section states when the splice happens on the
// same 90kHz clock the pictures use, which is what makes "the manifest says cut
// here and the media says cut 400ms later" a checkable claim.

// Splice command types.
const (
	SpliceNull    = 0x00
	SpliceInsert  = 0x05
	SpliceTimeSig = 0x06
	SplicePrivate = 0xFF
)

// SCTE35StreamType is the PMT stream_type of a splice information PID.
const SCTE35StreamType = 0x86

// SpliceSpec describes the section to build.
type SpliceSpec struct {
	// PTS is the splice point on the 90kHz clock, written into splice_time.
	PTS int64
	// NoPTS builds a section whose splice_time states no time at all — a
	// splice_immediate, which means "now" and cannot be compared to a boundary.
	NoPTS bool
	// PTSAdjustment is added to the splice time by a decoder. A non-zero one is how
	// a downstream splicer shifts a section it did not author, and reading
	// splice_time alone gets the wrong answer whenever it is set.
	PTSAdjustment int64
	// Command is the splice_command_type. SpliceInsert and SpliceTimeSig carry a
	// time; the others do not.
	Command int
	// OutOfNetwork marks the start of a break rather than the return from one.
	OutOfNetwork bool
	// EventID is splice_event_id, which pairs an out with its in.
	EventID uint32
	// Descriptors are written into the section's descriptor loop.
	Descriptors []SegmentationDescriptor
}

// SpliceSection builds one splice_info_section, without the transport framing.
func SpliceSection(spec SpliceSpec) []byte {
	var cmd bitWriter
	switch spec.Command {
	case SpliceInsert:
		cmd.u(32, spec.EventID)
		cmd.u(1, 0) // splice_event_cancel_indicator
		cmd.u(7, 0) // reserved
		cmd.u(1, boolBit(spec.OutOfNetwork))
		cmd.u(1, 1) // program_splice_flag
		cmd.u(1, 0) // duration_flag
		cmd.u(1, boolBit(spec.NoPTS))
		cmd.u(4, 0) // reserved
		if !spec.NoPTS {
			writeSpliceTime(&cmd, spec.PTS)
		}
		cmd.u(16, 0) // unique_program_id
		cmd.u(8, 0)  // avail_num
		cmd.u(8, 0)  // avails_expected
	case SpliceTimeSig:
		if spec.NoPTS {
			cmd.u(1, 0) // time_specified_flag clear
			cmd.u(7, 0) // reserved
		} else {
			writeSpliceTime(&cmd, spec.PTS)
		}
	}
	command := cmd.bytes()

	var w bitWriter
	w.u(8, 0) // protocol_version
	w.u(1, 0) // encrypted_packet
	w.u(6, 0) // encryption_algorithm
	writeBits33(&w, spec.PTSAdjustment)
	w.u(8, 0)                     // cw_index
	w.u(12, 0)                    // tier
	w.u(12, uint32(len(command))) // splice_command_length
	w.u(8, uint32(spec.Command&0xFF))
	body := append(w.bytes(), command...)
	var descriptors []byte
	for _, d := range spec.Descriptors {
		descriptors = append(descriptors, SegmentationDescriptorBytes(d)...)
	}
	body = append(body, byte(len(descriptors)>>8), byte(len(descriptors)))
	body = append(body, descriptors...)
	body = append(body, 0xDE, 0xAD, 0xBE, 0xEF) // CRC32

	// table_id, then section_syntax_indicator 0, private_indicator 0, two reserved
	// bits set, and a 12-bit section_length covering everything after it.
	out := []byte{0xFC, 0x30 | byte(len(body)>>8), byte(len(body) & 0xFF)}
	return append(out, body...)
}

// writeSpliceTime writes a splice_time() with the time specified.
func writeSpliceTime(w *bitWriter, pts int64) {
	w.u(1, 1) // time_specified_flag
	w.u(6, 0) // reserved
	writeBits33(w, pts)
}

// writeBits33 writes one of SCTE-35's 33-bit timestamps, which do not fit the
// 32-bit writer in one call.
func writeBits33(w *bitWriter, v int64) {
	w.u(1, uint32((v>>32)&0x01))
	w.u(32, uint32(v&0xFFFFFFFF))
}

// TSWithSplice builds a segment carrying video and a splice information PID the
// PMT declares with stream_type 0x86 — the shape a packager receives from an
// upstream encoder that has been told where the ad breaks are.
func TSWithSplice(startPTS, frameDur int64, frames int, sps []byte, specs ...SpliceSpec) []byte {
	const (
		pmtPID    = 0x1000
		videoPID  = 0x0100
		splicePID = 0x01F0
	)
	var out []byte
	out = append(out, tsPacket(0x0000, true, 0, pat(pmtPID))...)
	out = append(out, tsPacket(pmtPID, true, 0, pmt2(videoPID, 0x1B, splicePID, SCTE35StreamType))...)

	// Each section goes in its own packet, behind the pointer_field a private
	// section shares with PSI.
	for i, spec := range specs {
		sec := append([]byte{0x00}, SpliceSection(spec)...)
		out = append(out, tsPacket(splicePID, true, i&0x0F, sec)...)
	}

	es := []byte{0x00, 0x00, 0x00, 0x01, 0x67}
	es = append(es, sps...)
	es = append(es, 0x00, 0x00, 0x00, 0x01, H264IDRSlice)

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

// SCTE35BinScheme is the emsg scheme_id_uri whose message is a splice_info_section
// verbatim.
const SCTE35BinScheme = "urn:scte:scte35:2013:bin"

// Emsg builds a DASH event message box. Version 0 states a delta from the
// fragment's own decode time; version 1 an absolute time on the stated timescale.
func Emsg(version byte, scheme, value string, timescale uint32, time int64, id uint32, message []byte) []byte {
	str := func(s string) []byte { return append([]byte(s), 0x00) }
	var body []byte
	switch version {
	case 0:
		body = concat(
			str(scheme), str(value),
			u32(timescale),
			u32(uint32(time)), // presentation_time_delta
			u32(0),            // event_duration
			u32(id),
			message,
		)
	default:
		body = concat(
			u32(timescale),
			u64(uint64(time)), // presentation_time
			u32(0),            // event_duration
			u32(id),
			str(scheme), str(value),
			message,
		)
	}
	return box("emsg", append([]byte{version, 0x00, 0x00, 0x00}, body...))
}

// MP4SegmentWithEmsg builds a fragment preceded by the event message boxes given.
// They sit at the top level, beside the moof rather than inside it, which is why a
// fragment walk alone never sees them.
func MP4SegmentWithEmsg(trackID, sequence uint32, baseDecodeTime int64, sampleDuration uint32, samples, payloadBytes int, emsgs ...[]byte) []byte {
	var head []byte
	for _, e := range emsgs {
		head = append(head, e...)
	}
	return append(head, MP4Segment(trackID, sequence, baseDecodeTime, sampleDuration, samples, payloadBytes)...)
}

// Segmentation type ids, the ones that name a break rather than a programme edge.
const (
	SegTypeProviderAdStart      = 0x30
	SegTypeProviderAdEnd        = 0x31
	SegTypeProviderPlacementOpp = 0x34
	SegTypeBreakStart           = 0x22
	SegTypeProgramStart         = 0x10
)

// SegmentationDescriptor describes the descriptor to build.
type SegmentationDescriptor struct {
	EventID  uint32
	TypeID   int
	UPIDType int
	UPID     []byte
	// Duration in 90kHz ticks; zero writes no segmentation_duration at all.
	Duration int64
	// Restricted writes the delivery restriction fields, which shift every field
	// after them — a reader that assumed they were absent misreads the type id.
	Restricted bool
}

// SegmentationDescriptorBytes builds one segmentation_descriptor.
func SegmentationDescriptorBytes(d SegmentationDescriptor) []byte {
	var w bitWriter
	w.u(32, 0x43554549) // identifier: "CUEI"
	w.u(32, d.EventID)
	w.u(1, 0) // segmentation_event_cancel_indicator
	w.u(7, 0) // reserved
	w.u(1, 1) // program_segmentation_flag
	if d.Duration > 0 {
		w.u(1, 1) // segmentation_duration_flag
	} else {
		w.u(1, 0)
	}
	if d.Restricted {
		w.u(1, 0) // delivery_not_restricted_flag clear: the restriction fields follow
		w.u(1, 1) // web_delivery_allowed_flag
		w.u(1, 1) // no_regional_blackout_flag
		w.u(1, 1) // archive_allowed_flag
		w.u(2, 3) // device_restrictions
	} else {
		w.u(1, 1) // delivery_not_restricted_flag
		w.u(5, 0x1F)
	}
	if d.Duration > 0 {
		w.u(8, uint32(d.Duration>>32)&0xFF)
		w.u(32, uint32(d.Duration))
	}
	w.u(8, uint32(d.UPIDType))
	w.u(8, uint32(len(d.UPID)))
	body := append(w.bytes(), d.UPID...)

	var tail bitWriter
	tail.u(8, uint32(d.TypeID))
	tail.u(8, 1) // segment_num
	tail.u(8, 1) // segments_expected
	body = append(body, tail.bytes()...)

	return append([]byte{0x02, byte(len(body))}, body...)
}
