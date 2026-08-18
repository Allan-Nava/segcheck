package media

import (
	"encoding/hex"
	"fmt"
)

// SCTE-35 splice information.
//
// An ad break is signalled by a splice_info_section: in MPEG-TS on a PID the PMT
// declares with stream_type 0x86, carried as a private section rather than a PES;
// in fMP4 inside an `emsg` box.
//
// The section states when the splice happens on the same 90kHz clock the pictures
// use. That is what makes it a media measurement rather than another manifest
// claim, and it is what lets segcheck answer the question an operator actually
// has: the break is signalled, but can a player cut there at all?
//
// Two fields are easy to read wrongly, and both were tested before either was
// written. pts_adjustment is *added* by the decoder, so a section a downstream
// splicer shifted states its real time in neither field alone. And a
// splice_immediate — or a splice_time with time_specified_flag clear — states no
// time at all: it means "now", and inventing a time for it would put the break
// wherever the section happened to be multiplexed.

// spliceTableID is the table_id of a splice_info_section.
const spliceTableID = 0xFC

// maxSplicesPerSegment bounds how many sections are kept from one segment. A
// handful is normal; thousands is a malformed or hostile stream.
const maxSplicesPerSegment = 256

// SplicePoint is one SCTE-35 splice information section.
type SplicePoint struct {
	// PTS is when the splice happens, on the 90kHz clock, with pts_adjustment
	// already applied. Valid only when HasPTS is set.
	PTS int64 `json:"pts,omitempty"`
	// HasPTS is false for a splice_immediate and for any command that carries no
	// time. Such a break cannot be compared to a segment boundary, and a check
	// that saw the zero value as "at the start" would report every one of them as
	// perfectly aligned.
	HasPTS bool `json:"has_pts,omitempty"`
	// Command is the splice_command_type, named.
	Command string `json:"command,omitempty"`
	// OutOfNetwork marks the start of a break rather than the return from one.
	OutOfNetwork bool `json:"out_of_network,omitempty"`
	// EventID is splice_event_id, which pairs an out with its in.
	EventID uint32 `json:"event_id,omitempty"`
	// Timescale is the clock PTS is counted on: 90kHz for MPEG-TS, whatever an
	// emsg states for fMP4. Without it a caller comparing against a track's
	// timestamps would be comparing two different units.
	Timescale uint32 `json:"timescale,omitempty"`
	// Scheme is the emsg scheme_id_uri an fMP4 event arrived under, empty for
	// MPEG-TS where the PMT stream type is what identifies it.
	Scheme string `json:"scheme,omitempty"`
	// SegmentationType names what kind of break this is, from the
	// segmentation_descriptor: a provider advertisement, a distributor placement
	// opportunity, a programme boundary. The command says *when*; this says *what*,
	// and an operator chasing an ad-insertion problem needs both.
	SegmentationType string `json:"segmentation_type,omitempty"`
	// UPID identifies the break to an ad server. Kept as text when it is text and
	// as hex otherwise, because half the types in the table are opaque bytes.
	UPID string `json:"upid,omitempty"`
	// Duration is the break's length in 90kHz ticks, when a descriptor states one.
	Duration int64 `json:"duration,omitempty"`
}

// ParseSpliceSection reads a splice_info_section from bytes a caller obtained
// elsewhere — the hexadecimal an HLS SCTE35-OUT attribute carries, which is the
// manifest's own copy of what the media says.
func ParseSpliceSection(sec []byte) (SplicePoint, bool) {
	return parseSpliceSection(sec)
}

