package media

import "testing"

// The MPEG-TS readers below the segment level: PSI reassembly, the program
// tables, the PES header, sync recovery and the continuity counter.
//
// These are all reached only by malformed or unusual transport that a
// well-formed fixture cannot produce — a section split across packets, a stray
// prefix, a duplicate packet, a scrambled payload. Each one is a place where the
// parser either loses a track without saying so or reports transport loss that
// did not happen.

// ---------- psiAccumulate ----------

// A PSI section larger than one packet arrives in pieces. Until the declared
// length has all arrived there is nothing to parse, and a reader that gave up on
// the first fragment would never see the PMT of a program with many streams —
// losing every track in the segment.
func TestPsiAccumulate_ReassemblesASectionAcrossPackets(t *testing.T) {
	// A section of 300 bytes: 3 header bytes plus a declared length of 297.
	const total = 300
	body := make([]byte, total-3)
	for i := range body {
		body[i] = byte(i)
	}
	sec := []byte{0x02, 0x80 | byte((total-3)>>8), byte((total - 3) & 0xFF)}
	sec = append(sec, body...)

	buf := map[uint16][]byte{}
	// First packet: pointer_field 0, then the first 150 bytes.
	first := append([]byte{0x00}, sec[:150]...)
	if got, ok := psiAccumulate(buf, 0x1000, first, true); ok {
		t.Fatalf("a half-delivered section was reported complete (%d bytes)", len(got))
	}
	// Continuation: the rest.
	out, ok := psiAccumulate(buf, 0x1000, sec[150:], false)
	if !ok {
		t.Fatal("the section was never completed")
	}
	if len(out) != total {
		t.Errorf("section length = %d, want %d", len(out), total)
	}
	// Once delivered, the buffer is released so the next section starts clean.
	if buf[0x1000] != nil {
		t.Error("the reassembly buffer was not released after the section completed")
	}
}

// pointer_field skips any tail of the previous section sharing the packet.
func TestPsiAccumulate_HonoursThePointerField(t *testing.T) {
	sec := []byte{0x00, 0x80, 0x05, 1, 2, 3, 4, 5}
	payload := append([]byte{0x03, 0xAA, 0xBB, 0xCC}, sec...) // skip three bytes

	out, ok := psiAccumulate(map[uint16][]byte{}, 0, payload, true)
	if !ok {
		t.Fatal("a section behind a pointer_field was not read")
	}
	if out[0] != 0x00 || len(out) != 8 {
		t.Errorf("section = %v, want the eight bytes after the pointer", out)
	}
}

func TestPsiAccumulate_RejectsUnusablePayloads(t *testing.T) {
	tests := []struct {
		name    string
		buf     map[uint16][]byte
		payload []byte
		pusi    bool
	}{
		{"empty payload with the start flag", map[uint16][]byte{}, nil, true},
		{"pointer past the end", map[uint16][]byte{}, []byte{0x50, 0x01}, true},
		// A continuation for a PID whose section never started: there is nothing
		// to append to, and inventing a start would misparse the table.
		{"continuation with no start", map[uint16][]byte{}, []byte{1, 2, 3, 4}, false},
		{"too short to hold a length", map[uint16][]byte{}, []byte{0x00, 0x02}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := psiAccumulate(tc.buf, 0, tc.payload, tc.pusi); ok {
				t.Error("an unusable payload was reported as a complete section")
			}
		})
	}
}

// A declared section length beyond the 1024-byte maximum is not a PSI section at
// all, and accepting it would let a malformed packet drive an unbounded read.
func TestPsiAccumulate_RejectsAnOversizedDeclaredLength(t *testing.T) {
	payload := append([]byte{0x00, 0x02, 0x0F, 0xFF}, make([]byte, 200)...)
	if _, ok := psiAccumulate(map[uint16][]byte{}, 0, payload, true); ok {
		t.Error("a section declaring more than 1024 bytes was accepted")
	}
}

// ---------- parsePAT / parsePMT ----------

