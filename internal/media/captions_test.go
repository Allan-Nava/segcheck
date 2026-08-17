package media

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// SC-37: closed captions.
//
// CEA-608 and CEA-708 captions do not live in a track of their own — they ride in
// the video elementary stream, in an SEI message carrying ATSC A/53 user data. So
// "the manifest declares CC1 and the encoder stopped emitting it" is invisible to
// every manifest checker, and readable only by walking the bitstream.
//
// CC1 and CC3 share CEA-608 field 1; CC2 and CC4 share field 2. Telling CC1 from
// CC3 needs the line-21 control codes decoded, which this reader does not do, so
// it reports the field. CEA-708 names its services in the DTVCC packet layer, so
// those are reported by number.

func TestParseTS_CaptionsInSEI(t *testing.T) {
	sps := mediatest.SPSFor(1280, 720)
	tests := []struct {
		name     string
		pkts     []mediatest.CCPacket
		field1   bool
		field2   bool
		services []int
	}{
		{"608 field 1", mediatest.CC608(mediatest.CCTypeField1), true, false, nil},
		{"608 field 2", mediatest.CC608(mediatest.CCTypeField2), false, true, nil},
		{"708 service 1", mediatest.CC708(1), false, false, []int{1}},
		{"708 service 6", mediatest.CC708(6), false, false, []int{6}},
		// Above 6 the service number moves into an extended header byte, which a
		// reader that stops at the three-bit field would read as service 7.
		{"708 service 12", mediatest.CC708(12), false, false, []int{12}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sei := mediatest.H264SEICaptions(tc.pkts)
			info, err := ParseTS(mediatest.TSWithSEI(0, 3600, 25, sps, sei))
			if err != nil {
				t.Fatalf("ParseTS: %v", err)
			}
			tr, ok := info.Track(Video)
			if !ok {
				t.Fatal("no video track")
			}
			c := tr.Captions
			if !c.Scanned {
				t.Fatal("the bitstream was not scanned for captions")
			}
			if c.Field1 != tc.field1 || c.Field2 != tc.field2 {
				t.Errorf("fields = %v/%v, want %v/%v", c.Field1, c.Field2, tc.field1, tc.field2)
			}
			if !sameInts(c.Services, tc.services) {
				t.Errorf("services = %v, want %v", c.Services, tc.services)
			}
			if !c.Any() {
				t.Error("Any() is false on a segment that carries captions")
			}
		})
	}
}

// A segment with no caption SEI must report none — and be seen to have been
// looked at, because "no captions" and "not scanned" lead to opposite verdicts.
func TestParseTS_NoCaptionsIsScannedAndEmpty(t *testing.T) {
	sps := mediatest.SPSFor(1280, 720)
	for _, tc := range []struct {
		name string
		seg  []byte
	}{
		{"no SEI at all", mediatest.TSWithSPS(0, 3600, 25, sps)},
		{"an SEI that is not user data", mediatest.TSWithSEI(0, 3600, 25, sps, mediatest.H264SEIOther())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := ParseTS(tc.seg)
			if err != nil {
				t.Fatalf("ParseTS: %v", err)
			}
			tr, _ := info.Track(Video)
			if !tr.Captions.Scanned {
				t.Error("the bitstream was not scanned for captions")
			}
			if tr.Captions.Any() {
				t.Errorf("captions reported on a segment that carries none: %+v", tr.Captions)
			}
		})
	}
}

// A packet marked invalid is not caption data. Treating it as data would report
// captions on a stream whose encoder emits the SEI and nothing in it.
func TestParseTS_InvalidPacketsAreNotCaptions(t *testing.T) {
	sei := mediatest.H264SEICaptions([]mediatest.CCPacket{
		{Valid: false, Type: mediatest.CCTypeField1, Data: [2]byte{0x80, 0x80}},
	})
	info, err := ParseTS(mediatest.TSWithSEI(0, 3600, 25, mediatest.SPSFor(1280, 720), sei))
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	tr, _ := info.Track(Video)
	if tr.Captions.Any() {
		t.Errorf("an invalid cc_data_pkt was read as caption data: %+v", tr.Captions)
	}
}

