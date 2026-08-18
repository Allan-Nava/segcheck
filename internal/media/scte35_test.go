package media

import (
	"strings"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// SC-20: ad-break signalling.
//
// SCTE-35 states where a break begins on the same 90kHz clock the pictures use.
// That is what makes it checkable against the media rather than only against
// itself: a splice point that does not land on a segment boundary is a break no
// player can cut to, however correctly the manifest describes it.

func TestParseTS_SplicePoints(t *testing.T) {
	sps := mediatest.SPSFor(1280, 720)
	tests := []struct {
		name  string
		spec  mediatest.SpliceSpec
		wantT int64
		wantK bool
		out   bool
	}{
		{
			name:  "splice_insert out of network",
			spec:  mediatest.SpliceSpec{Command: mediatest.SpliceInsert, PTS: 270000, OutOfNetwork: true, EventID: 42},
			wantT: 270000, wantK: true, out: true,
		},
		{
			name:  "splice_insert returning to network",
			spec:  mediatest.SpliceSpec{Command: mediatest.SpliceInsert, PTS: 1170000, EventID: 42},
			wantT: 1170000, wantK: true, out: false,
		},
		{
			name:  "time_signal",
			spec:  mediatest.SpliceSpec{Command: mediatest.SpliceTimeSig, PTS: 450000},
			wantT: 450000, wantK: true,
		},
		{
			// pts_adjustment is added by the decoder, so a section a downstream
			// splicer shifted states its real time in neither field alone.
			name:  "pts_adjustment is added to the splice time",
			spec:  mediatest.SpliceSpec{Command: mediatest.SpliceTimeSig, PTS: 450000, PTSAdjustment: 90000},
			wantT: 540000, wantK: true,
		},
		{
			// splice_immediate means "now": there is no time to compare, and
			// inventing one would put the break wherever the section happened to
			// be multiplexed.
			name:  "splice_immediate states no time",
			spec:  mediatest.SpliceSpec{Command: mediatest.SpliceInsert, NoPTS: true, OutOfNetwork: true},
			wantK: false, out: true,
		},
		{
			name:  "a time_signal with no time specified",
			spec:  mediatest.SpliceSpec{Command: mediatest.SpliceTimeSig, NoPTS: true},
			wantK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info, err := ParseTS(mediatest.TSWithSplice(0, 3600, 25, sps, tc.spec))
			if err != nil {
				t.Fatalf("ParseTS: %v", err)
			}
			if len(info.Splices) != 1 {
				t.Fatalf("splices = %d, want 1: %+v", len(info.Splices), info.Splices)
			}
			s := info.Splices[0]
			if s.HasPTS != tc.wantK {
				t.Fatalf("HasPTS = %v, want %v", s.HasPTS, tc.wantK)
			}
			if tc.wantK && s.PTS != tc.wantT {
				t.Errorf("PTS = %d, want %d", s.PTS, tc.wantT)
			}
			if s.OutOfNetwork != tc.out {
				t.Errorf("OutOfNetwork = %v, want %v", s.OutOfNetwork, tc.out)
			}
		})
	}
}

// A splice information PID that carries a command with no timing — a null, or a
// private command this reader does not model — is still a signalling PID, and its
// presence is worth reporting. Inventing a time for it is not.
func TestParseTS_SpliceCommandsWithoutTiming(t *testing.T) {
	sps := mediatest.SPSFor(1280, 720)
	for _, cmd := range []int{mediatest.SpliceNull, mediatest.SplicePrivate} {
		info, err := ParseTS(mediatest.TSWithSplice(0, 3600, 25, sps,
			mediatest.SpliceSpec{Command: cmd}))
		if err != nil {
			t.Fatalf("ParseTS: %v", err)
		}
		if len(info.Splices) != 1 {
			t.Fatalf("command %#x: splices = %d, want 1", cmd, len(info.Splices))
		}
		if info.Splices[0].HasPTS {
			t.Errorf("command %#x: a time was reported where the section states none", cmd)
		}
	}
}

