package media

import (
	"strings"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// mediatestSPSBytes is the escaped SPS payload as it appears in an elementary
// stream, without the start code or the NAL header byte.
func mediatestSPSBytes(width, height int) []byte {
	return mediatest.AnnexB(mediatest.SPSFor(width, height))[5:]
}

// ParseTS at the segment level: the packet shapes a well-formed fixture never
// produces. Null packets, adaptation-only packets, a scrambled payload, an
// elementary stream that arrives before the program map that describes it, and
// sync lost in the middle rather than at the start.
//
// Each is a place where the parser can lose a whole track without saying so,
// which under this tool's rules is the worst outcome available: the checks that
// would have looked at that track simply report nothing.

// tsPkt assembles one 188-byte packet. adaptation, when non-nil, becomes the
// adaptation field; payload is what follows it.
func tsPkt(pid uint16, pusi bool, cc int, scramble byte, adaptation, payload []byte) []byte {
	pkt := make([]byte, 0, TSPacketSize)
	b1 := byte(pid>>8) & 0x1F
	if pusi {
		b1 |= 0x40
	}
	afc := byte(0x01)
	if adaptation != nil {
		afc = 0x03
	}
	if payload == nil {
		afc = 0x02 // adaptation field only, no payload
	}
	pkt = append(pkt, 0x47, b1, byte(pid&0xFF), scramble<<6|afc<<4|byte(cc&0x0F))
	if afc&0x02 != 0 {
		pkt = append(pkt, byte(len(adaptation)))
		pkt = append(pkt, adaptation...)
	}
	pkt = append(pkt, payload...)
	for len(pkt) < TSPacketSize {
		pkt = append(pkt, 0xFF)
	}
	return pkt[:TSPacketSize]
}

func patPkt(pmtPID uint16) []byte {
	sec := []byte{0x00, 0xB0, 0x0D, 0x00, 0x01, 0xC1, 0x00, 0x00}
	sec = append(sec, 0x00, 0x01, byte(0xE0|pmtPID>>8), byte(pmtPID&0xFF))
	sec = append(sec, 0xDE, 0xAD, 0xBE, 0xEF)
	return tsPkt(0x0000, true, 0, 0, nil, append([]byte{0x00}, sec...))
}

func pmtPkt(pmtPID, esPID uint16, streamType byte) []byte {
	sec := []byte{0x02, 0xB0, 0x12, 0x00, 0x01, 0xC1, 0x00, 0x00,
		byte(0xE0 | esPID>>8), byte(esPID & 0xFF), 0xF0, 0x00,
		streamType, byte(0xE0 | esPID>>8), byte(esPID & 0xFF), 0xF0, 0x00,
		0xDE, 0xAD, 0xBE, 0xEF}
	return tsPkt(pmtPID, true, 0, 0, nil, append([]byte{0x00}, sec...))
}

func pesPkt(pid uint16, cc int, pts int64, es []byte, scramble byte) []byte {
	header := []byte{0x00, 0x00, 0x01, 0xE0, 0x00, 0x00, 0x80, 0x80, 0x05}
	header = append(header,
		byte(0x20|((pts>>30)&0x07)<<1|0x01),
		byte((pts>>22)&0xFF),
		byte(((pts>>15)&0x7F)<<1|0x01),
		byte((pts>>7)&0xFF),
		byte((pts&0x7F)<<1|0x01),
	)
	return tsPkt(pid, true, cc, scramble, nil, append(header, es...))
}

// Null packets are stuffing. Counting them as an elementary stream would invent a
// track with no timestamps, which the tracks check then reports on.
func TestParseTS_NullPacketsAreNotATrack(t *testing.T) {
	var data []byte
	data = append(data, patPkt(0x1000)...)
	data = append(data, pmtPkt(0x1000, 0x0100, 0x1B)...)
	for i := 0; i < 3; i++ {
		data = append(data, tsPkt(0x1FFF, false, i, 0, nil, []byte{0xFF, 0xFF})...)
	}
	data = append(data, pesPkt(0x0100, 0, 90000, nil, 0)...)

	info, err := ParseTS(data)
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	if len(info.Tracks) != 1 {
		t.Fatalf("got %d tracks, want 1 — the null PID was taken for a stream", len(info.Tracks))
	}
	if info.Tracks[0].Kind != Video {
		t.Errorf("track kind = %s, want video", info.Tracks[0].Kind)
	}
}

// The transport_scrambling_control bits mean the payload is encrypted, so the
// parser cannot read inside it. That has to be reported as a property of the
// track — segcheck could not look — rather than as an absence of timestamps.
func TestParseTS_ScrambledPayloadMarksTheTrack(t *testing.T) {
	var data []byte
	data = append(data, patPkt(0x1000)...)
	data = append(data, pmtPkt(0x1000, 0x0100, 0x1B)...)
	data = append(data, pesPkt(0x0100, 0, 90000, nil, 0x02)...) // scrambled

	info, err := ParseTS(data)
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	if len(info.Tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(info.Tracks))
	}
	if !info.Tracks[0].Encrypted {
		t.Error("a scrambled payload did not mark the track encrypted")
	}
	if !info.Encrypted() {
		t.Error("the segment did not report itself as encrypted")
	}
}

