package media

import (
	"fmt"
)

// TSPacketSize is the fixed MPEG-TS packet length.
const TSPacketSize = 188

// TSTimescale is the fixed 90kHz clock of MPEG-TS presentation timestamps.
const TSTimescale = 90000

// maxESCapture bounds how much elementary-stream payload we keep per track in
// order to find the H.264 SPS. The SPS sits within the first keyframe, so a
// segment that has not shown one in 1 MiB does not have one to find.
const maxESCapture = 1 << 20

// streamTypeAAC is the PMT stream_type for AAC delivered as ADTS.
const streamTypeAAC = 0x0F

// streamTypeSCTE35 is the PMT stream_type of a splice information PID.
const streamTypeSCTE35 = 0x86

// adtsHeaderCapture bounds how much AAC payload we keep. Only the first frame
// header is read, and holding a whole segment of audio per rendition to read
// seven bytes of it would cost megabytes for nothing.
const adtsHeaderCapture = 64

// tsStream is the accumulating state for one elementary stream (one PID).
type tsStream struct {
	pid        uint16
	streamType byte
	pts        []int64
	scrambled  bool
	es         []byte // bounded capture, for SPS parsing
	// lastCC is the previous continuity counter seen with a payload, -1 before
	// the first packet.
	lastCC   int
	ccErrors int
}

// ParseTS parses an MPEG-TS segment: the program map, then every elementary
// stream's presentation timestamps, transport-level packet loss, and — for
// H.264 — the coded resolution out of the SPS.
func ParseTS(data []byte) (SegmentInfo, error) {
	start, ok := tsSyncOffset(data)
	if !ok {
		return SegmentInfo{}, ErrUnknownContainer
	}

	var (
		pmtPIDs    = map[uint16]bool{}
		splicePIDs = map[uint16]bool{}
		streams    = map[uint16]*tsStream{}
		sections   = map[uint16][]byte{} // PSI reassembly per PID
		splices    []SplicePoint
		packets    int
	)

	for off := start; off+TSPacketSize <= len(data); off += TSPacketSize {
		pkt := data[off : off+TSPacketSize]
		if pkt[0] != 0x47 {
			// Lost sync: try to recover at the next plausible packet boundary
			// rather than abandoning the segment.
			next, ok := tsSyncOffset(data[off:])
			if !ok {
				break
			}
			off += next - TSPacketSize // the loop's += restores alignment
			continue
		}
		packets++

		pusi := pkt[1]&0x40 != 0
		pid := uint16(pkt[1]&0x1F)<<8 | uint16(pkt[2])
		scrambled := pkt[3]&0xC0 != 0
		afc := (pkt[3] >> 4) & 0x03
		cc := int(pkt[3] & 0x0F)

		payloadStart := 4
		discontinuity := false
		if afc&0x02 != 0 {
			if payloadStart >= len(pkt) {
				continue
			}
			afLen := int(pkt[4])
			if afLen > 0 && 5 < len(pkt) {
				discontinuity = pkt[5]&0x80 != 0
			}
			payloadStart = 5 + afLen
		}
		hasPayload := afc&0x01 != 0
		if !hasPayload || payloadStart >= len(pkt) {
			continue
		}
		payload := pkt[payloadStart:]

		switch {
		case pid == 0x0000: // PAT
			if sec, done := psiAccumulate(sections, pid, payload, pusi); done {
				for _, p := range parsePAT(sec) {
					pmtPIDs[p] = true
				}
			}
		case pmtPIDs[pid]: // PMT
			if sec, done := psiAccumulate(sections, pid, payload, pusi); done {
				for pid, stype := range parsePMT(sec) {
					if _, seen := streams[pid]; !seen {
						streams[pid] = &tsStream{pid: pid, streamType: stype, lastCC: -1}
					}
					if stype == streamTypeSCTE35 {
						splicePIDs[pid] = true
					}
				}
			}
		case pid == 0x1FFF: // null packets
			continue
		default:
			s := streams[pid]
			if s == nil {
				// An elementary stream we saw before its PMT. Track it as
				// unknown rather than dropping its timestamps.
				s = &tsStream{pid: pid, lastCC: -1}
				streams[pid] = s
			}
			if scrambled {
				s.scrambled = true
			}
			s.checkCC(cc, discontinuity)
			if splicePIDs[pid] {
				// Splice information is a private section, not a PES: it shares
				// PSI's pointer_field framing. It stays in this branch so the PID's
				// continuity counters are still followed and an operator can see
				// the signalling is there at all.
				if sec, done := psiAccumulate(sections, pid, payload, pusi); done {
					if sp, ok := parseSpliceSection(sec); ok && len(splices) < maxSplicesPerSegment {
						splices = append(splices, sp)
					}
				}
				continue
			}
			if pusi {
				if pts, ok := pesPTS(payload); ok {
					s.pts = append(s.pts, pts)
				}
				if body, ok := pesBody(payload); ok {
					s.capture(body)
				}
			} else {
				s.capture(payload)
			}
		}
	}

	if packets == 0 {
		return SegmentInfo{}, ErrUnknownContainer
	}

	info := SegmentInfo{Container: ContainerTS, Bytes: int64(len(data)), Splices: splices}
	for _, s := range sortedStreams(streams) {
		info.Tracks = append(info.Tracks, s.track())
	}
	if len(info.Tracks) == 0 {
		return info, fmt.Errorf("MPEG-TS with %d packets but no elementary stream", packets)
	}
	return info, nil
}