// parseSpliceSection reads one splice_info_section.
func parseSpliceSection(sec []byte) (SplicePoint, bool) {
	// table_id, two flag bits and two reserved, then a 12-bit section_length.
	if len(sec) < 14 || sec[0] != spliceTableID {
		return SplicePoint{}, false
	}
	length := int(sec[1]&0x0F)<<8 | int(sec[2])
	if length+3 > len(sec) {
		return SplicePoint{}, false
	}
	body := sec[3 : 3+length]

	// protocol_version(8) encrypted_packet(1) encryption_algorithm(6)
	// pts_adjustment(33) cw_index(8) tier(12) splice_command_length(12)
	// splice_command_type(8) — 88 bits, so the command starts at byte 11.
	const headerLen = 11
	if len(body) < headerLen {
		return SplicePoint{}, false
	}
	r := &bitReader{data: body}
	r.skip(8) // protocol_version
	encrypted := r.bits(1) == 1
	r.skip(6) // encryption_algorithm
	adjustment := int64(r.bits(1))<<32 | int64(r.bits(32))
	r.skip(8)  // cw_index
	r.skip(12) // tier
	cmdLen := int(r.bits(12))
	cmdType := int(r.bits(8))
	// No error check: headerLen guarantees the 88 bits just read were there.

	out := SplicePoint{Command: spliceCommandName(cmdType)}
	if encrypted {
		// The command is ciphertext. The section still says a break is signalled,
		// which is worth reporting; its timing is not readable here.
		return out, true
	}

	// splice_command_length may be 0x0FFF, which means "unknown" — read to the end
	// of the section rather than trusting it.
	if cmdLen == 0x0FFF || cmdLen <= 0 {
		cmdLen = len(body) - headerLen
	}
	cmd := r.take(cmdLen)
	if cmd == nil {
		return out, true // the command is not readable; the signal still is
	}

	// The descriptor loop follows the command: a 16-bit length and then the
	// descriptors themselves. This is where the *kind* of break is stated, and
	// stopping at the command left every finding saying "time_signal" where the
	// stream said "Provider Placement Opportunity Start".
	if loopLen := int(r.bits(16)); !r.err {
		if loop := r.take(loopLen); loop != nil {
			readSpliceDescriptors(loop, &out)
		}
	}

	switch cmdType {
	case 0x05: // splice_insert
		c := &bitReader{data: cmd}
		out.EventID = c.bits(32)
		cancel := c.bits(1) == 1
		c.skip(7) // reserved
		if cancel || c.err {
			return out, true
		}
		out.OutOfNetwork = c.bits(1) == 1
		programSplice := c.bits(1) == 1
		durationFlag := c.bits(1) == 1
		immediate := c.bits(1) == 1
		c.skip(4) // reserved
		_ = durationFlag
		if programSplice && !immediate {
			if pts, ok := readSpliceTime(c); ok {
				out.PTS, out.HasPTS = wrapPTS(pts+adjustment), true
			}
		}
	case 0x06: // time_signal
		c := &bitReader{data: cmd}
		if pts, ok := readSpliceTime(c); ok {
			out.PTS, out.HasPTS = wrapPTS(pts+adjustment), true
		}
	}
	return out, true
}

// readSpliceTime reads a splice_time(), which states a time only when its
// time_specified_flag is set.
func readSpliceTime(r *bitReader) (int64, bool) {
	if r.bits(1) != 1 {
		return 0, false // no time specified: the splice means "now"
	}
	r.skip(6) // reserved
	pts := int64(r.bits(1))<<32 | int64(r.bits(32))
	if r.err {
		return 0, false
	}
	return pts, true
}

// wrapPTS keeps a sum on the 33-bit clock MPEG-TS timestamps live on.
func wrapPTS(v int64) int64 {
	return ((v % PTSModulus) + PTSModulus) % PTSModulus
}

func spliceCommandName(t int) string {
	switch t {
	case 0x00:
		return "splice_null"
	case 0x04:
		return "splice_schedule"
	case 0x05:
		return "splice_insert"
	case 0x06:
		return "time_signal"
	case 0x07:
		return "bandwidth_reservation"
	case 0xFF:
		return "private_command"
	}
	return "unknown"
}

// skip advances past bits whose value does not matter.
func (r *bitReader) skip(n int) {
	if r.pos+n > len(r.data)*8 {
		r.err = true
		return
	}
	r.pos += n
}

// take returns n whole bytes from the current position, which must be byte
// aligned, or nil when they are not there. SCTE-35's commands are byte-aligned
// sub-structures, and reading one as a slice keeps its own bounds its own.
func (r *bitReader) take(n int) []byte {
	if r.err || r.pos%8 != 0 || n < 0 {
		return nil
	}
	start := r.pos / 8
	if start+n > len(r.data) {
		return nil
	}
	r.pos += n * 8
	return r.data[start : start+n]
}

// SCTE-35 emsg schemes. The binary one carries a splice_info_section verbatim, so
// the same reader answers for it; the XML one carries a different encoding this
// reader does not model, and its presence alone is what gets reported.
const (
	scte35BinScheme = "urn:scte:scte35:2013:bin"
	scte35XMLScheme = "urn:scte:scte35:2013:xml"
)