// Several breaks in one segment, and the PID reported as a track so an operator
// can see the signalling is there at all.
func TestParseTS_MultipleSplicesAndTheSignallingTrack(t *testing.T) {
	sps := mediatest.SPSFor(1280, 720)
	info, err := ParseTS(mediatest.TSWithSplice(0, 3600, 25, sps,
		mediatest.SpliceSpec{Command: mediatest.SpliceInsert, PTS: 90000, OutOfNetwork: true, EventID: 1},
		mediatest.SpliceSpec{Command: mediatest.SpliceInsert, PTS: 1440000, EventID: 1},
	))
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	if len(info.Splices) != 2 {
		t.Fatalf("splices = %d, want 2: %+v", len(info.Splices), info.Splices)
	}
	if !info.Splices[0].OutOfNetwork || info.Splices[1].OutOfNetwork {
		t.Errorf("the out and the in were not distinguished: %+v", info.Splices)
	}
	found := false
	for _, tr := range info.Tracks {
		if tr.Codec == "scte35" {
			found = true
		}
	}
	if !found {
		t.Errorf("the splice information PID is not reported as a track: %+v", info.Tracks)
	}
}

// A segment with no splice PID at all reports no splices, and must not invent one
// from a PID that merely happens to carry sections.
func TestParseTS_NoSpliceInformation(t *testing.T) {
	info, err := ParseTS(mediatest.TSWithSPS(0, 3600, 25, mediatest.SPSFor(1280, 720)))
	if err != nil {
		t.Fatalf("ParseTS: %v", err)
	}
	if len(info.Splices) != 0 {
		t.Errorf("splices = %+v, want none", info.Splices)
	}
}

// The section parser's own edges. A malformed section is not a splice point.
func TestParseSpliceSection_Malformed(t *testing.T) {
	good := mediatest.SpliceSection(mediatest.SpliceSpec{
		Command: mediatest.SpliceTimeSig, PTS: 450000,
	})
	if _, ok := parseSpliceSection(good); !ok {
		t.Fatal("a well-formed section was rejected")
	}
	for _, tc := range []struct {
		name string
		sec  []byte
	}{
		{"empty", nil},
		{"not a splice_info_section", append([]byte{0x02}, good[1:]...)},
		{"truncated before the command", good[:9]},
		{"a section_length longer than the bytes present", append([]byte{0xFC, 0x3F, 0xFF}, good[3:]...)},
		// A section_length that fits the buffer but not the fixed header.
		{"a section shorter than its own header", []byte{0xFC, 0x30, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if s, ok := parseSpliceSection(tc.sec); ok {
				t.Errorf("accepted a malformed section as %+v", s)
			}
		})
	}
}

// An encrypted section still says a break is signalled, which is worth reporting;
// its timing is ciphertext, and inventing one would put the break anywhere.
func TestParseSpliceSection_Encrypted(t *testing.T) {
	sec := mediatest.SpliceSection(mediatest.SpliceSpec{Command: mediatest.SpliceTimeSig, PTS: 450000})
	// Set encrypted_packet, the top bit of the byte after protocol_version.
	sec[4] |= 0x80
	sp, ok := parseSpliceSection(sec)
	if !ok {
		t.Fatal("an encrypted section was rejected outright")
	}
	if sp.HasPTS {
		t.Errorf("a time was read out of an encrypted command: %+v", sp)
	}
	if sp.Command != "time_signal" {
		t.Errorf("command = %q, want time_signal: the header is not encrypted", sp.Command)
	}
}