// checkCC follows the 4-bit continuity counter, which increments once per
// packet with payload on a PID. A break means the transport dropped or
// duplicated packets somewhere between the packager and us — unless the
// adaptation field declares the discontinuity, which is how a splice is
// signalled legitimately.
func (s *tsStream) checkCC(cc int, discontinuity bool) {
	defer func() { s.lastCC = cc }()
	if s.lastCC < 0 || discontinuity {
		return
	}
	if cc == s.lastCC {
		return // a duplicate packet is explicitly allowed by the spec
	}
	if cc != (s.lastCC+1)&0x0F {
		s.ccErrors++
	}
}

// esCapacity is how much of the elementary stream is worth keeping. A video
// track is scanned end to end for parameter sets and random access points; an
// AAC track only needs its first ADTS header, and capturing a whole segment of
// audio to read seven bytes would cost megabytes per rendition.
func (s *tsStream) esCapacity() int {
	switch {
	case isVideoStreamType(s.streamType):
		return maxESCapture
	case s.streamType == streamTypeAAC:
		return adtsHeaderCapture
	default:
		return 0
	}
}

func (s *tsStream) capture(b []byte) {
	room := s.esCapacity() - len(s.es)
	if room <= 0 {
		return
	}
	if len(b) > room {
		b = b[:room]
	}
	s.es = append(s.es, b...)
}

// keyframes runs a keyframe walk over the captured elementary stream, and clears
// Scanned when the capture itself was cut short at maxESCapture: a keyframe past
// the cap is one nobody looked for, not one that is absent.
func (s *tsStream) keyframes(walk func([]byte) keyframeVerdict) keyframeVerdict {
	v := walk(s.es)
	if len(s.es) >= maxESCapture {
		v.Scanned = false
	}
	return v
}