// HEVC carries the same SEI behind a two-byte NAL header, and a prefix SEI is
// type 39 rather than 6.
func TestParseTS_CaptionsInHEVC(t *testing.T) {
	sei := mediatest.HEVCSEICaptions(mediatest.CC608(mediatest.CCTypeField1))
	info, err := ParseTS(mediatest.TSWithHEVCSEI(0, 3600, 25, mediatest.HEVCSPSFor(1280, 720), sei))
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	tr, ok := info.Track(Video)
	if !ok {
		t.Fatal("no video track")
	}
	if !tr.Captions.Field1 {
		t.Errorf("no CEA-608 field 1 found in an HEVC prefix SEI: %+v", tr.Captions)
	}
}

// In fMP4 the NAL units are length-prefixed rather than separated by start codes,
// so the same SEI needs a different walk to reach at all.
func TestParseMP4_CaptionsInSEI(t *testing.T) {
	init := mediatest.MP4Init(1, 90000, "video", 1280, 720)
	sei := mediatest.H264SEICaptions(mediatest.CC608(mediatest.CCTypeField1))
	frag := mediatest.MP4SegmentWithNALUs(1, 1, 0, 3600, 25, [][]byte{
		sei,
		append([]byte{0x65}, make([]byte, 40)...), // an IDR slice
	})
	info, err := ParseMP4(frag, init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, ok := info.Track(Video)
	if !ok {
		t.Fatal("no video track")
	}
	if !tr.Captions.Scanned {
		t.Fatal("the fragment was not scanned for captions")
	}
	if !tr.Captions.Field1 {
		t.Errorf("no CEA-608 field 1 found in the fragment: %+v", tr.Captions)
	}
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Apple's own fMP4 reference stream does not put captions in the video SEI at
// all: it declares a separate CMAF caption track with a c608 sample entry, in the
// same segment as the video. A reader that only walks the video bitstream reports
// "could not look" on the most widely deployed caption delivery there is.
//
// The track states the standard, not the field: attributing a c608 track's data to
// field 1 or field 2 means locating its samples and reading their cdat/cdt2 boxes,
// which is SC-91.
func TestParseMP4_CMAFCaptionTrack(t *testing.T) {
	for _, tc := range []struct {
		entry            string
		want608, want708 bool
	}{
		{"c608", true, false},
		{"c708", false, true},
	} {
		t.Run(tc.entry, func(t *testing.T) {
			init := mediatest.MP4InitWithCaptionTrack(1, 2, 90000, 1280, 720, tc.entry)
			frag := mediatest.MP4SegmentTwoTracks(1, 2, 1, 0, 3600, 25, 25, make([]byte, 400))
			info, err := ParseMP4(frag, init)
			if err != nil {
				t.Fatalf("ParseMP4: %v", err)
			}
			tr, ok := info.Track(Video)
			if !ok {
				t.Fatal("no video track")
			}
			c := tr.Captions
			if !c.Scanned {
				t.Fatal("a segment with a caption track was reported as unscannable")
			}
			if c.Track608 != tc.want608 || c.Track708 != tc.want708 {
				t.Errorf("608/708 track = %v/%v, want %v/%v", c.Track608, c.Track708, tc.want608, tc.want708)
			}
			if !c.Any() {
				t.Error("Any() is false on a segment with a populated caption track")
			}
		})
	}
}

// The defect, in its CMAF form: the caption track is still declared in the init
// and still there in the segment, but it carries no samples. That is precisely
// what an encoder that stopped emitting captions leaves behind, and it is
// invisible to anything that only reads the manifest.
func TestParseMP4_CMAFCaptionTrackWithNoSamples(t *testing.T) {
	init := mediatest.MP4InitWithCaptionTrack(1, 2, 90000, 1280, 720, "c608")
	frag := mediatest.MP4SegmentTwoTracks(1, 2, 1, 0, 3600, 25, 0, make([]byte, 400))
	info, err := ParseMP4(frag, init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, _ := info.Track(Video)
	if !tr.Captions.Scanned {
		t.Fatal("the segment was reported as unscannable")
	}
	if tr.Captions.Any() {
		t.Errorf("an empty caption track was reported as carrying captions: %+v", tr.Captions)
	}
}

// The service list is kept ascending and without repeats, so the report reads the
// same whatever order the DTVCC packets arrived in.
func TestCaptionScanner_ServiceListIsSortedAndUnique(t *testing.T) {
	var s captionScanner
	for _, n := range []int{6, 1, 6, 3, 1, 63} {
		s.addService(n)
	}
	want := []int{1, 3, 6, 63}
	if !sameInts(s.out.Services, want) {
		t.Errorf("services = %v, want %v", s.out.Services, want)
	}
}

// The SEI framing and the ATSC wrapper, at every place a malformed segment can
// stop the walk. None of these may panic, and none may report captions.
func TestCaptionScanner_MalformedInputReportsNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		rbsp []byte
	}{
		{"empty", nil},
		{"a payload type chain that never ends", []byte{0xFF, 0xFF, 0xFF}},
		{"a size chain that never ends", []byte{0x04, 0xFF, 0xFF}},
		{"a size longer than the payload", []byte{0x04, 0x40, 0x01, 0x02}},
		{"user data too short to hold a header", []byte{0x04, 0x03, 0xB5, 0x00, 0x31}},
		{"not the ATSC country code", []byte{0x04, 0x0B, 0x00, 0x00, 0x31, 'G', 'A', '9', '4', 0x03, 0x41, 0xFF, 0x00}},
		{"not GA94", []byte{0x04, 0x0B, 0xB5, 0x00, 0x31, 'X', 'X', 'X', 'X', 0x03, 0x41, 0xFF, 0x00}},
		{"not the cc_data type code", []byte{0x04, 0x0B, 0xB5, 0x00, 0x31, 'G', 'A', '9', '4', 0x09, 0x41, 0xFF, 0x00}},
		{"a cc_count larger than the bytes present", []byte{0x04, 0x0B, 0xB5, 0x00, 0x31, 'G', 'A', '9', '4', 0x03, 0x5F, 0xFF, 0x00}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s captionScanner
			s.readSEI(tc.rbsp)
			s.flush()
			if s.out.Any() {
				t.Errorf("captions reported from malformed input: %+v", s.out)
			}
		})
	}
}