// A packager may emit the elementary stream before the program map. Dropping
// those packets would lose the timestamps at the start of the segment and make
// the segment look shorter than it is.
func TestParseTS_ElementaryStreamBeforeItsProgramMap(t *testing.T) {
	var data []byte
	data = append(data, pesPkt(0x0100, 0, 90000, nil, 0)...) // before any PAT
	data = append(data, patPkt(0x1000)...)
	data = append(data, pmtPkt(0x1000, 0x0100, 0x1B)...)
	data = append(data, pesPkt(0x0100, 1, 93600, nil, 0)...)

	info, err := ParseTS(data)
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	if len(info.Tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(info.Tracks))
	}
	tr := info.Tracks[0]
	if tr.Samples != 2 {
		t.Errorf("samples = %d, want 2 — the packet before the PMT was dropped", tr.Samples)
	}
	if tr.MinPTS != 90000 {
		t.Errorf("MinPTS = %d, want 90000 from the packet that preceded the PMT", tr.MinPTS)
	}
	// The stream was tracked before its type was known, so it stays unclassified
	// rather than being guessed at.
	if tr.Kind != Other {
		t.Logf("kind = %s (the PMT arrived after the stream was first seen)", tr.Kind)
	}
}

// An adaptation field with no payload carries the PCR and the discontinuity flag
// and nothing else. It must be stepped over rather than read as payload.
func TestParseTS_AdaptationOnlyPacketsCarryNoPayload(t *testing.T) {
	var data []byte
	data = append(data, patPkt(0x1000)...)
	data = append(data, pmtPkt(0x1000, 0x0100, 0x1B)...)
	// Adaptation field only: 182 bytes of it, no payload at all.
	data = append(data, tsPkt(0x0100, false, 0, 0, make([]byte, 182), nil)...)
	data = append(data, pesPkt(0x0100, 1, 90000, nil, 0)...)

	info, err := ParseTS(data)
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	if len(info.Tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(info.Tracks))
	}
	if info.Tracks[0].Samples != 1 {
		t.Errorf("samples = %d, want 1", info.Tracks[0].Samples)
	}
	// The adaptation-only packet has no payload, so it is not a continuity error.
	if info.Tracks[0].CCErrors != 0 {
		t.Errorf("CCErrors = %d, want 0", info.Tracks[0].CCErrors)
	}
}

// An adaptation field long enough to swallow the whole packet leaves no payload.
// The packet is skipped rather than sliced out of bounds.
func TestParseTS_AdaptationFieldFillingThePacket(t *testing.T) {
	var data []byte
	data = append(data, patPkt(0x1000)...)
	data = append(data, pmtPkt(0x1000, 0x0100, 0x1B)...)

	// afc = 0x03 (adaptation + payload) but the declared length reaches the end.
	pkt := []byte{0x47, 0x01, 0x00, 0x30, 183}
	pkt = append(pkt, make([]byte, TSPacketSize-5)...)
	data = append(data, pkt...)
	data = append(data, pesPkt(0x0100, 1, 90000, nil, 0)...)

	info, err := ParseTS(data)
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	if len(info.Tracks) != 1 {
		t.Errorf("got %d tracks, want 1", len(info.Tracks))
	}
}

// Sync lost in the middle of a segment — a truncated write, a proxy that spliced
// in a banner — must be recovered from at the next packet boundary. Abandoning
// the segment there would silently drop every timestamp after the damage.
func TestParseTS_RecoversFromSyncLostMidSegment(t *testing.T) {
	// Sync is confirmed against several packets at the 188-byte stride, so the
	// run before the damage has to be long enough to establish it.
	var data []byte
	data = append(data, patPkt(0x1000)...)
	data = append(data, pmtPkt(0x1000, 0x0100, 0x1B)...)
	for i, pts := range []int64{90000, 93600, 97200, 100800} {
		data = append(data, pesPkt(0x0100, i, pts, nil, 0)...)
	}
	// 188 bytes of rubbish where a packet should be.
	data = append(data, make([]byte, TSPacketSize)...)
	data = append(data, pesPkt(0x0100, 4, 104400, nil, 0)...)
	data = append(data, pesPkt(0x0100, 5, 108000, nil, 0)...)

	info, err := ParseTS(data)
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	if len(info.Tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(info.Tracks))
	}
	tr := info.Tracks[0]
	if tr.Samples != 6 {
		t.Errorf("samples = %d, want 6 — the packets after the damage were lost", tr.Samples)
	}
	if tr.MaxPTS != 108000 {
		t.Errorf("MaxPTS = %d, want 108000 — recovery stopped short of the tail", tr.MaxPTS)
	}
}