// Program number 0 is the network information table, not a program. Treating its
// PID as a PMT makes the parser look for a program map where there is none.
func TestParsePAT_SkipsTheNetworkPID(t *testing.T) {
	// section_length covers everything after the two length bytes: 5 header
	// bytes + two 4-byte entries + 4 CRC bytes = 17.
	sec := []byte{0x00, 0xB0, 17, 0x00, 0x01, 0xC1, 0x00, 0x00}
	sec = append(sec, 0x00, 0x00, 0xE0, 0x10) // program 0: the network PID
	sec = append(sec, 0x00, 0x01, 0xE1, 0x00) // program 1: a real PMT at 0x100
	sec = append(sec, 0xDE, 0xAD, 0xBE, 0xEF)

	got := parsePAT(sec)
	if len(got) != 1 {
		t.Fatalf("parsePAT = %v, want just the one real program", got)
	}
	if got[0] != 0x100 {
		t.Errorf("PMT PID = %#x, want 0x100", got[0])
	}
}

func TestParsePAT_RejectsWhatIsNotAPAT(t *testing.T) {
	if got := parsePAT([]byte{0x02, 0xB0, 0x0D, 0, 0, 0, 0, 0}); got != nil {
		t.Errorf("a PMT table_id parsed as a PAT: %v", got)
	}
	for n := 0; n < 8; n++ {
		if got := parsePAT(make([]byte, n)); got != nil {
			t.Errorf("%d bytes parsed as a PAT: %v", n, got)
		}
	}
}

// A section_length longer than the bytes present is clamped rather than read
// past: a truncated table still yields the programs it did deliver.
func TestParsePAT_ClampsAnOverlongSectionLength(t *testing.T) {
	sec := []byte{0x00, 0x0F, 0xFF, 0x00, 0x01, 0xC1, 0x00, 0x00}
	sec = append(sec, 0x00, 0x01, 0xE1, 0x00)
	if got := parsePAT(sec); len(got) != 1 || got[0] != 0x100 {
		t.Errorf("parsePAT with an overlong declared length = %v, want [0x100]", got)
	}
}

// The descriptors between the program header and the first stream are variable
// length, both at program level and per stream. Skipping them by the wrong
// amount reads a stream_type out of a descriptor and mislabels the track.
func TestParsePMT_SkipsDescriptors(t *testing.T) {
	// program_info_length 4, then two streams, the first with a 2-byte descriptor.
	sec := []byte{0x02, 0xB0, 0x00, 0x00, 0x01, 0xC1, 0x00, 0x00, 0xE1, 0x00, 0xF0, 0x04}
	sec = append(sec, 0x09, 0x02, 0xAA, 0xBB) // a program-level descriptor
	sec = append(sec, 0x1B, 0xE1, 0x00, 0xF0, 0x02, 0xCC, 0xDD)
	sec = append(sec, 0x0F, 0xE1, 0x01, 0xF0, 0x00)
	sec = append(sec, 0xDE, 0xAD, 0xBE, 0xEF)
	sec[2] = byte(len(sec) - 3) // section_length

	got := parsePMT(sec)
	if len(got) != 2 {
		t.Fatalf("parsePMT = %v, want two streams", got)
	}
	if got[0x100] != 0x1B {
		t.Errorf("PID 0x100 stream_type = %#x, want 0x1B — a descriptor was read as a stream", got[0x100])
	}
	if got[0x101] != 0x0F {
		t.Errorf("PID 0x101 stream_type = %#x, want 0x0F", got[0x101])
	}
}

func TestParsePMT_RejectsWhatIsNotAPMT(t *testing.T) {
	if got := parsePMT([]byte{0x00, 0xB0, 0x12, 0, 0, 0, 0, 0, 0, 0, 0, 0}); got != nil {
		t.Errorf("a PAT table_id parsed as a PMT: %v", got)
	}
	for n := 0; n < 12; n++ {
		if got := parsePMT(make([]byte, n)); got != nil {
			t.Errorf("%d bytes parsed as a PMT: %v", n, got)
		}
	}
}

func TestParsePMT_ClampsAnOverlongSectionLength(t *testing.T) {
	sec := []byte{0x02, 0x0F, 0xFF, 0x00, 0x01, 0xC1, 0x00, 0x00, 0xE1, 0x00, 0xF0, 0x00}
	sec = append(sec, 0x1B, 0xE1, 0x00, 0xF0, 0x00)
	if got := parsePMT(sec); len(got) != 1 || got[0x100] != 0x1B {
		t.Errorf("parsePMT with an overlong declared length = %v", got)
	}
}

// ---------- PES ----------