// A cancelled splice_insert withdraws an event that was announced earlier. It
// carries no timing of its own, and reading on past the cancel flag would read the
// fields that are not there.
func TestParseSpliceSection_Cancelled(t *testing.T) {
	sec := mediatest.SpliceSection(mediatest.SpliceSpec{
		Command: mediatest.SpliceInsert, PTS: 450000, EventID: 9,
	})
	// The fixed header is 88 bits, so the command starts at body byte 11: a 32-bit
	// splice_event_id, then splice_event_cancel_indicator as the next top bit.
	sec[3+11+4] |= 0x80
	sp, ok := parseSpliceSection(sec)
	if !ok {
		t.Fatal("a cancelled section was rejected outright")
	}
	if sp.HasPTS {
		t.Errorf("a time was read out of a cancelled event: %+v", sp)
	}
	if sp.EventID != 9 {
		t.Errorf("event id = %d, want 9: a cancellation names what it cancels", sp.EventID)
	}
}

// splice_command_length is bits 68..79 of the section body: the low nibble of
// byte 8 and all of byte 9. Two values of it matter.
func TestParseSpliceSection_CommandLength(t *testing.T) {
	setLen := func(sec []byte, n int) {
		sec[3+8] = sec[3+8]&0xF0 | byte(n>>8)&0x0F
		sec[3+9] = byte(n & 0xFF)
	}

	// 0xFFF means "unknown": read to the end of the section rather than refusing,
	// because a section that does not state its command length still states its
	// command.
	unknown := mediatest.SpliceSection(mediatest.SpliceSpec{Command: mediatest.SpliceTimeSig, PTS: 450000})
	setLen(unknown, 0x0FFF)
	if sp, ok := parseSpliceSection(unknown); !ok || !sp.HasPTS || sp.PTS != 450000 {
		t.Errorf("an unknown command length gave %+v (ok=%v), want the time read to the end", sp, ok)
	}

	// A length that runs past the section is not readable at all. The section still
	// says a break is signalled.
	tooLong := mediatest.SpliceSection(mediatest.SpliceSpec{Command: mediatest.SpliceTimeSig, PTS: 450000})
	setLen(tooLong, 0x100)
	sp, ok := parseSpliceSection(tooLong)
	if !ok {
		t.Fatal("the section was rejected outright")
	}
	if sp.HasPTS {
		t.Errorf("a time was read from a command that is not there: %+v", sp)
	}
	if sp.Command != "time_signal" {
		t.Errorf("command = %q, want time_signal", sp.Command)
	}
}

// Every command type gets a name, so a finding quotes what the section says rather
// than a number an operator has to look up.
func TestSpliceCommandName(t *testing.T) {
	for _, tc := range []struct {
		t    int
		want string
	}{
		{0x00, "splice_null"},
		{0x04, "splice_schedule"},
		{0x05, "splice_insert"},
		{0x06, "time_signal"},
		{0x07, "bandwidth_reservation"},
		{0xFF, "private_command"},
		{0x42, "unknown"},
	} {
		if got := spliceCommandName(tc.t); got != tc.want {
			t.Errorf("spliceCommandName(%#x) = %q, want %q", tc.t, got, tc.want)
		}
	}
}

// The bit reader's own bounds. Running out must be remembered, not returned as a
// zero a caller would read as data.
func TestBitReaderSkipAndTake(t *testing.T) {
	r := &bitReader{data: []byte{0xFF, 0x00}}
	r.skip(20) // past the end
	if !r.err {
		t.Error("skipping past the end did not set err")
	}

	r = &bitReader{data: []byte{0x01, 0x02, 0x03}}
	if got := r.take(2); len(got) != 2 || got[0] != 0x01 {
		t.Errorf("take(2) = %v, want the first two bytes", got)
	}
	if got := r.take(4); got != nil {
		t.Errorf("take past the end = %v, want nil", got)
	}

	// Not byte aligned, so there are no whole bytes to hand out.
	r = &bitReader{data: []byte{0x01, 0x02}}
	r.skip(3)
	if got := r.take(1); got != nil {
		t.Errorf("take at a non-aligned position = %v, want nil", got)
	}

	// A negative count, and a reader already in error.
	r = &bitReader{data: []byte{0x01}}
	if got := r.take(-1); got != nil {
		t.Errorf("take(-1) = %v, want nil", got)
	}
	r.err = true
	if got := r.take(1); got != nil {
		t.Errorf("take on a reader in error = %v, want nil", got)
	}
}