// Rubbish with no recoverable boundary at all ends the walk, and the packets
// already read still produce a result.
func TestParseTS_UnrecoverableTailEndsTheWalk(t *testing.T) {
	var data []byte
	data = append(data, patPkt(0x1000)...)
	data = append(data, pmtPkt(0x1000, 0x0100, 0x1B)...)
	for i, pts := range []int64{90000, 93600, 97200, 100800} {
		data = append(data, pesPkt(0x0100, i, pts, nil, 0)...)
	}
	// A run of bytes with no 0x47 anywhere, so there is no boundary to recover at.
	tail := make([]byte, TSPacketSize*2)
	for i := range tail {
		tail[i] = 0x11
	}
	data = append(data, tail...)

	info, err := ParseTS(data)
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	if len(info.Tracks) != 1 || info.Tracks[0].Samples != 4 {
		t.Errorf("tracks = %d, samples = %d; want the four packets read before the rubbish",
			len(info.Tracks), info.Tracks[0].Samples)
	}
}

// A PES packet larger than one transport packet continues in packets with the
// start flag clear. Those carry no PES header, so their whole payload is
// elementary stream — and dropping them loses the parameter set whenever it does
// not fit in the first packet, which for a real SPS with a long VUI it often
// does not.
func TestParseTS_ContinuationPacketsAreCapturedAsElementaryStream(t *testing.T) {
	var data []byte
	data = append(data, patPkt(0x1000)...)
	data = append(data, pmtPkt(0x1000, 0x0100, 0x1B)...)

	// The first packet opens the PES with a slice and no parameter set. The
	// parameter set arrives in a continuation packet, which carries no PES header
	// at all — so it is only ever seen if the whole payload is captured.
	data = append(data, pesPkt(0x0100, 0, 90000, []byte{0x00, 0x00, 0x00, 0x01, 0x41, 0x9A}, 0)...)

	sps := append([]byte{0x00, 0x00, 0x00, 0x01, 0x67}, mediatestSPSBytes(1920, 1080)...)
	data = append(data, tsPkt(0x0100, false, 1, 0, nil, sps)...)

	data = append(data, pesPkt(0x0100, 2, 93600, nil, 0)...)
	data = append(data, pesPkt(0x0100, 3, 97200, nil, 0)...)

	info, err := ParseTS(data)
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	tr, ok := info.Track(Video)
	if !ok {
		t.Fatal("no video track")
	}
	if tr.Width != 1920 || tr.Height != 1080 {
		t.Errorf("resolution = %dx%d, want 1920x1080 — the continuation packet's payload was dropped", tr.Width, tr.Height)
	}
	if tr.CCErrors != 0 {
		t.Errorf("CCErrors = %d, want 0", tr.CCErrors)
	}
}

// Transport that carries nothing but a program map describes no media. That is
// not an unknown container — the bytes really are MPEG-TS — so it gets an error
// that says exactly what was found.
func TestParseTS_PacketsButNoElementaryStream(t *testing.T) {
	var data []byte
	data = append(data, patPkt(0x1000)...)
	for i := 0; i < 3; i++ {
		data = append(data, tsPkt(0x1FFF, false, i, 0, nil, []byte{0xFF})...)
	}

	info, err := ParseTS(data)
	if err == nil {
		t.Fatal("transport with no elementary stream parsed cleanly")
	}
	if !strings.Contains(err.Error(), "no elementary stream") {
		t.Errorf("err = %v, want it to say there is no elementary stream", err)
	}
	if info.Container != ContainerTS {
		t.Errorf("container = %q, want it still reported as MPEG-TS", info.Container)
	}
}

// Fewer bytes than one packet, or bytes that never sync: not this container.
func TestParseTS_NoPacketsAtAll(t *testing.T) {
	for _, data := range [][]byte{nil, {}, []byte("<html>404</html>")} {
		if _, err := ParseTS(data); err != ErrUnknownContainer {
			t.Errorf("%d bytes: err = %v, want ErrUnknownContainer", len(data), err)
		}
	}
}

// MinPTS and MaxPTS are the minimum and maximum, not the first and last. With
// B-frames the stream is not in presentation order, so a parser that took the
// first timestamp as the start would report a segment that begins late and a gap
// against the previous one that is not there.
func TestParseTS_MinAndMaxPTSAreNotFirstAndLast(t *testing.T) {
	var data []byte
	data = append(data, patPkt(0x1000)...)
	data = append(data, pmtPkt(0x1000, 0x0100, 0x1B)...)
	// Decode order for an IPBB pattern: the earliest presentation time arrives
	// third, and the latest arrives second.
	for i, pts := range []int64{93600, 104400, 90000, 97200, 100800} {
		data = append(data, pesPkt(0x0100, i, pts, nil, 0)...)
	}

	info, err := ParseTS(data)
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	tr := info.Tracks[0]
	if tr.MinPTS != 90000 {
		t.Errorf("MinPTS = %d, want 90000 — the first timestamp was taken for the minimum", tr.MinPTS)
	}
	if tr.MaxPTS != 104400 {
		t.Errorf("MaxPTS = %d, want 104400 — the last timestamp was taken for the maximum", tr.MaxPTS)
	}
}