// maxEmsgStringLen bounds the null-terminated strings in an emsg. A box claiming a
// scheme longer than this is malformed, and scanning on would read the rest of the
// segment looking for a terminator that is not there.
const maxEmsgStringLen = 512

// parseEmsgs reads the DASH event message boxes at the top level of a segment.
// They sit beside the moof rather than inside it, which is why the fragment walk
// never saw them.
func parseEmsgs(data []byte, baseDecodeTime int64, haveBase bool) []SplicePoint {
	var out []SplicePoint
	for _, b := range boxesIn(data) {
		if b.typ != "emsg" || len(out) >= maxSplicesPerSegment {
			continue
		}
		if sp, ok := parseEmsg(b.payload, baseDecodeTime, haveBase); ok {
			out = append(out, sp)
		}
	}
	return out
}

// parseEmsg reads one emsg box. Version 0 states a delta from the fragment's own
// start, version 1 an absolute time on the stated timescale — so version 0 without
// a tfdt to add it to has no time at all, rather than a time of zero.
func parseEmsg(b []byte, baseDecodeTime int64, haveBase bool) (SplicePoint, bool) {
	if len(b) < 4 {
		return SplicePoint{}, false
	}
	version := b[0]
	body := b[4:]

	var (
		scheme, value string
		timescale     uint32
		pts           int64
		hasPTS        bool
		id            uint32
		msg           []byte
	)
	switch version {
	case 0:
		var ok bool
		scheme, body, ok = emsgString(body)
		if !ok {
			return SplicePoint{}, false
		}
		value, body, ok = emsgString(body)
		if !ok {
			return SplicePoint{}, false
		}
		if len(body) < 16 {
			return SplicePoint{}, false
		}
		timescale = be32(body[0:])
		delta := int64(be32(body[4:]))
		id = be32(body[12:])
		msg = body[16:]
		// The delta is measured from the fragment's decode time. Without one there
		// is nothing to measure it from, and a delta reported as an absolute time
		// would place every event at the start of the presentation.
		if haveBase {
			pts, hasPTS = baseDecodeTime+delta, true
		}
	case 1:
		if len(body) < 20 {
			return SplicePoint{}, false
		}
		timescale = be32(body[0:])
		pts, hasPTS = int64(be64(body[4:])), true
		id = be32(body[16:])
		rest := body[20:]
		var ok bool
		scheme, rest, ok = emsgString(rest)
		if !ok {
			return SplicePoint{}, false
		}
		value, rest, ok = emsgString(rest)
		if !ok {
			return SplicePoint{}, false
		}
		msg = rest
	default:
		return SplicePoint{}, false
	}
	_ = value

	if scheme != scte35BinScheme && scheme != scte35XMLScheme {
		return SplicePoint{}, false // an event, but not an ad break
	}

	out := SplicePoint{
		Timescale: timescale,
		Scheme:    scheme,
		EventID:   id,
		PTS:       pts,
		HasPTS:    hasPTS,
	}
	if scheme == scte35BinScheme {
		// The message is a splice_info_section verbatim. Its command tells us
		// whether this is the start of a break or the return from one — but its
		// splice_time is on the MPEG-TS clock, while the emsg states the event's
		// time on its own timescale, so the emsg's time is the one to keep.
		if sp, ok := parseSpliceSection(msg); ok {
			out.Command = sp.Command
			out.OutOfNetwork = sp.OutOfNetwork
			if sp.EventID != 0 {
				out.EventID = sp.EventID
			}
		}
	}
	return out, true
}

// emsgString reads one null-terminated UTF-8 string and returns the rest.
func emsgString(b []byte) (string, []byte, bool) {
	limit := len(b)
	if limit > maxEmsgStringLen {
		limit = maxEmsgStringLen
	}
	for i := 0; i < limit; i++ {
		if b[i] == 0x00 {
			return string(b[:i]), b[i+1:], true
		}
	}
	return "", nil, false
}