// A splice_time whose 33-bit field is cut off states no time, rather than a
// truncated one.
func TestReadSpliceTime_Truncated(t *testing.T) {
	// time_specified_flag set, then not enough bytes for the timestamp.
	r := &bitReader{data: []byte{0x80, 0x00}}
	if _, ok := readSpliceTime(r); ok {
		t.Error("a truncated splice_time was read as a time")
	}
}

// DASH signals a break inband with an emsg box, which sits beside the moof rather
// than inside it. Version 0 states a delta from the fragment's decode time and
// version 1 an absolute time, on a timescale the box itself declares — so a check
// comparing it to the media has to convert rather than assume 90kHz.
func TestParseMP4_EmsgSplices(t *testing.T) {
	init := mediatest.MP4Init(1, 90000, "video", 1280, 720)
	section := mediatest.SpliceSection(mediatest.SpliceSpec{
		Command: mediatest.SpliceInsert, PTS: 450000, OutOfNetwork: true, EventID: 7,
	})

	t.Run("version 1 states an absolute time", func(t *testing.T) {
		e := mediatest.Emsg(1, mediatest.SCTE35BinScheme, "", 1000, 4500, 7, section)
		info, err := ParseMP4(mediatest.MP4SegmentWithEmsg(1, 1, 180000, 3600, 25, 2000, e), init)
		if err != nil {
			t.Fatalf("ParseMP4: %v", err)
		}
		if len(info.Splices) != 1 {
			t.Fatalf("splices = %+v, want one", info.Splices)
		}
		s := info.Splices[0]
		if !s.HasPTS || s.PTS != 4500 || s.Timescale != 1000 {
			t.Errorf("splice = %+v, want 4500 on a 1000 timescale", s)
		}
		// The message is a splice_info_section, so the command and the direction of
		// the break are readable even though its own splice_time is not the one to
		// use.
		if !s.OutOfNetwork || s.Command != "splice_insert" {
			t.Errorf("the embedded section was not read: %+v", s)
		}
	})

	t.Run("version 0 is a delta from the fragment", func(t *testing.T) {
		e := mediatest.Emsg(0, mediatest.SCTE35BinScheme, "", 90000, 90000, 7, section)
		info, err := ParseMP4(mediatest.MP4SegmentWithEmsg(1, 1, 180000, 3600, 25, 2000, e), init)
		if err != nil {
			t.Fatalf("ParseMP4: %v", err)
		}
		if len(info.Splices) != 1 {
			t.Fatalf("splices = %+v, want one", info.Splices)
		}
		if s := info.Splices[0]; !s.HasPTS || s.PTS != 270000 {
			t.Errorf("splice PTS = %d, want 270000 (the fragment's 180000 plus the delta)", s.PTS)
		}
	})
}

// An emsg under a scheme that is not SCTE-35 is some other kind of event, and
// reporting it as an ad break would invent a break that nobody signalled.
func TestParseMP4_EmsgOtherSchemesAreNotAdBreaks(t *testing.T) {
	init := mediatest.MP4Init(1, 90000, "video", 1280, 720)
	e := mediatest.Emsg(1, "urn:mpeg:dash:event:2012", "1", 1000, 4500, 7, []byte("hello"))
	info, err := ParseMP4(mediatest.MP4SegmentWithEmsg(1, 1, 0, 3600, 25, 2000, e), init)
	if err != nil {
		t.Fatalf("ParseMP4: %v", err)
	}
	if len(info.Splices) != 0 {
		t.Errorf("splices = %+v, want none", info.Splices)
	}
}