// Padding and private_stream_2 carry no optional PES header, so the bytes where
// a PTS would be are payload. Reading them as a timestamp yields a plausible
// number from arbitrary data, which is worse than reporting none.
func TestPesHasHeaderExtension(t *testing.T) {
	for _, id := range []byte{0xBC, 0xBE, 0xBF, 0xF0, 0xF1, 0xF2, 0xF8, 0xFF} {
		if pesHasHeaderExtension(id) {
			t.Errorf("stream_id %#x carries no optional PES header", id)
		}
	}
	for _, id := range []byte{0xE0, 0xE1, 0xC0, 0xC1, 0xBD, 0xFD} {
		if !pesHasHeaderExtension(id) {
			t.Errorf("stream_id %#x does carry the optional PES header", id)
		}
	}
}

func TestPesPTS_RejectsPacketsWithoutAReadableTimestamp(t *testing.T) {
	good := func() []byte {
		p := []byte{0x00, 0x00, 0x01, 0xE0, 0x00, 0x00, 0x80, 0x80, 0x05}
		return append(p, 0x21, 0x00, 0x01, 0x00, 0x01)
	}
	if _, ok := pesPTS(good()); !ok {
		t.Fatal("a well-formed PES packet yielded no PTS")
	}

	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"no start code", func(p []byte) []byte { p[2] = 0x00; return p }},
		{"padding stream carries no header", func(p []byte) []byte { p[3] = 0xBE; return p }},
		{"PTS_DTS_flags clear", func(p []byte) []byte { p[7] = 0x00; return p }},
		{"too short", func(p []byte) []byte { return p[:13] }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if pts, ok := pesPTS(tc.mutate(good())); ok {
				t.Errorf("got PTS %d, want none", pts)
			}
		})
	}
}

// The PES header length is declared, so the elementary stream starts a variable
// distance in. Getting it wrong feeds the SPS reader bytes that are still header.
func TestPesBody_StartsAfterTheDeclaredHeaderLength(t *testing.T) {
	p := []byte{0x00, 0x00, 0x01, 0xE0, 0x00, 0x00, 0x80, 0x80, 0x03, 0xAA, 0xBB, 0xCC}
	p = append(p, 'e', 's')

	body, ok := pesBody(p)
	if !ok {
		t.Fatal("no body found")
	}
	if string(body) != "es" {
		t.Errorf("body = %q, want %q", body, "es")
	}
}

func TestPesBody_RejectsUnusablePackets(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
	}{
		{"too short", []byte{0x00, 0x00, 0x01, 0xE0}},
		{"no start code", []byte{0x00, 0x00, 0x00, 0xE0, 0, 0, 0x80, 0x80, 0x00, 1}},
		{"padding stream", []byte{0x00, 0x00, 0x01, 0xBE, 0, 0, 0x80, 0x80, 0x00, 1}},
		// A declared header length that runs past the packet leaves no body,
		// which must be reported rather than sliced out of bounds.
		{"header longer than the packet", []byte{0x00, 0x00, 0x01, 0xE0, 0, 0, 0x80, 0x80, 0xFF, 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if body, ok := pesBody(tc.in); ok {
				t.Errorf("got body %q, want none", body)
			}
		})
	}
}

// ---------- continuity counter ----------

// The continuity counter is how transport loss is detected, and three things
// must not be counted as loss: the first packet seen, a declared discontinuity,
// and a duplicate packet — which the spec explicitly permits.
func TestTsStream_CheckCC(t *testing.T) {
	tests := []struct {
		name   string
		seq    []int
		disc   []bool
		errors int
	}{
		{"in order", []int{0, 1, 2, 3}, []bool{false, false, false, false}, 0},
		{"wraps past 15", []int{14, 15, 0, 1}, []bool{false, false, false, false}, 0},
		{"one lost packet", []int{0, 2}, []bool{false, false}, 1},
		{"a duplicate is not loss", []int{0, 0, 1}, []bool{false, false, false}, 0},
		// A splice declares its discontinuity in the adaptation field; counting
		// it would report loss on a legitimate ad insertion.
		{"declared discontinuity", []int{0, 5}, []bool{false, true}, 0},
		{"first packet is never loss", []int{9}, []bool{false}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &tsStream{lastCC: -1}
			for i, cc := range tc.seq {
				s.checkCC(cc, tc.disc[i])
			}
			if s.ccErrors != tc.errors {
				t.Errorf("ccErrors = %d, want %d", s.ccErrors, tc.errors)
			}
		})
	}
}

// ---------- elementary-stream capture ----------