// segmentationTypes names the segmentation_type_id values. The table is deliberately
// partial: an id this tool does not know is left unnamed rather than guessed at, and
// the number is more use to an operator than a wrong word.
var segmentationTypes = map[int]string{
	0x00: "Not Indicated",
	0x01: "Content Identification",
	0x10: "Program Start",
	0x11: "Program End",
	0x12: "Program Early Termination",
	0x13: "Program Breakaway",
	0x14: "Program Resumption",
	0x15: "Program Runover Planned",
	0x16: "Program Runover Unplanned",
	0x17: "Program Overlap Start",
	0x20: "Chapter Start",
	0x21: "Chapter End",
	0x22: "Break Start",
	0x23: "Break End",
	0x30: "Provider Advertisement Start",
	0x31: "Provider Advertisement End",
	0x32: "Distributor Advertisement Start",
	0x33: "Distributor Advertisement End",
	0x34: "Provider Placement Opportunity Start",
	0x35: "Provider Placement Opportunity End",
	0x36: "Distributor Placement Opportunity Start",
	0x37: "Distributor Placement Opportunity End",
	0x40: "Unscheduled Event Start",
	0x41: "Unscheduled Event End",
	0x50: "Network Start",
	0x51: "Network End",
}

// cueiIdentifier is the four bytes every SCTE-35 descriptor this tool reads is
// registered under. A descriptor loop is extensible, so one carrying another
// registration is left alone rather than read as if it were this one.
const cueiIdentifier = 0x43554549 // "CUEI"

// readSpliceDescriptors walks the descriptor loop, taking what a segmentation
// descriptor says about the break.
func readSpliceDescriptors(loop []byte, out *SplicePoint) {
	for off := 0; off+2 <= len(loop); {
		tag := int(loop[off])
		length := int(loop[off+1])
		body := off + 2
		if body+length > len(loop) {
			return // a declared length that runs past the loop
		}
		if tag == 0x02 { // segmentation_descriptor
			readSegmentationDescriptor(loop[body:body+length], out)
		}
		off = body + length
	}
}

// readSegmentationDescriptor reads the fields this tool reports. Almost every one of
// them is optional and shifts the ones after it, which is why the flags are read
// rather than assumed: a reader that took the delivery restrictions to be absent
// names whatever byte happens to sit where the type id goes.
func readSegmentationDescriptor(b []byte, out *SplicePoint) {
	r := &bitReader{data: b}
	if r.bits(32) != cueiIdentifier {
		return // another registration entirely
	}
	eventID := r.bits(32)
	cancel := r.bits(1) == 1
	r.skip(7)
	if cancel || r.err {
		return
	}

	programSegmentation := r.bits(1) == 1
	durationFlag := r.bits(1) == 1
	notRestricted := r.bits(1) == 1
	if notRestricted {
		r.skip(5) // reserved
	} else {
		// web_delivery_allowed, no_regional_blackout, archive_allowed and
		// device_restrictions: five bits either way, but only one of the two
		// spellings is present, and everything after moves with it.
		r.skip(5)
	}
	if !programSegmentation {
		// A per-component descriptor: each component states its own offset, and
		// skipping the wrong number of them lands mid-field.
		count := int(r.bits(8))
		for i := 0; i < count; i++ {
			r.skip(8)  // component_tag
			r.skip(7)  // reserved
			r.skip(33) // pts_offset
		}
	}
	var duration int64
	if durationFlag {
		duration = int64(r.bits(8))<<32 | int64(r.bits(32))
	}
	upidType := int(r.bits(8))
	upidLen := int(r.bits(8))
	upid := r.take(upidLen)
	typeID := int(r.bits(8))
	if r.err {
		return
	}

	name, known := segmentationTypes[typeID]
	if !known {
		name = fmt.Sprintf("segmentation type %#02x", typeID)
	}
	out.SegmentationType = name
	out.EventID = eventID
	if duration > 0 {
		out.Duration = duration
	}
	if len(upid) > 0 {
		out.UPID = formatUPID(upidType, upid)
	}
}

// formatUPID renders a UPID. Half the types in the table are text an operator will
// recognise and half are opaque bytes, so the text ones are kept as text and the rest
// as hex: a byte string printed as if it were UTF-8 is unreadable either way.
func formatUPID(upidType int, upid []byte) string {
	switch upidType {
	case 0x01, 0x03, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F, 0x10:
		if printableASCII(upid) {
			return string(upid)
		}
	}
	return hex.EncodeToString(upid)
}

func printableASCII(b []byte) bool {
	for _, c := range b {
		if c < 0x20 || c > 0x7E {
			return false
		}
	}
	return len(b) > 0
}