// A malformed emsg is not an event. None of these may panic, and none may be read
// as a splice point.
func TestParseEmsg_Malformed(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"too short for a header", []byte{0x01}},
		{"a version this reader does not know", append([]byte{0x09, 0, 0, 0}, make([]byte, 40)...)},
		{"version 1 truncated", append([]byte{0x01, 0, 0, 0}, make([]byte, 8)...)},
		{"version 0 with no string terminator", append([]byte{0x00, 0, 0, 0}, []byte("urn:scte")...)},
		{"version 0 truncated after the strings", append([]byte{0x00, 0, 0, 0}, []byte("a\x00b\x00")...)},
		{"version 1 with no value string", append(append([]byte{0x01, 0, 0, 0}, make([]byte, 20)...), []byte("a\x00")...)},
		{"version 1 with an unterminated scheme", append(append([]byte{0x01, 0, 0, 0}, make([]byte, 20)...), []byte("urn:scte")...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if sp, ok := parseEmsg(tc.body, 0, true); ok {
				t.Errorf("accepted a malformed emsg as %+v", sp)
			}
		})
	}
}

// A version 0 emsg measures its delta from the fragment's decode time. Without a
// tfdt there is nothing to measure from, and reporting the delta as an absolute
// time would place every break at the start of the presentation.
func TestParseEmsg_Version0WithoutABaseHasNoTime(t *testing.T) {
	e := mediatest.Emsg(0, mediatest.SCTE35BinScheme, "", 90000, 90000, 7, nil)
	// Strip the box header to get at the payload the parser sees.
	sp, ok := parseEmsg(e[8:], 0, false)
	if !ok {
		t.Fatal("the emsg was rejected")
	}
	if sp.HasPTS {
		t.Errorf("a time was reported with no fragment to measure it from: %+v", sp)
	}
}

// The emsg string fields, at the edges. A string with no terminator is not a
// string, and a caller that scanned on would read the rest of the segment looking
// for one that is not there.
func TestEmsgString(t *testing.T) {
	got, rest, ok := emsgString([]byte("abc\x00def"))
	if !ok || got != "abc" || string(rest) != "def" {
		t.Errorf("emsgString = %q/%q/%v, want abc/def/true", got, rest, ok)
	}
	if _, _, ok := emsgString([]byte("no terminator")); ok {
		t.Error("a string with no terminator was accepted")
	}
	// A terminator beyond the bound is one this reader will not scan to.
	long := append([]byte(strings.Repeat("a", maxEmsgStringLen+10)), 0x00)
	if _, _, ok := emsgString(long); ok {
		t.Error("a string longer than the bound was accepted")
	}
	if _, _, ok := emsgString(nil); ok {
		t.Error("an empty buffer produced a string")
	}
}

// A version 0 emsg whose scheme terminates but whose value does not.
func TestParseEmsg_Version0UnterminatedValue(t *testing.T) {
	body := append([]byte{0x00, 0, 0, 0}, []byte("urn:scte:scte35:2013:bin\x00no terminator")...)
	if sp, ok := parseEmsg(body, 0, true); ok {
		t.Errorf("accepted an emsg with an unterminated value as %+v", sp)
	}
}