func (s *tsStream) track() Track {
	t := Track{
		ID:        uint32(s.pid),
		Kind:      streamKind(s.streamType),
		Codec:     streamCodec(s.streamType),
		Timescale: TSTimescale,
		Samples:   len(s.pts),
		Encrypted: s.scrambled,
		CCErrors:  s.ccErrors,
	}
	if len(s.pts) > 0 {
		t.HasPTS = true
		t.MinPTS, t.MaxPTS = s.pts[0], s.pts[0]
		for _, p := range s.pts {
			if p < t.MinPTS {
				t.MinPTS = p
			}
			if p > t.MaxPTS {
				t.MaxPTS = p
			}
		}
		t.FrameDur = medianDelta(s.pts)
	}
	if t.Kind == Audio && s.streamType == streamTypeAAC {
		if rate, channels, ok := adtsHeaderFields(s.es); ok {
			t.SampleRate, t.Channels = rate, channels
		}
	}
	if t.Kind == Video && len(s.es) > 0 {
		// Dispatch on the stream type rather than trying both readers: an HEVC
		// NAL header is two bytes and carries its type in different bits, so a
		// stream read with the wrong reader does not fail cleanly — it can find
		// something that looks like a parameter set and return a plausible
		// wrong resolution, which is worse than reporting none.
		var (
			w, h int
			ok   bool
		)
		switch t.Codec {
		case "hevc":
			w, h, ok = hevcResolution(s.es)
			kf := hevcKeyframes(s.es)
			t.OpensOnKeyframe, t.HasKeyframe = kf.Opens, kf.Present
			t.KeyframeKnown, t.KeyframeScanned = kf.Known, kf.Scanned
			t.Captions = hevcCaptions(s.es)
			if c, cok := hevcColour(s.es); cok {
				t.ColourDesc = c
			}
		default:
			w, h, ok = h264Resolution(s.es)
			kf := h264Keyframes(s.es)
			t.OpensOnKeyframe, t.HasKeyframe = kf.Opens, kf.Present
			t.KeyframeKnown, t.KeyframeScanned = kf.Known, kf.Scanned
			t.Captions = h264Captions(s.es)
			if c, cok := h264Colour(s.es); cok {
				t.ColourDesc = c
			}
		}
		if ok {
			t.Width, t.Height = w, h
		}
	}
	return t
}

// tsSyncOffset finds the first byte where the 188-byte packet stride holds.
// Segments served with a stray prefix (a proxy banner, a partial packet) still
// parse instead of being written off as an unknown container.
func tsSyncOffset(data []byte) (int, bool) {
	limit := len(data)
	if limit > TSPacketSize*4 {
		limit = TSPacketSize * 4
	}
	for i := 0; i < limit; i++ {
		if data[i] != 0x47 {
			continue
		}
		// Confirm with as many further sync bytes as the data allows.
		ok := true
		confirmed := 0
		for j := 1; j <= 3; j++ {
			next := i + j*TSPacketSize
			if next >= len(data) {
				break
			}
			confirmed++
			if data[next] != 0x47 {
				ok = false
				break
			}
		}
		if ok && (confirmed > 0 || len(data)-i >= TSPacketSize) {
			return i, true
		}
	}
	return 0, false
}

// psiAccumulate reassembles a PSI section that may span several TS packets.
// It returns the complete section once the declared length has arrived.
func psiAccumulate(buf map[uint16][]byte, pid uint16, payload []byte, pusi bool) ([]byte, bool) {
	if pusi {
		if len(payload) < 1 {
			return nil, false
		}
		ptr := int(payload[0])
		if 1+ptr > len(payload) {
			return nil, false
		}
		buf[pid] = append([]byte(nil), payload[1+ptr:]...)
	} else {
		if buf[pid] == nil {
			return nil, false // continuation without a start: nothing to append to
		}
		buf[pid] = append(buf[pid], payload...)
	}
	sec := buf[pid]
	if len(sec) < 3 {
		return nil, false
	}
	total := 3 + int(uint16(sec[1]&0x0F)<<8|uint16(sec[2]))
	if total > 1024 || len(sec) < total {
		return nil, false
	}
	delete(buf, pid)
	return sec[:total], true
}

// parsePAT returns the PMT PIDs of every program in the table.
func parsePAT(sec []byte) []uint16 {
	if len(sec) < 8 || sec[0] != 0x00 {
		return nil
	}
	sectionLength := int(uint16(sec[1]&0x0F)<<8 | uint16(sec[2]))
	end := 3 + sectionLength - 4 // drop the trailing CRC32
	if end > len(sec) {
		end = len(sec)
	}
	var out []uint16
	for i := 8; i+4 <= end; i += 4 {
		programNumber := uint16(sec[i])<<8 | uint16(sec[i+1])
		pid := uint16(sec[i+2]&0x1F)<<8 | uint16(sec[i+3])
		if programNumber == 0 {
			continue // network PID, not a program map
		}
		out = append(out, pid)
	}
	return out
}