// Only video payload is kept, and only up to the cap: the capture exists to find
// a parameter set, and an unbounded one would hold a whole segment in memory for
// every rendition being checked.
func TestTsStream_Capture(t *testing.T) {
	audio := &tsStream{streamType: 0x0F, lastCC: -1}
	audio.capture(make([]byte, 100))
	if len(audio.es) != 0 {
		t.Errorf("captured %d bytes of an audio stream", len(audio.es))
	}

	video := &tsStream{streamType: 0x1B, lastCC: -1}
	video.capture([]byte{1, 2, 3})
	video.capture([]byte{4, 5})
	if string(video.es) != string([]byte{1, 2, 3, 4, 5}) {
		t.Errorf("es = %v, want the appended payloads", video.es)
	}

	// Filled to one byte short of the cap, the next write is truncated to fit
	// and every write after it is dropped.
	full := &tsStream{streamType: 0x1B, lastCC: -1, es: make([]byte, maxESCapture-1)}
	full.capture([]byte{0xAA, 0xBB, 0xCC})
	if len(full.es) != maxESCapture {
		t.Errorf("es grew to %d, want exactly the %d-byte cap", len(full.es), maxESCapture)
	}
	full.capture([]byte{0xDD})
	if len(full.es) != maxESCapture {
		t.Errorf("es grew past the cap to %d", len(full.es))
	}
}

// ---------- sync recovery ----------

// A segment served with a stray prefix still has to parse: the sync byte repeats
// at the 188-byte stride, and that is what identifies the real start.
func TestTsSyncOffset(t *testing.T) {
	good := mediatestTS()

	if off, ok := tsSyncOffset(good); !ok || off != 0 {
		t.Errorf("tsSyncOffset on a clean segment = %d, %v; want 0, true", off, ok)
	}

	// A prefix containing a decoy 0x47 that is not on the stride.
	prefixed := append([]byte{0x00, 0x47, 0xFF}, good...)
	off, ok := tsSyncOffset(prefixed)
	if !ok {
		t.Fatal("a prefixed segment lost its sync")
	}
	if off != 3 {
		t.Errorf("sync offset = %d, want 3 — the decoy 0x47 at index 1 was taken for the start", off)
	}

	// Nothing that looks like transport at all.
	for _, bad := range [][]byte{nil, {}, []byte("<html>404</html>"), make([]byte, 400)} {
		if _, ok := tsSyncOffset(bad); ok {
			t.Errorf("found sync in %d bytes that are not transport", len(bad))
		}
	}

	// A single packet with no second sync byte to confirm against is still
	// accepted when it is all the data there is.
	one := make([]byte, TSPacketSize)
	one[0] = 0x47
	if _, ok := tsSyncOffset(one); !ok {
		t.Error("a lone well-formed packet was rejected")
	}
}

// ---------- sortedStreams ----------

func TestSortedStreams(t *testing.T) {
	m := map[uint16]*tsStream{
		0x101: {pid: 0x101}, 0x100: {pid: 0x100}, 0x1FF: {pid: 0x1FF}, 0x0FF: {pid: 0x0FF},
	}
	got := sortedStreams(m)
	want := []uint16{0x0FF, 0x100, 0x101, 0x1FF}
	if len(got) != len(want) {
		t.Fatalf("sortedStreams returned %d streams, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].pid != w {
			t.Fatalf("stream %d has PID %#x, want %#x — track order must not depend on map iteration", i, got[i].pid, w)
		}
	}
}

// ---------- streamCodec's remaining rows ----------

func TestStreamCodec_ID3AndUnknown(t *testing.T) {
	if got := streamCodec(0x15); got != "id3" {
		t.Errorf("streamCodec(0x15) = %q, want id3", got)
	}
	// Every stream type this table does not know must come back unnamed rather
	// than guessed, so the tracks check has nothing to falsely compare.
	for _, st := range []byte{0x00, 0x07, 0x20, 0x50, 0xC0} {
		if got := streamCodec(st); got != "" {
			t.Errorf("streamCodec(%#x) = %q, want the empty string", st, got)
		}
	}
}

// mediatestTS is a minimal well-formed segment built here rather than in
// mediatest, so the sync tests do not depend on that package's shape.
func mediatestTS() []byte {
	pkt := make([]byte, TSPacketSize*3)
	for i := 0; i < 3; i++ {
		pkt[i*TSPacketSize] = 0x47
		pkt[i*TSPacketSize+1] = 0x1F
		pkt[i*TSPacketSize+2] = 0xFF // the null PID: valid, and carries nothing
		pkt[i*TSPacketSize+3] = 0x10
	}
	return pkt
}