// SC-92: what kind of break this is.
//
// A splice_info_section says when. Its segmentation_descriptor says what: a provider
// advertisement, a distributor placement opportunity, a programme boundary. An
// operator chasing an ad-insertion problem needs the second as much as the first —
// "time_signal at 12.000s" and "Provider Placement Opportunity Start at 12.000s" are
// very different lines in a report.
func TestParseSpliceSection_SegmentationDescriptor(t *testing.T) {
	for _, tc := range []struct {
		name string
		desc mediatest.SegmentationDescriptor
		want string
		upid string
	}{
		{
			name: "provider advertisement start",
			desc: mediatest.SegmentationDescriptor{EventID: 7, TypeID: mediatest.SegTypeProviderAdStart},
			want: "Provider Advertisement Start",
		},
		{
			name: "placement opportunity with a UPID",
			desc: mediatest.SegmentationDescriptor{
				EventID: 9, TypeID: mediatest.SegTypeProviderPlacementOpp,
				UPIDType: 0x0C, UPID: []byte("SIGNAL:abc"),
			},
			want: "Provider Placement Opportunity Start",
			upid: "SIGNAL:abc",
		},
		{
			name: "a duration shifts every field after it",
			desc: mediatest.SegmentationDescriptor{
				EventID: 11, TypeID: mediatest.SegTypeBreakStart, Duration: 30 * 90000,
			},
			want: "Break Start",
		},
		{
			// The delivery restriction fields are five bits either way, but which
			// five depends on the flag — and everything after them moves with it.
			name: "delivery restrictions present",
			desc: mediatest.SegmentationDescriptor{
				EventID: 13, TypeID: mediatest.SegTypeProgramStart, Restricted: true,
			},
			want: "Program Start",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sec := mediatest.SpliceSection(mediatest.SpliceSpec{
				Command: mediatest.SpliceTimeSig, PTS: 1080000,
				Descriptors: []mediatest.SegmentationDescriptor{tc.desc},
			})
			sp, ok := parseSpliceSection(sec)
			if !ok {
				t.Fatal("the section was rejected")
			}
			if sp.SegmentationType != tc.want {
				t.Errorf("segmentation type = %q, want %q", sp.SegmentationType, tc.want)
			}
			if sp.UPID != tc.upid {
				t.Errorf("UPID = %q, want %q", sp.UPID, tc.upid)
			}
			if sp.EventID != tc.desc.EventID {
				t.Errorf("event id = %d, want %d", sp.EventID, tc.desc.EventID)
			}
			if tc.desc.Duration > 0 && sp.Duration != tc.desc.Duration {
				t.Errorf("duration = %d, want %d", sp.Duration, tc.desc.Duration)
			}
		})
	}
}

// A descriptor that is not a segmentation one, and a segmentation one that is not
// CUEI-identified, both leave the section's own timing intact and name no type: a
// descriptor loop is extensible, and reading an unknown entry as a segmentation
// descriptor would name whatever byte happened to sit where the type id goes.
func TestParseSpliceSection_OtherDescriptors(t *testing.T) {
	sec := mediatest.SpliceSection(mediatest.SpliceSpec{Command: mediatest.SpliceTimeSig, PTS: 450000})
	// An avail_descriptor (tag 0x00) rather than a segmentation one.
	withOther := replaceDescriptorLoop(sec, []byte{0x00, 0x08, 'C', 'U', 'E', 'I', 0, 0, 0, 1})
	sp, ok := parseSpliceSection(withOther)
	if !ok {
		t.Fatal("the section was rejected")
	}
	if sp.SegmentationType != "" {
		t.Errorf("segmentation type = %q, want none from an avail_descriptor", sp.SegmentationType)
	}
	if !sp.HasPTS || sp.PTS != 450000 {
		t.Errorf("the command's own timing was lost: %+v", sp)
	}

	// A segmentation descriptor whose identifier is not CUEI.
	bad := replaceDescriptorLoop(sec, []byte{0x02, 0x0A, 'X', 'X', 'X', 'X', 0, 0, 0, 1, 0x00, 0x00})
	if sp, _ := parseSpliceSection(bad); sp.SegmentationType != "" {
		t.Errorf("segmentation type = %q, want none from a foreign identifier", sp.SegmentationType)
	}

	// A descriptor whose declared length runs past the loop.
	short := replaceDescriptorLoop(sec, []byte{0x02, 0x40, 'C', 'U', 'E', 'I'})
	if sp, _ := parseSpliceSection(short); sp.SegmentationType != "" {
		t.Errorf("segmentation type = %q, want none from a truncated descriptor", sp.SegmentationType)
	}
}

// replaceDescriptorLoop rewrites a section's descriptor loop, so a test can plant a
// descriptor the builder does not model.
func replaceDescriptorLoop(sec, loop []byte) []byte {
	// The loop sits after the command, whose length the header states, and is
	// followed by the CRC.
	cmdLen := int(sec[3+8]&0x0F)<<8 | int(sec[3+9])
	head := sec[:3+11+cmdLen]
	body := append(append([]byte{}, head[3:]...), byte(len(loop)>>8), byte(len(loop)))
	body = append(body, loop...)
	body = append(body, 0xDE, 0xAD, 0xBE, 0xEF)
	out := []byte{0xFC, 0x30 | byte(len(body)>>8), byte(len(body) & 0xFF)}
	return append(out, body...)
}