// The DTVCC packet layer's own edges: a packet_size of 0 means 128 bytes, the null
// service ends the packet, and an extended header cut off by the end of the packet
// must stop the walk rather than read past it.
func TestCaptionScanner_DTVCCPacketEdges(t *testing.T) {
	// packet_size 0, then a service 2 block, then the null service as padding.
	var s captionScanner
	s.pkt = []byte{0x00, 2<<5 | 0x01, 0x41, 0x00, 0x00}
	s.flush()
	if !sameInts(s.out.Services, []int{2}) {
		t.Errorf("services = %v, want [2] from a packet_size of 0", s.out.Services)
	}

	// An extended header announced at the last byte of the packet.
	var trunc captionScanner
	trunc.pkt = []byte{0x02, 7<<5 | 0x01}
	trunc.flush()
	if len(trunc.out.Services) != 0 {
		t.Errorf("a truncated extended header gave %v", trunc.out.Services)
	}

	// A packet too short to hold a header at all.
	var tiny captionScanner
	tiny.pkt = []byte{0x02}
	tiny.flush()
	if tiny.out.Any() {
		t.Errorf("a one-byte packet gave %+v", tiny.out)
	}
}

// A length prefix this reader cannot use is not an absence of captions: the walk
// reports that it did not happen, so the check says so rather than blaming the
// stream.
func TestLengthPrefixedCaptions_UnusableInput(t *testing.T) {
	if got := lengthPrefixedCaptions([]byte{0x00, 0x00, 0x00, 0x01, 0x06}, 0, false); got.Scanned {
		t.Error("a zero length prefix was reported as scanned")
	}
	if got := lengthPrefixedCaptions([]byte{0x00, 0x00, 0x00, 0x01, 0x06}, 5, false); got.Scanned {
		t.Error("a five-byte length prefix was reported as scanned")
	}
	// A declared length longer than the bytes present stops the walk, having
	// looked.
	got := lengthPrefixedCaptions([]byte{0x00, 0x00, 0xFF, 0x00, 0x06}, 4, false)
	if !got.Scanned || got.Any() {
		t.Errorf("a truncated NAL gave %+v", got)
	}
}