// parsePMT returns elementary PID -> stream_type for one program.
func parsePMT(sec []byte) map[uint16]byte {
	if len(sec) < 12 || sec[0] != 0x02 {
		return nil
	}
	sectionLength := int(uint16(sec[1]&0x0F)<<8 | uint16(sec[2]))
	end := 3 + sectionLength - 4
	if end > len(sec) {
		end = len(sec)
	}
	programInfoLength := int(uint16(sec[10]&0x0F)<<8 | uint16(sec[11]))
	out := map[uint16]byte{}
	for i := 12 + programInfoLength; i+5 <= end; {
		streamType := sec[i]
		pid := uint16(sec[i+1]&0x1F)<<8 | uint16(sec[i+2])
		esInfoLength := int(uint16(sec[i+3]&0x0F)<<8 | uint16(sec[i+4]))
		out[pid] = streamType
		i += 5 + esInfoLength
	}
	return out
}

// pesPTS extracts the presentation timestamp from the head of a PES packet.
func pesPTS(payload []byte) (int64, bool) {
	if len(payload) < 14 || payload[0] != 0x00 || payload[1] != 0x00 || payload[2] != 0x01 {
		return 0, false
	}
	if !pesHasHeaderExtension(payload[3]) {
		return 0, false
	}
	if payload[7]&0x80 == 0 { // PTS_DTS_flags: no PTS present
		return 0, false
	}
	p := payload[9:14]
	pts := (int64(p[0]&0x0E) << 29) |
		(int64(p[1]) << 22) |
		(int64(p[2]&0xFE) << 14) |
		(int64(p[3]) << 7) |
		(int64(p[4]) >> 1)
	return pts, true
}

// pesBody returns the elementary-stream bytes after the PES header.
func pesBody(payload []byte) ([]byte, bool) {
	if len(payload) < 9 || payload[0] != 0x00 || payload[1] != 0x00 || payload[2] != 0x01 {
		return nil, false
	}
	if !pesHasHeaderExtension(payload[3]) {
		return nil, false
	}
	start := 9 + int(payload[8])
	if start >= len(payload) {
		return nil, false
	}
	return payload[start:], true
}

// pesHasHeaderExtension reports whether a stream_id carries the optional PES
// header (flags, PTS/DTS). Padding and private_stream_2 do not.
func pesHasHeaderExtension(streamID byte) bool {
	switch streamID {
	case 0xBC, 0xBE, 0xBF, 0xF0, 0xF1, 0xF2, 0xF8, 0xFF:
		return false
	}
	return true
}

func isVideoStreamType(t byte) bool { return streamKind(t) == Video }

func streamKind(t byte) TrackKind {
	switch t {
	case 0x01, 0x02, 0x10, 0x1B, 0x24, 0x25, 0x33, 0xD1, 0xEA:
		return Video
	case 0x03, 0x04, 0x0F, 0x11, 0x1C, 0x81, 0x82, 0x83, 0x84, 0x87, 0x8A:
		return Audio
	default:
		return Other
	}
}

func streamCodec(t byte) string {
	switch t {
	case 0x01:
		return "mpeg1video"
	case 0x02:
		return "mpeg2video"
	case 0x10:
		return "mpeg4video"
	case 0x1B:
		return "h264"
	case 0x24, 0x25:
		return "hevc"
	case 0x33:
		return "vvc"
	case 0xD1:
		return "dirac"
	case 0xEA:
		return "vc1"
	case 0x03, 0x04:
		return "mp2"
	case 0x0F, 0x11:
		return "aac"
	case 0x1C:
		return "pcm"
	case 0x81, 0x82, 0x83, 0x84:
		return "ac3"
	case 0x87, 0x8A:
		return "eac3"
	case 0x06:
		return "" // PES private data: could be AC-3, subtitles, or ID3
	case 0x15:
		return "id3"
	case streamTypeSCTE35:
		return "scte35"
	default:
		return ""
	}
}

// sortedStreams returns the streams in PID order so output is deterministic.
func sortedStreams(m map[uint16]*tsStream) []*tsStream {
	pids := make([]uint16, 0, len(m))
	for pid := range m {
		pids = append(pids, pid)
	}
	for i := 1; i < len(pids); i++ {
		for j := i; j > 0 && pids[j] < pids[j-1]; j-- {
			pids[j], pids[j-1] = pids[j-1], pids[j]
		}
	}
	out := make([]*tsStream, 0, len(pids))
	for _, pid := range pids {
		out = append(out, m[pid])
	}
	return out
}