// The segmentation descriptor's own edges. Every one of these shifts a later field or
// refuses a value, and a reader that got one wrong names whatever byte happened to
// sit where the type id goes.
func TestReadSegmentationDescriptor_Edges(t *testing.T) {
	desc := func(body []byte) SplicePoint {
		var out SplicePoint
		readSegmentationDescriptor(body, &out)
		return out
	}
	cuei := []byte{'C', 'U', 'E', 'I'}

	// A cancelled event withdraws one announced earlier and states nothing else.
	cancelled := append(append([]byte{}, cuei...), 0, 0, 0, 7, 0x80)
	if got := desc(cancelled); got.SegmentationType != "" {
		t.Errorf("a cancelled descriptor named %q", got.SegmentationType)
	}

	// Truncated before the type id.
	if got := desc(append(append([]byte{}, cuei...), 0, 0, 0, 7)); got.SegmentationType != "" {
		t.Errorf("a truncated descriptor named %q", got.SegmentationType)
	}

	// A type id the table does not hold is reported as its number: more use to an
	// operator than a wrong word.
	unknown := mediatest.SpliceSection(mediatest.SpliceSpec{
		Command: mediatest.SpliceTimeSig, PTS: 90000,
		Descriptors: []mediatest.SegmentationDescriptor{{EventID: 1, TypeID: 0x7E}},
	})
	sp, _ := parseSpliceSection(unknown)
	if sp.SegmentationType != "segmentation type 0x7e" {
		t.Errorf("segmentation type = %q, want the number", sp.SegmentationType)
	}

	// A component-level descriptor: program_segmentation_flag clear, so a component
	// count and one 40-bit offset per component precede everything else. Written as
	// bytes because the flags land on a byte boundary here.
	component := append(append([]byte{}, cuei...),
		0, 0, 0, 21, // segmentation_event_id
		0x00, // not cancelled
		// program_segmentation 0, duration_flag 0, delivery_not_restricted 1, reserved
		0x3F,
		0x01,                   // component_count
		0x03,                   // component_tag
		0x00, 0x00, 0x00, 0x00, // reserved(7) + pts_offset(33), five bytes in all
		0x00,
		0x00, // segmentation_upid_type
		0x00, // segmentation_upid_length
		0x22, // segmentation_type_id: Break Start
		0x01, // segment_num
		0x01, // segments_expected
	)
	if got := desc(component); got.SegmentationType != "Break Start" {
		t.Errorf("a component-level descriptor named %q, want Break Start", got.SegmentationType)
	}
}

// A UPID is text for half the types in the table and opaque bytes for the rest, and
// one printed as if it were UTF-8 is unreadable either way.
func TestFormatUPID(t *testing.T) {
	if got := formatUPID(0x0C, []byte("SIGNAL:x")); got != "SIGNAL:x" {
		t.Errorf("a text UPID = %q, want it as text", got)
	}
	// A type that is text, carrying bytes that are not.
	if got := formatUPID(0x0C, []byte{0x00, 0xFF}); got != "00ff" {
		t.Errorf("unprintable bytes under a text type = %q, want hex", got)
	}
	// A type that is opaque bytes whatever they look like.
	if got := formatUPID(0x06, []byte("abcd")); got != "61626364" {
		t.Errorf("an opaque UPID = %q, want hex", got)
	}
	if printableASCII(nil) {
		t.Error("an empty UPID was called printable")
	}
}