// The SEI framing uses 0xFF chains for both the type and the size, and a payload
// over 255 bytes is the ordinary case for a caption SEI on a busy line.
func TestSEIMessage_LongChains(t *testing.T) {
	pkts := make([]mediatest.CCPacket, 31)
	for i := range pkts {
		pkts[i] = mediatest.CCPacket{Valid: true, Type: mediatest.CCTypeField2, Data: [2]byte{0x80, 0x80}}
	}
	// 31 triplets plus the wrapper is over 100 bytes; nest it inside a segment and
	// assert the reader still reaches the packets.
	info, err := ParseTS(mediatest.TSWithSEI(0, 3600, 25, mediatest.SPSFor(1280, 720),
		mediatest.H264SEICaptions(pkts)))
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	tr, _ := info.Track(Video)
	if !tr.Captions.Field2 {
		t.Errorf("a long caption SEI was not read: %+v", tr.Captions)
	}
}

// A NAL unit too short to hold an HEVC header must be skipped, not indexed into.
func TestHEVCCaptions_ShortNALU(t *testing.T) {
	got := hevcCaptions([]byte{0x00, 0x00, 0x00, 0x01, 0x4E})
	if got.Any() {
		t.Errorf("a two-byte HEVC NAL gave %+v", got)
	}
}

// The NAL length prefix size, from both boxes that state it and from an entry that
// states neither.
func TestNALLengthSizeFrom(t *testing.T) {
	pad := make([]byte, visualSampleEntrySize)
	avcC := append(append([]byte{}, pad...), 0x00, 0x00, 0x00, 0x0D, 'a', 'v', 'c', 'C', 1, 0x64, 0, 0x28, 0xFD)
	if got := nalLengthSizeFrom(avcC); got != 2 {
		t.Errorf("avcC lengthSizeMinusOne 1 gave %d, want 2", got)
	}
	hvcC := append(append([]byte{}, pad...), 0x00, 0x00, 0x00, 0x1E, 'h', 'v', 'c', 'C')
	hvcC = append(hvcC, make([]byte, 22)...) // the hvcC payload, 22 bytes of it
	hvcC[len(hvcC)-1] = 0xF3                 // byte 21: lengthSizeMinusOne 3
	if got := nalLengthSizeFrom(hvcC); got != 4 {
		t.Errorf("hvcC lengthSizeMinusOne 3 gave %d, want 4", got)
	}
	if got := nalLengthSizeFrom(pad); got != 0 {
		t.Errorf("an entry with no child boxes gave %d, want 0", got)
	}
	if got := nalLengthSizeFrom(append(append([]byte{}, pad...), 0x00, 0x00, 0x00, 0x08, 'b', 't', 'r', 't')); got != 0 {
		t.Errorf("an entry with no avcC or hvcC gave %d, want 0", got)
	}
}

// HEVC in fMP4: the two-byte NAL header and the type-39 prefix SEI, reached
// through the length-prefixed walk rather than Annex-B.
func TestParseMP4_CaptionsInHEVCSEI(t *testing.T) {
	init := mediatest.MP4InitHEVCWithSEI(1, 90000, 1280, 720)
	sei := mediatest.HEVCSEICaptions(mediatest.CC708(2))
	frag := mediatest.MP4SegmentWithNALUs(1, 1, 0, 3600, 25, [][]byte{
		sei,
		append([]byte{19 << 1, 0x01}, make([]byte, 40)...), // an IDR_W_RADL slice
	})
	info, err := ParseMP4(frag, init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	tr, ok := info.Track(Video)
	if !ok {
		t.Fatal("no video track")
	}
	if !sameInts(tr.Captions.Services, []int{2}) {
		t.Errorf("services = %v, want [2]: %+v", tr.Captions.Services, tr.Captions)
	}
}