// SC-96: the segmentation descriptor, decoded by hand from the specification.
//
// Every other assertion about this reader goes through mediatest's builder, and a builder
// and a reader written from the same misreading of the layout would agree with each other.
// No public stream carries a segmentation descriptor to check against — livesim2's
// sections have an empty descriptor loop — so these bytes are laid out field by field with
// the bit boundaries written down, and the reader has to agree with the specification
// rather than with its twin.
func TestReadSegmentationDescriptor_HandDecoded(t *testing.T) {
	// splice_descriptor_tag 0x02, then descriptor_length, then:
	//
	//   identifier                          32  "CUEI"
	//   segmentation_event_id               32  0x0000002A = 42
	//   segmentation_event_cancel_indicator  1  0
	//   reserved                             7  0
	//   program_segmentation_flag            1  1
	//   segmentation_duration_flag           1  1
	//   delivery_not_restricted_flag         1  1
	//   reserved                             5  all ones
	//   segmentation_duration               40  0x00002932E0 = 2700000 ticks = 30s
	//   segmentation_upid_type               8  0x0C  (MPU, text in practice)
	//   segmentation_upid_length             8  0x0A
	//   segmentation_upid                   80  "SIGNAL:abc"
	//   segmentation_type_id                 8  0x34  Provider Placement Opportunity Start
	//   segment_num                          8  0x01
	//   segments_expected                    8  0x01
	//
	// The three flags and the five reserved bits share one byte: 1,1,1 then 11111 is
	// 0xFF. Getting that byte wrong is what moves the type id, which is the whole
	// reason it is spelled out here.
	body := []byte{
		'C', 'U', 'E', 'I',
		0x00, 0x00, 0x00, 0x2A,
		0x00,                         // not cancelled, reserved
		0xFF,                         // program_segmentation, duration_flag, not_restricted, reserved
		0x00, 0x00, 0x29, 0x32, 0xE0, // segmentation_duration, 40 bits
		0x0C, // upid type
		0x0A, // upid length
		'S', 'I', 'G', 'N', 'A', 'L', ':', 'a', 'b', 'c',
		0x34, // segmentation_type_id
		0x01, // segment_num
		0x01, // segments_expected
	}
	var got SplicePoint
	readSegmentationDescriptor(body, &got)

	if got.SegmentationType != "Provider Placement Opportunity Start" {
		t.Errorf("segmentation type = %q", got.SegmentationType)
	}
	if got.EventID != 42 {
		t.Errorf("event id = %d, want 42", got.EventID)
	}
	if got.UPID != "SIGNAL:abc" {
		t.Errorf("UPID = %q, want SIGNAL:abc", got.UPID)
	}
	if got.Duration != 2700000 {
		t.Errorf("duration = %d ticks, want 2700000 (30s at 90kHz)", got.Duration)
	}

	// The same bytes with the delivery restriction flag cleared: the five bits after it
	// are the restriction fields rather than reserved, so nothing moves — but a reader
	// that skipped a different number of them would land on the wrong type id, and this
	// is the pair that catches it.
	restricted := append([]byte{}, body...)
	restricted[9] = 0xC7 // program_segmentation 1, duration 1, not_restricted 0, then 00111
	var got2 SplicePoint
	readSegmentationDescriptor(restricted, &got2)
	if got2.SegmentationType != "Provider Placement Opportunity Start" {
		t.Errorf("with restrictions present: segmentation type = %q", got2.SegmentationType)
	}

	// And the whole thing through the section reader, so the descriptor loop's own
	// framing is in the same test.
	descriptor := append([]byte{0x02, byte(len(body))}, body...)
	sec := mediatest.SpliceSection(mediatest.SpliceSpec{Command: mediatest.SpliceTimeSig, PTS: 1080000})
	full := replaceDescriptorLoop(sec, descriptor)
	sp, ok := parseSpliceSection(full)
	if !ok {
		t.Fatal("the section carrying a hand-decoded descriptor was rejected")
	}
	if sp.SegmentationType != "Provider Placement Opportunity Start" || sp.UPID != "SIGNAL:abc" {
		t.Errorf("through the section reader: %+v", sp)
	}
	if !sp.HasPTS || sp.PTS != 1080000 {
		t.Errorf("the command's own timing was lost: %+v", sp)
	}
}
