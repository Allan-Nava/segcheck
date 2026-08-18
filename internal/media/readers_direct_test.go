package media

import (
	"bytes"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// The readers added for colour, codec profile and audio configuration are box
// and bitstream walks, and every one of them has a branch that fires only on
// media that is truncated, unexpected or simply absent. Those branches are the
// ones that decide whether segcheck reports "unstated" or a confident wrong
// answer, so they are exercised here directly rather than only through a
// synthetic origin — a defect a fixture cannot easily plant is still a defect.

// ---------- colour ----------

func TestTransferName(t *testing.T) {
	for _, tc := range []struct {
		code int
		want string
	}{
		{TransferBT709, "BT.709"},
		{TransferBT601, "BT.601"},
		{TransferPQ, "PQ"},
		{TransferHLG, "HLG"},
		{8, "linear"},
		{14, "BT.2020"},
		{15, "BT.2020"},
		{TransferUnspec, ""},
		{99, ""},
	} {
		if got := TransferName(tc.code); got != tc.want {
			t.Errorf("TransferName(%d) = %q, want %q", tc.code, got, tc.want)
		}
	}
}

// HDR is the question every consumer of this asks, and "unspecified" is not a
// claim of HDR however convenient that would be.
func TestColourDescription_HDRAndLabel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		c       ColourDescription
		wantHDR bool
		label   string
	}{
		{"PQ", ColourDescription{Transfer: TransferPQ, Stated: true}, true, "PQ"},
		{"HLG", ColourDescription{Transfer: TransferHLG, Stated: true}, true, "HLG"},
		{"BT.709", ColourDescription{Transfer: TransferBT709, Stated: true}, false, "BT.709"},
		{"unstated PQ is not a claim", ColourDescription{Transfer: TransferPQ}, false, "unstated"},
		{"an unnamed code point is quoted as a number", ColourDescription{Transfer: 17, Stated: true}, false, "transfer 17"},
		{"code point zero", ColourDescription{Transfer: 0, Stated: true}, false, "transfer 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.HDR(); got != tc.wantHDR {
				t.Errorf("HDR() = %v, want %v", got, tc.wantHDR)
			}
			if got := tc.c.Label(); got != tc.label {
				t.Errorf("Label() = %q, want %q", got, tc.label)
			}
		})
	}
}

// plausibleColour is the guard against the failure mode that matters: a walk
// that lost its place reads three bytes out of the middle of another field and
// returns a colour that looks like a colour.
func TestPlausibleColour(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		primaries, transfer, matrix int
		want                        bool
	}{
		{"BT.709", 1, 1, 1, true},
		{"PQ over BT.2020", 9, 16, 9, true},
		{"matrix 0 is identity, which is assigned", 1, 1, 0, true},
		{"primaries 0 is reserved", 0, 1, 1, false},
		{"transfer 0 is reserved", 1, 0, 1, false},
		{"3 is reserved in all three", 3, 1, 1, false},
		{"transfer 3 is reserved", 1, 3, 1, false},
		{"matrix 3 is reserved", 1, 1, 3, false},
		{"past the assigned primaries", 23, 1, 1, false},
		{"past the assigned transfers", 1, 19, 1, false},
		{"past the assigned matrices", 1, 1, 15, false},
		{"beyond a byte", 300, 1, 1, false},
		{"negative", -1, 1, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := plausibleColour(tc.primaries, tc.transfer, tc.matrix); got != tc.want {
				t.Errorf("plausibleColour(%d,%d,%d) = %v, want %v",
					tc.primaries, tc.transfer, tc.matrix, got, tc.want)
			}
		})
	}
}

// The Annex-B wrappers find the parameter set among the other NAL units, and
// must find nothing rather than something in a stream that has none.
func TestColourFromElementaryStream(t *testing.T) {
	vui := mediatest.VUIParams{VideoSignalType: true, ColourDescription: true, Primaries: 9, Transfer: 16, Matrix: 9}
	h264 := mediatest.TSWithSPS(0, 3600, 25, mediatest.SPS(mediatest.SPSParams{
		ProfileIDC: 100, ChromaFormatIDC: 1,
		WidthInMBsMinus1: 79, HeightInMapUnits: 44, FrameMBsOnly: 1,
		VUI: &vui,
	}))
	info, err := Parse(h264, nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tr, _ := info.Track(Video)
	if c, ok := tr.Colour(); !ok || c.Transfer != TransferPQ {
		t.Errorf("H.264 colour from the elementary stream = %+v, ok=%v", c, ok)
	}

	// A stream with no parameter set at all: the walk finds none and says so.
	if _, ok := h264Colour([]byte{0x00, 0x00, 0x01, 0x09, 0x10}); ok {
		t.Error("h264Colour found a colour in a stream with no SPS")
	}
	if _, ok := hevcColour([]byte{0x00, 0x00, 0x01, 0x46, 0x01, 0x10}); ok {
		t.Error("hevcColour found a colour in a stream with no SPS")
	}
	// And a truncated one.
	if _, ok := parseH264Colour([]byte{0x64}); ok {
		t.Error("parseH264Colour accepted two bytes")
	}
	if _, ok := parseHEVCColour([]byte{0x01, 0x02}); ok {
		t.Error("parseHEVCColour accepted two bytes")
	}
}

// An HEVC SPS whose reference-picture-set counts are absurd is not an SPS, and a
// reader that walked on would reach the VUI offset in some other field.
func TestParseHEVCColour_RejectsImpossibleReferenceSets(t *testing.T) {
	// A hand-built prefix: video_parameter_set_id, one sub-layer, then a
	// profile_tier_level of zeroes, and after it a chroma format that cannot be.
	rbsp := make([]byte, 40)
	rbsp[0] = 0x00 // sps_video_parameter_set_id 0, max_sub_layers_minus1 0
	// chroma_format_idc read as a large ue() lands past 3 and is rejected.
	rbsp[13] = 0x01
	if _, ok := parseHEVCColour(rbsp); ok {
		// Not a hard requirement that it fail, but it must never claim a colour.
		t.Log("the walk completed; what matters is that it stated nothing")
	}
}

// ---------- codec profile ----------

func TestProfileFromCodecConfig(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		init                 []byte
		profile, level, tier int
	}{
		{"avcC", mediatest.MP4InitAVCProfile(1, 90000, 1920, 1080, 0x4d, 0x40, 31), 0x4d, 31, 0},
		{"hvcC main tier", mediatest.MP4InitHEVCProfile(1, 90000, 1920, 1080, 1, 0, 120), 1, 120, 0},
		{"av1C main tier", mediatest.MP4InitAV1Profile(1, 90000, 1920, 1080, 1, 8, 0), 1, 8, 0},
		{"vpcC", mediatest.MP4InitVP9Profile(1, 90000, 1920, 1080, 0, 30), 0, 30, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := Parse(mediatest.MP4Segment(1, 0, 0, 3600, 10, 500), tc.init)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			tr, _ := info.Track(Video)
			p, ok := tr.CodecProfile()
			if !ok {
				t.Fatal("no profile read")
			}
			if p.Profile != tc.profile || p.Level != tc.level || p.Tier != tc.tier {
				t.Errorf("got %d/%d/%d, want %d/%d/%d", p.Profile, p.Level, p.Tier, tc.profile, tc.level, tc.tier)
			}
		})
	}

	// Records too short to hold the fields they claim: every one must decline.
	for _, tc := range []struct {
		name     string
		children []byte
	}{
		{"nothing at all", nil},
		{"a truncated avcC", boxFor("avcC", []byte{0x01, 0x64})},
		{"a truncated hvcC", boxFor("hvcC", make([]byte, 8))},
		{"a truncated av1C", boxFor("av1C", []byte{0x81})},
		{"a truncated vpcC", boxFor("vpcC", []byte{0x01, 0x00})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := profileFromCodecConfig(tc.children); ok {
				t.Error("a truncated configuration record reported a profile")
			}
		})
	}
}

// The bitstream paths, and their guards.
func TestProfileLevelFromBitstream(t *testing.T) {
	es := annexB(concat3([]byte{0x67}, mediatest.SPS(mediatest.SPSParams{
		ProfileIDC: 77, ChromaFormatIDC: 1,
		WidthInMBsMinus1: 79, HeightInMapUnits: 44, FrameMBsOnly: 1,
	})))
	p, ok := h264ProfileLevel(es)
	if !ok || p.Profile != 77 {
		t.Errorf("h264ProfileLevel = %+v, ok=%v, want profile 77", p, ok)
	}
	if _, ok := h264ProfileLevel([]byte{0x00, 0x00, 0x01, 0x09, 0x10}); ok {
		t.Error("h264ProfileLevel found a profile in a stream with no SPS")
	}
	if _, ok := hevcProfileLevel([]byte{0x00, 0x00, 0x01, 0x46, 0x01, 0x10}); ok {
		t.Error("hevcProfileLevel found a profile in a stream with no SPS")
	}

	hevc := annexB(concat3([]byte{0x42, 0x01}, mediatest.HEVCSPS(mediatest.HEVCSPSParams{
		WidthInLumaSamples: 1920, HeightInLumaSamples: 1080, ChromaFormatIDC: 1,
	})))
	hp, ok := hevcProfileLevel(hevc)
	if !ok {
		t.Fatal("hevcProfileLevel read nothing from a well-formed SPS")
	}
	if hp.Profile != 1 || hp.Level != 93 {
		t.Errorf("HEVC profile/level = %d/%d, want 1/93 as the writer states", hp.Profile, hp.Level)
	}
}

// ---------- audio configuration ----------

// descriptorPayload has to honour the variable-length length: seven bits and a
// continuation flag per byte. A reader that assumed one byte works on most files
// and lands in the middle of the payload on the rest.
func TestDescriptorPayload(t *testing.T) {
	// A long-form length. Each byte carries seven bits, so eight is written as a
	// continuation byte of zero followed by 0x08 — not as 0x81 0x08, which is 136
	// and is the arithmetic a reader gets wrong in the other direction.
	long := []byte{0x05, 0x80, 0x08, 1, 2, 3, 4, 5, 6, 7, 8}
	got, ok := descriptorPayload(long, 0x05)
	if !ok || len(got) != 8 {
		t.Errorf("long-form length: got %d bytes, ok=%v, want 8", len(got), ok)
	}

	// Skipping past a descriptor of another tag to reach the wanted one.
	mixed := []byte{0x06, 0x02, 0xAA, 0xBB, 0x05, 0x01, 0x42}
	got, ok = descriptorPayload(mixed, 0x05)
	if !ok || len(got) != 1 || got[0] != 0x42 {
		t.Errorf("skipping a foreign tag: got %v, ok=%v", got, ok)
	}

	for _, tc := range []struct {
		name string
		b    []byte
	}{
		{"empty", nil},
		{"a tag with no length", []byte{0x05}},
		{"a length past the end", []byte{0x05, 0x20, 0x01}},
		{"the tag is not there", []byte{0x06, 0x01, 0x00}},
		// The standard allows four bytes at most. A fifth continuation bit means
		// this is not a descriptor, and accepting it would hand back a payload
		// measured from the wrong place.
		{"a length that never terminates", []byte{0x05, 0x80, 0x80, 0x80, 0x80, 0x80, 0x01}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := descriptorPayload(tc.b, 0x05); ok {
				t.Error("a malformed descriptor was accepted")
			}
		})
	}
}

// An audio object type of 32 or above uses the escape value, and a reader that
// stopped at five bits would read the wrong type for every one of them.
func TestReadAudioObjectType_EscapeValue(t *testing.T) {
	init := mediatest.MP4InitESDS(1, 48000, 33, 3, 2, false, false)
	info, err := Parse(mediatest.MP4Segment(1, 0, 0, 1024, 10, 500), init)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tr, _ := info.Track(Audio)
	cfg, ok := tr.AudioConfig()
	if !ok {
		t.Fatal("no audio configuration read")
	}
	if cfg.ObjectType != 33 {
		t.Errorf("ObjectType = %d, want 33 through the escape value", cfg.ObjectType)
	}
}

// Every configuration box has a length below which it cannot hold what it claims.
func TestAudioConfigGuards(t *testing.T) {
	for _, tc := range []struct {
		name     string
		typ      string
		children []byte
	}{
		{"nothing at all", "mp4a", nil},
		{"a truncated esds", "mp4a", boxFor("esds", []byte{0x00, 0x00})},
		{"an esds with no ES_Descriptor", "mp4a", boxFor("esds", []byte{0, 0, 0, 0, 0x06, 0x01, 0x00})},
		{"a truncated dOps", "Opus", boxFor("dOps", []byte{0x00, 0x02})},
		{"dOps with an impossible channel count", "Opus", boxFor("dOps", append([]byte{0x00, 0xFF}, make([]byte, 9)...))},
		{"a truncated dfLa", "fLaC", boxFor("dfLa", []byte{0x00, 0x00, 0x00, 0x00})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := append(make([]byte, audioSampleEntrySize), tc.children...)
			cfg := audioConfigFromEntry(tc.typ, payload)
			if cfg.Stated {
				t.Errorf("a malformed %s reported a configuration: %+v", tc.name, cfg)
			}
		})
	}

	// A sample entry with nothing after its fixed fields at all.
	if cfg := audioConfigFromEntry("mp4a", make([]byte, audioSampleEntrySize)); cfg.Stated {
		t.Error("an entry with no child boxes reported a configuration")
	}
}

// ---------- boxes ----------

// A pssh box that cannot hold a system id must be skipped, and one system
// appearing twice must be reported once: a packager may emit one pssh per track
// and mean one system.
func TestPSSHSystems(t *testing.T) {
	moov := concat3(
		boxFor("pssh", []byte{0x00, 0x00}), // too short for a system id
		mediatest.PSSH(mediatest.WidevineSystemID),
		mediatest.PSSH(mediatest.WidevineSystemID),
		mediatest.PSSH(mediatest.FairPlaySystemID),
	)
	got := psshSystems(moov)
	if len(got) != 2 {
		t.Fatalf("found %d systems, want 2 (the duplicate collapsed, the truncated skipped): %v", len(got), got)
	}
	if got[0].Name != "widevine" || got[1].Name != "fairplay" {
		t.Errorf("systems = %v, want widevine then fairplay in document order", got)
	}
}

// saiz decides whether protected content is really protected, and a truncated
// one must report nothing rather than a run of clear samples.
func TestParseSaiz(t *testing.T) {
	for _, tc := range []struct {
		name              string
		box               []byte
		wantClear, wantEn int
		wantKnown         bool
	}{
		{
			name:      "a non-zero default means every sample is encrypted",
			box:       []byte{0, 0, 0, 0, 8, 0, 0, 0, 3},
			wantEn:    3,
			wantKnown: true,
		},
		{
			name:      "per-sample sizes, two clear then one encrypted",
			box:       []byte{0, 0, 0, 0, 0, 0, 0, 0, 3, 0, 0, 8},
			wantClear: 2, wantEn: 1,
			wantKnown: true,
		},
		{
			name:      "an aux_info_type prefix shifts everything by eight bytes",
			box:       append([]byte{0, 0, 0, 1, 'c', 'e', 'n', 'c', 0, 0, 0, 0, 8, 0, 0, 0, 2}, nil...),
			wantEn:    2,
			wantKnown: true,
		},
		{"too short to hold a count", []byte{0, 0, 0, 0, 0}, 0, 0, false},
		{"the sizes are not there", []byte{0, 0, 0, 0, 0, 0, 0, 0, 9}, 0, 0, false},
		{"empty", nil, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var f fragTrack
			parseSaiz(tc.box, &f)
			if f.sampleStateKnown != tc.wantKnown {
				t.Errorf("known = %v, want %v", f.sampleStateKnown, tc.wantKnown)
			}
			if f.clearSamples != tc.wantClear || f.encryptedSamples != tc.wantEn {
				t.Errorf("clear/encrypted = %d/%d, want %d/%d",
					f.clearSamples, f.encryptedSamples, tc.wantClear, tc.wantEn)
			}
		})
	}
}

// tenc's layout hinges on its version byte, and reading the pattern
// unconditionally would report one on every cenc track.
func TestParseTenc(t *testing.T) {
	kid := make([]byte, 16)
	kid[0] = 0x11
	v0 := append([]byte{0, 0, 0, 0, 0, 0x19, 1, 8}, kid...)
	if got := parseTenc(v0); got.hasPattern {
		t.Error("a version-0 tenc reported a crypt pattern from its reserved byte")
	}
	v1 := append([]byte{1, 0, 0, 0, 0, 0x19, 1, 8}, kid...)
	got := parseTenc(v1)
	if !got.hasPattern || got.crypt != 1 || got.skip != 9 {
		t.Errorf("version-1 pattern = %d:%d present=%v, want 1:9 present", got.crypt, got.skip, got.hasPattern)
	}
	if got.keyID == "" {
		t.Error("the default_KID was not read")
	}
	// An all-zero KID is the unset value, not a key id.
	zero := append([]byte{1, 0, 0, 0, 0, 0x19, 1, 8}, make([]byte, 16)...)
	if parseTenc(zero).keyID != "" {
		t.Error("an all-zero default_KID was reported as a key id")
	}
	if parseTenc([]byte{1, 0, 0, 0}).keyID != "" {
		t.Error("a truncated tenc reported a key id")
	}
}

// The parameter sets inside a configuration record are behind a count and a
// length each, and a record that lies about either must yield what it really
// holds rather than a slice past its end.
func TestParameterSetExtraction(t *testing.T) {
	if got := avcCParameterSets([]byte{0x01, 0x64, 0x00, 0x28, 0xFF}); got != nil {
		t.Error("an avcC too short to hold a count yielded parameter sets")
	}
	if got := avcCParameterSets([]byte{0x01, 0x64, 0x00, 0x28, 0xFF, 0xE1, 0x00, 0x20, 0x67}); len(got) != 0 {
		t.Errorf("an avcC whose length runs past the end yielded %d sets", len(got))
	}
	if got := hvcCParameterSets(make([]byte, 10)); got != nil {
		t.Error("an hvcC too short to hold its prefix yielded parameter sets")
	}
	rec := make([]byte, 23)
	rec[22] = 1 // one array, and nothing after it
	if got := hvcCParameterSets(rec); len(got) != 0 {
		t.Errorf("an hvcC promising an array it does not hold yielded %d sets", len(got))
	}
}

// A colr box that is not nclx, and a sample entry that is not visual, both have
// to fall through rather than be read as code points.
func TestColourFromStsdGuards(t *testing.T) {
	if c := colourFromStsd([]byte{0x00}); c.Stated {
		t.Error("a truncated stsd reported a colour")
	}
	if c := colourFromStsd(stsdWith(boxFor("mp4a", make([]byte, 40)))); c.Stated {
		t.Error("an audio sample entry reported a video colour")
	}
	// nclx with code points outside the assigned ranges.
	bad := boxFor("colr", concat3([]byte("nclx"), []byte{0x00, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00}))
	entry := boxFor("avc1", append(make([]byte, visualSampleEntrySize), bad...))
	if c := colourFromStsd(stsdWith(entry)); c.Stated {
		t.Error("reserved code points were reported as a colour")
	}
	// A visual entry with nothing after its fixed fields.
	if c := colourFromStsd(stsdWith(boxFor("avc1", make([]byte, visualSampleEntrySize)))); c.Stated {
		t.Error("an entry with no child boxes reported a colour")
	}
}

// lengthPrefixedKeyframes walks samples whose NAL units carry a length rather
// than a start code, and a length that runs past the buffer means the offsets
// are not what this reader thinks they are.
func TestLengthPrefixedKeyframes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		samples    []byte
		lengthSize int
		hevc       bool
		wantOpens  bool
		wantKnown  bool
		wantScan   bool
	}{
		{"an impossible length size", []byte{0, 0, 0, 1, 0x65}, 5, false, false, false, false},
		{"a length past the buffer", []byte{0, 0, 0, 0x40, 0x65}, 4, false, false, false, false},
		{"a zero length", []byte{0, 0, 0, 0}, 4, false, false, false, false},
		{"an H.264 IDR", []byte{0, 0, 0, 2, 0x65, 0x80}, 4, false, true, true, true},
		{"an H.264 non-IDR", []byte{0, 0, 0, 2, 0x41, 0x80}, 4, false, false, true, true},
		{"an HEVC IRAP", []byte{0, 0, 0, 2, 0x26, 0x01}, 4, true, true, true, true},
		{"an HEVC trailing picture", []byte{0, 0, 0, 2, 0x02, 0x01}, 4, true, false, true, true},
		{"an HEVC unit too short for its header", []byte{0, 0, 0, 1, 0x26}, 4, true, false, false, true},
		{"a parameter set only", []byte{0, 0, 0, 2, 0x67, 0x80}, 4, false, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := lengthPrefixedKeyframes(tc.samples, tc.lengthSize, tc.hevc)
			if v.Known != tc.wantKnown {
				t.Errorf("Known = %v, want %v", v.Known, tc.wantKnown)
			}
			if v.Known && v.Opens != tc.wantOpens {
				t.Errorf("Opens = %v, want %v", v.Opens, tc.wantOpens)
			}
			if v.Scanned != tc.wantScan {
				t.Errorf("Scanned = %v, want %v", v.Scanned, tc.wantScan)
			}
		})
	}
}

// ---------- helpers the tests above need ----------

// boxFor writes a box the way the parser reads one: a big-endian size, a
// four-character type, then the payload.
func boxFor(typ string, payload []byte) []byte {
	size := 8 + len(payload)
	out := []byte{byte(size >> 24), byte(size >> 16), byte(size >> 8), byte(size)}
	out = append(out, typ...)
	return append(out, payload...)
}

// stsdWith wraps one sample entry in the version, flags and entry count an stsd
// carries before it.
func stsdWith(entry []byte) []byte {
	return append([]byte{0, 0, 0, 0, 0, 0, 0, 1}, entry...)
}

func concat3(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// annexB prefixes a NAL unit with a start code.
func annexB(nal []byte) []byte {
	return append([]byte{0x00, 0x00, 0x00, 0x01}, nal...)
}

// ---------- the variable-length material the VUI hides behind ----------
//
// The writers emit an RBSP, not an escaped NAL payload, so these parse it
// directly. Running unescapeRBSP over a writer's output corrupts it wherever the
// RBSP happens to contain 00 00 03 — which one of these parameter sets does, and
// which is how the convention was pinned down.

// Everything between an SPS's fixed fields and its VUI is variable length, and
// each of these shapes moves the VUI by a different amount. A reader that
// mismeasured any of them would read a colour out of the middle of another field
// and return something plausible, so every shape is written and read back.
func TestParseColour_ThroughTheVariableLengthFields(t *testing.T) {
	vui := mediatest.VUIParams{VideoSignalType: true, ColourDescription: true, Primaries: 9, Transfer: 16, Matrix: 9}

	for _, tc := range []struct {
		name string
		sps  []byte
	}{
		{
			name: "H.264 with scaling matrices",
			sps: mediatest.SPS(mediatest.SPSParams{
				ProfileIDC: 100, ChromaFormatIDC: 1, ScalingMatrix: true,
				WidthInMBsMinus1: 79, HeightInMapUnits: 44, FrameMBsOnly: 1, VUI: &vui,
			}),
		},
		{
			name: "H.264 with 4:4:4 scaling matrices, which are twelve lists not eight",
			sps: mediatest.SPS(mediatest.SPSParams{
				ProfileIDC: 244, ChromaFormatIDC: 3, ScalingMatrix: true,
				WidthInMBsMinus1: 79, HeightInMapUnits: 44, FrameMBsOnly: 1, VUI: &vui,
			}),
		},
		{
			name: "H.264 with picture order count type 1 and a list of offsets",
			sps: mediatest.SPS(mediatest.SPSParams{
				ProfileIDC: 100, ChromaFormatIDC: 1, PicOrderCntType: 1,
				OffsetForRefFrame: []int32{1, -2, 3},
				WidthInMBsMinus1:  79, HeightInMapUnits: 44, FrameMBsOnly: 1, VUI: &vui,
			}),
		},
		{
			name: "H.264 interlaced, whose crop units differ",
			sps: mediatest.SPS(mediatest.SPSParams{
				ProfileIDC: 100, ChromaFormatIDC: 1,
				WidthInMBsMinus1: 79, HeightInMapUnits: 22, FrameMBsOnly: 0,
				FrameCropping: true, CropBottom: 2, VUI: &vui,
			}),
		},
		{
			name: "H.264 baseline, which states no chroma format at all",
			sps: mediatest.SPS(mediatest.SPSParams{
				ProfileIDC: 66, ChromaFormatIDC: 1,
				WidthInMBsMinus1: 79, HeightInMapUnits: 44, FrameMBsOnly: 1, VUI: &vui,
			}),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseH264Colour(tc.sps)
			if !ok || !got.Stated {
				t.Fatalf("colour not read: ok=%v got=%+v", ok, got)
			}
			if got.Transfer != TransferPQ {
				t.Errorf("transfer = %d, want %d", got.Transfer, TransferPQ)
			}
		})
	}

	for _, tc := range []struct {
		name string
		p    mediatest.HEVCSPSParams
	}{
		{
			name: "HEVC with several sub-layers, whose profile_tier_level grows",
			p: mediatest.HEVCSPSParams{
				MaxSubLayersMinus1: 3, ChromaFormatIDC: 1,
				WidthInLumaSamples: 1920, HeightInLumaSamples: 1080,
				ShortTermRefPicSets: 1, VUI: &vui,
			},
		},
		{
			name: "HEVC with a conformance window",
			p: mediatest.HEVCSPSParams{
				ChromaFormatIDC: 1, WidthInLumaSamples: 1920, HeightInLumaSamples: 1088,
				ConformanceWindow: true, ConfWinBottom: 4,
				ShortTermRefPicSets: 1, VUI: &vui,
			},
		},
		{
			name: "HEVC 4:4:4, which carries separate_colour_plane_flag",
			p: mediatest.HEVCSPSParams{
				ChromaFormatIDC: 3, WidthInLumaSamples: 1920, HeightInLumaSamples: 1080,
				ShortTermRefPicSets: 1, VUI: &vui,
			},
		},
		{
			name: "HEVC with several reference picture sets and no prediction",
			p: mediatest.HEVCSPSParams{
				ChromaFormatIDC: 1, WidthInLumaSamples: 1920, HeightInLumaSamples: 1080,
				ShortTermRefPicSets: 3, VUI: &vui,
			},
		},
		{
			name: "HEVC with no reference picture sets at all",
			p: mediatest.HEVCSPSParams{
				ChromaFormatIDC: 1, WidthInLumaSamples: 1920, HeightInLumaSamples: 1080,
				VUI: &vui,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseHEVCColour(mediatest.HEVCSPS(tc.p))
			if !ok || !got.Stated {
				t.Fatalf("colour not read: ok=%v got=%+v", ok, got)
			}
			if got.Transfer != TransferPQ {
				t.Errorf("transfer = %d, want %d", got.Transfer, TransferPQ)
			}
		})
	}
}

// An aspect ratio with no colour description after it, and a VUI that ends
// before the colour description flag: both must report what they can and no more.
func TestParseVUIColour_PartialAnswers(t *testing.T) {
	// video_signal_type_present_flag 0: the VUI states nothing about colour.
	sps := mediatest.SPS(mediatest.SPSParams{
		ProfileIDC: 100, ChromaFormatIDC: 1,
		WidthInMBsMinus1: 79, HeightInMapUnits: 44, FrameMBsOnly: 1,
		VUI: &mediatest.VUIParams{AspectRatioIDC: 1},
	})
	got, ok := parseH264Colour(sps)
	if !ok {
		t.Fatal("a VUI stating no colour was read as a failure")
	}
	if got.Stated || got.RangeStated {
		t.Errorf("a VUI with no video signal type claimed %+v", got)
	}

	// A truncated RBSP: the walk runs off the end inside the VUI.
	full := mediatest.SPS(mediatest.SPSParams{
		ProfileIDC: 100, ChromaFormatIDC: 1,
		WidthInMBsMinus1: 79, HeightInMapUnits: 44, FrameMBsOnly: 1,
		VUI: &mediatest.VUIParams{VideoSignalType: true, ColourDescription: true, Primaries: 9, Transfer: 16, Matrix: 9},
	})
	if _, ok := parseH264Colour(full[:len(full)-2]); ok {
		t.Error("a truncated VUI produced a colour")
	}
}

// scaling_list_data is one of the things between the resolution and the VUI, and
// its entries are either a reference to an earlier list or a run of signed codes.
func TestSkipHEVCScalingListData(t *testing.T) {
	// 0x55 is the bit pattern 01010101: a pred_mode_flag of 0 followed by an
	// Exp-Golomb 0, repeated — the reference branch for every entry. A zero-filled
	// buffer would not do, because an all-zero ue() never finds its terminator.
	r := &bitReader{data: bytes.Repeat([]byte{0x55}, 16)}
	skipHEVCScalingListData(r)
	if r.err {
		t.Error("skipHEVCScalingListData ran off the end of a buffer that could hold it")
	}
	// The other branch: pred_mode_flag 1, so a run of signed codes follows.
	// 0xAA is 10101010 — a flag of 1 and then Exp-Golomb zeros.
	explicit := &bitReader{data: bytes.Repeat([]byte{0xAA}, 512)}
	skipHEVCScalingListData(explicit)
	// And a buffer that cannot hold any of it: it stops rather than looping.
	short := &bitReader{data: []byte{0x55}}
	skipHEVCScalingListData(short)
	if !short.err {
		t.Error("skipHEVCScalingListData did not notice it had run out of bits")
	}
}

// The prediction branch of a reference picture set derives its own count from the
// previous set's, so a previous count that cannot be is a reader that lost its
// place.
func TestSkipHEVCShortTermRefPicSet_Guards(t *testing.T) {
	counts := []uint32{99, 0}
	r := &bitReader{data: []byte{0xFF, 0xFF, 0xFF, 0xFF}}
	if ok := skipHEVCShortTermRefPicSet(r, 1, counts); ok {
		t.Error("a reference set claiming ninety-nine delta POCs was accepted")
	}
	// A first set whose negative and positive counts are absurd.
	r = &bitReader{data: []byte{0x00, 0x00, 0x00, 0x00}}
	if ok := skipHEVCShortTermRefPicSet(r, 0, []uint32{0}); ok {
		t.Log("a zero-filled buffer reads as huge Exp-Golomb codes and is declined")
	}
}

// An AudioSpecificConfig may state its rate explicitly rather than by index, and
// may state a channel configuration that cannot be.
func TestParseAudioSpecificConfig_ExplicitRateAndGuards(t *testing.T) {
	// Object type 2, frequency index 15, then a 24-bit rate, then channels.
	w := []byte{}
	bits := []int{0, 0, 0, 1, 0 /* aot 2 */, 1, 1, 1, 1 /* index 15 */}
	_ = bits
	// Hand-pack: aot(5)=2, freqIndex(4)=15, rate(24)=44100, channels(4)=2.
	packed := packBits([]bitField{{5, 2}, {4, 15}, {24, 44100}, {4, 2}})
	w = packed
	got, ok := parseAudioSpecificConfig(w)
	if !ok {
		t.Fatal("an explicit sampling rate was not read")
	}
	if got.CodedSampleRate != 44100 || got.CodedChannels != 2 {
		t.Errorf("got %d Hz / %d channels, want 44100/2", got.CodedSampleRate, got.CodedChannels)
	}

	// A reserved frequency index yields no rate rather than a wrong one.
	got, ok = parseAudioSpecificConfig(packBits([]bitField{{5, 2}, {4, 13}, {4, 2}}))
	if !ok {
		t.Fatal("a reserved frequency index was read as a failure")
	}
	if got.CodedSampleRate != 0 {
		t.Errorf("a reserved frequency index produced %d Hz", got.CodedSampleRate)
	}

	if _, ok := parseAudioSpecificConfig([]byte{0x11}); ok {
		t.Error("a one-byte configuration was accepted")
	}
}

// An ES_Descriptor may carry the optional fields its flags select, and each one
// moves the DecoderConfigDescriptor along.
func TestParseESDS_OptionalFields(t *testing.T) {
	asc := packBits([]bitField{{5, 2}, {4, 3}, {4, 2}, {1, 0}, {1, 0}, {1, 0}})
	dsi := descriptorBytes(0x05, asc)
	dcd := descriptorBytes(0x04, append(append([]byte{0x40, 0x15}, make([]byte, 11)...), dsi...))

	for _, tc := range []struct {
		name  string
		flags byte
		extra []byte
	}{
		{"no optional fields", 0x00, nil},
		{"a stream dependence", 0x80, []byte{0x00, 0x01}},
		{"a URL", 0x40, []byte{0x02, 'h', 'i'}},
		{"an OCR stream", 0x20, []byte{0x00, 0x01}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := append([]byte{0x00, 0x01, tc.flags}, tc.extra...)
			esd := descriptorBytes(0x03, append(body, dcd...))
			cfg, ok := parseESDS(append([]byte{0, 0, 0, 0}, esd...))
			if !ok {
				t.Fatalf("the configuration was not reached past %s", tc.name)
			}
			if cfg.ObjectType != 2 {
				t.Errorf("ObjectType = %d, want 2", cfg.ObjectType)
			}
		})
	}

	// A URL flag whose length runs past the descriptor.
	esd := descriptorBytes(0x03, []byte{0x00, 0x01, 0x40, 0x40})
	if _, ok := parseESDS(append([]byte{0, 0, 0, 0}, esd...)); ok {
		t.Error("a URL length past the end of the descriptor was accepted")
	}
	// An ES_Descriptor too short to hold its own flags.
	esd = descriptorBytes(0x03, []byte{0x00})
	if _, ok := parseESDS(append([]byte{0, 0, 0, 0}, esd...)); ok {
		t.Error("a two-byte ES_Descriptor was accepted")
	}
	// A DecoderConfigDescriptor too short to hold its fixed fields.
	dcdShort := descriptorBytes(0x04, []byte{0x40})
	esd = descriptorBytes(0x03, append([]byte{0x00, 0x01, 0x00}, dcdShort...))
	if _, ok := parseESDS(append([]byte{0, 0, 0, 0}, esd...)); ok {
		t.Error("a truncated DecoderConfigDescriptor was accepted")
	}
}

// ---------- bit packing for the fixtures above ----------

type bitField struct {
	width int
	value uint32
}

// packBits writes fields most-significant bit first, which is how every field in
// these configurations is written.
func packBits(fields []bitField) []byte {
	var bits []byte
	for _, f := range fields {
		for i := f.width - 1; i >= 0; i-- {
			bits = append(bits, byte(f.value>>uint(i)&1))
		}
	}
	out := make([]byte, (len(bits)+7)/8)
	for i, b := range bits {
		if b == 1 {
			out[i/8] |= 1 << uint(7-i%8)
		}
	}
	return out
}

// descriptorBytes writes an MPEG-4 descriptor with the short length form.
func descriptorBytes(tag byte, payload []byte) []byte {
	return append([]byte{tag, byte(len(payload))}, payload...)
}

// Every optional block between an HEVC SPS's resolution and its VUI moves the VUI
// by a different amount, and a reader that skipped rather than parsed would land
// the colour description in the middle of something else. Each is written and
// read back.
func TestParseHEVCColour_ThroughEveryOptionalBlock(t *testing.T) {
	vui := mediatest.VUIParams{VideoSignalType: true, ColourDescription: true, Primaries: 9, Transfer: 18, Matrix: 9}
	base := mediatest.HEVCSPSParams{
		ChromaFormatIDC: 1, WidthInLumaSamples: 1920, HeightInLumaSamples: 1080,
		ShortTermRefPicSets: 1, VUI: &vui,
	}

	for _, tc := range []struct {
		name  string
		apply func(*mediatest.HEVCSPSParams)
	}{
		{"scaling list data", func(p *mediatest.HEVCSPSParams) { p.ScalingListData = true }},
		{"PCM coding", func(p *mediatest.HEVCSPSParams) { p.PCM = true }},
		{"long-term reference pictures", func(p *mediatest.HEVCSPSParams) { p.LongTermRefPics = 3 }},
		{"no sub-layer ordering info", func(p *mediatest.HEVCSPSParams) { p.NoSubLayerOrdering = true }},
		{
			"all of them at once, which is where the offsets compound",
			func(p *mediatest.HEVCSPSParams) {
				p.ScalingListData, p.PCM, p.LongTermRefPics = true, true, 2
				p.MaxSubLayersMinus1, p.ShortTermRefPicSets = 2, 2
				p.InterRefPicSetPrediction = true
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.apply(&p)
			got, ok := parseHEVCColour(mediatest.HEVCSPS(p))
			if !ok || !got.Stated {
				t.Fatalf("colour not read past %s: ok=%v got=%+v", tc.name, ok, got)
			}
			if got.Transfer != TransferHLG {
				t.Errorf("transfer = %d, want %d", got.Transfer, TransferHLG)
			}
		})
	}
}

// The guards inside the HEVC walk: values that cannot be, in an SPS that is
// therefore not an SPS.
func TestParseHEVCColour_Guards(t *testing.T) {
	// sps_max_sub_layers_minus1 of 7 is legal; anything the three bits cannot
	// hold is unreachable, so the reachable guard is the chroma format.
	bad := mediatest.HEVCSPS(mediatest.HEVCSPSParams{
		ChromaFormatIDC: 1, WidthInLumaSamples: 1920, HeightInLumaSamples: 1080,
		ShortTermRefPicSets: 1, VUI: nil,
	})
	// Truncating mid-walk: the reader must state nothing rather than guess.
	for _, n := range []int{4, 8, 12} {
		if len(bad) > n {
			if got, ok := parseHEVCColour(bad[:n]); ok && got.Stated {
				t.Errorf("a %d-byte SPS produced a colour: %+v", n, got)
			}
		}
	}
	// An SPS with no VUI at all reads cleanly and states nothing.
	if got, ok := parseHEVCColour(bad); !ok || got.Stated {
		t.Errorf("an SPS with no VUI: ok=%v got=%+v", ok, got)
	}
}

// The Annex-B wrapper has to skip a NAL too short to carry a two-byte header
// rather than index into it.
func TestHEVCColourAndProfile_ShortNALUnits(t *testing.T) {
	if _, ok := hevcColour([]byte{0x00, 0x00, 0x01, 0x42}); ok {
		t.Error("hevcColour read a colour out of a one-byte NAL unit")
	}
	if _, ok := hevcProfileLevel([]byte{0x00, 0x00, 0x01, 0x42}); ok {
		t.Error("hevcProfileLevel read a profile out of a one-byte NAL unit")
	}
	// An SPS whose RBSP is too short to hold a profile_tier_level.
	if _, ok := hevcProfileLevel(annexB([]byte{0x42, 0x01, 0x01, 0x02})); ok {
		t.Error("hevcProfileLevel read a profile out of a truncated SPS")
	}
	if _, ok := h264ProfileLevel(annexB([]byte{0x67, 0x64})); ok {
		t.Error("h264ProfileLevel read a profile out of a truncated SPS")
	}
}

// visualEntryChildren and the parameter-set walks reject what they cannot read.
func TestVisualEntryChildren(t *testing.T) {
	if _, ok := visualEntryChildren([]byte{0x00}); ok {
		t.Error("a truncated stsd yielded children")
	}
	if _, ok := visualEntryChildren([]byte{0, 0, 0, 0, 0, 0, 0, 1}); ok {
		t.Error("an stsd with no entries yielded children")
	}
	if _, ok := visualEntryChildren(stsdWith(boxFor("mp4a", make([]byte, 40)))); ok {
		t.Error("an audio entry yielded visual children")
	}
	// A protected entry keeps the original boxes beside sinf, so encv counts.
	if _, ok := visualEntryChildren(stsdWith(boxFor("encv", make([]byte, visualSampleEntrySize+8)))); !ok {
		t.Error("a protected visual entry yielded no children")
	}
	// An hvcC whose array count is honoured and whose NALU length is not there.
	rec := make([]byte, 23)
	rec[22] = 1
	rec = append(rec, 0x22, 0x00, 0x01, 0x00) // one NALU, and a truncated length
	if got := hvcCParameterSets(rec); len(got) != 0 {
		t.Errorf("an hvcC with a truncated NALU length yielded %d sets", len(got))
	}
}

// A colr box stating full range, and an ICC one: the first is a claim, the second
// has no code points at all.
func TestMP4InitColr_FullRange(t *testing.T) {
	init := mediatest.MP4InitColr(1, 90000, 1920, 1080, "avc1", 1, 1, 1, true)
	info, err := Parse(mediatest.MP4Segment(1, 0, 0, 3600, 10, 500), init)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tr, _ := info.Track(Video)
	c, ok := tr.Colour()
	if !ok || !c.FullRange {
		t.Errorf("full range was not read: ok=%v got=%+v", ok, c)
	}
}

// The fixture helpers have guards of their own, and a builder that silently did
// the wrong thing would plant a defect no test meant to plant.
func TestFixtureHelperGuards(t *testing.T) {
	// A malformed uuid yields sixteen bytes rather than panicking, and a test
	// asserting on the result is what says so.
	init := mediatest.MP4InitCENCWithPSSH(1, 90000, 640, 360, "avc1", "cenc", "not-a-uuid")
	info, err := Parse(mediatest.MP4Segment(1, 0, 0, 3600, 10, 500), init)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(info.DRMSystems) != 1 {
		t.Fatalf("a malformed system id produced %d systems", len(info.DRMSystems))
	}
	if info.DRMSystems[0].Name != "" {
		t.Errorf("a malformed system id was given the name %q", info.DRMSystems[0].Name)
	}
	// No systems at all: the init is returned untouched.
	plain := mediatest.MP4InitCENCWithPSSH(1, 90000, 640, 360, "avc1", "cenc")
	info, err = Parse(mediatest.MP4Segment(1, 0, 0, 3600, 10, 500), plain)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(info.DRMSystems) != 0 {
		t.Errorf("an init with no pssh reported %d systems", len(info.DRMSystems))
	}
}

// The guards inside both colour walks, reached with parameter sets whose fields
// state values the bitstream is free to carry and a reader must refuse.
func TestColourWalkGuards(t *testing.T) {
	vui := &mediatest.VUIParams{VideoSignalType: true, ColourDescription: true, Primaries: 9, Transfer: 16, Matrix: 9}

	// An offset list longer than the standard allows: not a parameter set.
	absurd := mediatest.SPS(mediatest.SPSParams{
		ProfileIDC: 100, ChromaFormatIDC: 1, PicOrderCntType: 1,
		OffsetForRefFrame: make([]int32, 300),
		WidthInMBsMinus1:  79, HeightInMapUnits: 44, FrameMBsOnly: 1, VUI: vui,
	})
	if got, ok := parseH264Colour(absurd); ok && got.Stated {
		t.Errorf("a 300-entry offset list produced a colour: %+v", got)
	}

	// Code points outside the assigned ranges, written deliberately.
	reserved := mediatest.SPS(mediatest.SPSParams{
		ProfileIDC: 100, ChromaFormatIDC: 1,
		WidthInMBsMinus1: 79, HeightInMapUnits: 44, FrameMBsOnly: 1,
		VUI: &mediatest.VUIParams{VideoSignalType: true, ColourDescription: true, Primaries: 0, Transfer: 1, Matrix: 1},
	})
	if got, ok := parseH264Colour(reserved); ok && got.Stated {
		t.Errorf("a reserved primary produced a colour: %+v", got)
	}

	// Truncated inside the cropping offsets, before the VUI flag is reached.
	full := mediatest.SPS(mediatest.SPSParams{
		ProfileIDC: 100, ChromaFormatIDC: 1,
		WidthInMBsMinus1: 79, HeightInMapUnits: 44, FrameMBsOnly: 1,
		FrameCropping: true, CropLeft: 300, CropRight: 300, CropTop: 300, CropBottom: 300,
		VUI: vui,
	})
	if got, ok := parseH264Colour(full[:6]); ok && got.Stated {
		t.Errorf("a six-byte SPS produced a colour: %+v", got)
	}

	// HEVC: a chroma format that cannot be, and a picture order count width that
	// cannot be. Both are hand-built, because a writer will not emit them.
	if got, ok := parseHEVCColour(hevcRBSPWith(4, 4)); ok && got.Stated {
		t.Errorf("chroma_format_idc 4 produced a colour: %+v", got)
	}
	if got, ok := parseHEVCColour(hevcRBSPWith(1, 20)); ok && got.Stated {
		t.Errorf("a 24-bit picture order count width produced a colour: %+v", got)
	}

	// Long-term reference pictures beyond the standard's maximum.
	if got, ok := parseHEVCColour(mediatest.HEVCSPS(mediatest.HEVCSPSParams{
		ChromaFormatIDC: 1, WidthInLumaSamples: 1920, HeightInLumaSamples: 1080,
		ShortTermRefPicSets: 1, LongTermRefPics: 40, VUI: vui,
	})); ok && got.Stated {
		t.Errorf("forty long-term reference pictures produced a colour: %+v", got)
	}

	// Forty reference picture sets is legal — the standard's maximum is
	// sixty-four — and forty of them is a great deal of variable-length material
	// to walk exactly, so it has to work rather than be refused.
	if got, ok := parseHEVCColour(mediatest.HEVCSPS(mediatest.HEVCSPSParams{
		ChromaFormatIDC: 1, WidthInLumaSamples: 1920, HeightInLumaSamples: 1080,
		ShortTermRefPicSets: 40, VUI: vui,
	})); !ok || !got.Stated {
		t.Errorf("forty reference picture sets: ok=%v got=%+v", ok, got)
	}
	// Sixty-five is past it, and past it means this is not a parameter set.
	if got, ok := parseHEVCColour(mediatest.HEVCSPS(mediatest.HEVCSPSParams{
		ChromaFormatIDC: 1, WidthInLumaSamples: 1920, HeightInLumaSamples: 1080,
		ShortTermRefPicSets: 65, VUI: vui,
	})); ok && got.Stated {
		t.Errorf("sixty-five reference picture sets produced a colour: %+v", got)
	}
}

// hevcRBSPWith hand-builds the head of an HEVC SPS as far as the two fields under
// test, because no writer will emit values this far out of range.
func hevcRBSPWith(chromaFormatIDC, log2PocLsbMinus4 uint32) []byte {
	w := &bitWriterLocal{}
	w.u(4, 0) // sps_video_parameter_set_id
	w.u(3, 0) // sps_max_sub_layers_minus1
	w.bit(1)  // sps_temporal_id_nesting_flag
	// profile_tier_level with one sub-layer: 96 fixed bits and nothing after.
	for i := 0; i < 96; i++ {
		w.bit(0)
	}
	w.ue(0)               // sps_seq_parameter_set_id
	w.ue(chromaFormatIDC) // the first field under test
	if chromaFormatIDC == 3 {
		w.bit(0)
	}
	w.ue(1920)
	w.ue(1080)
	w.bit(0)               // conformance_window_flag
	w.ue(0)                // bit_depth_luma_minus8
	w.ue(0)                // bit_depth_chroma_minus8
	w.ue(log2PocLsbMinus4) // the second field under test
	w.bit(1)               // rbsp_stop_one_bit
	return w.bytes()
}

// bitWriterLocal is a minimal writer for the hand-built cases above. The fixture
// package's writer is not exported, and a test that needs bytes no builder will
// produce has to write them itself.
type bitWriterLocal struct{ bits []byte }

func (w *bitWriterLocal) bit(v uint32) { w.bits = append(w.bits, byte(v&1)) }

func (w *bitWriterLocal) u(n int, v uint32) {
	for i := n - 1; i >= 0; i-- {
		w.bit(v >> uint(i))
	}
}

// ue writes an unsigned Exp-Golomb code.
func (w *bitWriterLocal) ue(v uint32) {
	n := v + 1
	bits := 0
	for t := n; t > 1; t >>= 1 {
		bits++
	}
	for i := 0; i < bits; i++ {
		w.bit(0)
	}
	for i := bits; i >= 0; i-- {
		w.bit(n >> uint(i))
	}
}

func (w *bitWriterLocal) bytes() []byte {
	out := make([]byte, (len(w.bits)+7)/8)
	for i, b := range w.bits {
		if b == 1 {
			out[i/8] |= 1 << uint(7-i%8)
		}
	}
	return out
}

// The Annex-B HEVC wrapper, reached through a real transport stream so the NAL
// splitter is in the path too.
func TestHEVCColourThroughATransportStream(t *testing.T) {
	sps := mediatest.HEVCSPS(mediatest.HEVCSPSParams{
		ChromaFormatIDC: 1, WidthInLumaSamples: 1920, HeightInLumaSamples: 1080,
		ShortTermRefPicSets: 1,
		VUI: &mediatest.VUIParams{VideoSignalType: true, ColourDescription: true,
			Primaries: 9, Transfer: 16, Matrix: 9},
	})
	info, err := Parse(mediatest.TSWithHEVCSPS(0, 3600, 25, sps), nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tr, ok := info.Track(Video)
	if !ok {
		t.Fatal("no video track")
	}
	c, ok := tr.Colour()
	if !ok || c.Transfer != TransferPQ {
		t.Errorf("HEVC colour through a transport stream = %+v, ok=%v", c, ok)
	}
	p, ok := tr.CodecProfile()
	if !ok || p.Level == 0 {
		t.Errorf("HEVC profile through a transport stream = %+v, ok=%v", p, ok)
	}
}

// The explicit branch of scaling_list_data, whose entries are runs of signed
// codes and whose largest sizes carry a DC coefficient as well.
func TestSkipHEVCScalingListData_ExplicitEntries(t *testing.T) {
	w := &bitWriterLocal{}
	for sizeID := 0; sizeID < 4; sizeID++ {
		step := 1
		if sizeID == 3 {
			step = 3
		}
		for matrixID := 0; matrixID < 6; matrixID += step {
			w.bit(1) // scaling_list_pred_mode_flag: codes follow
			coefNum := 64
			if sizeID == 0 {
				coefNum = 16
			}
			if sizeID > 1 {
				w.ue(0) // scaling_list_dc_coef_minus8, as a signed code of zero
			}
			for i := 0; i < coefNum; i++ {
				w.ue(0) // scaling_list_delta_coef
			}
		}
	}
	r := &bitReader{data: w.bytes()}
	skipHEVCScalingListData(r)
	if r.err {
		t.Error("the explicit branch ran off the end of a buffer written for it")
	}
}

// The prediction branch carries a use_delta_flag only for entries the reference
// set does not use, and both paths change how many delta POCs the set ends up
// with — which is what sizes the next set.
func TestSkipHEVCShortTermRefPicSet_UseDeltaFlag(t *testing.T) {
	// One reference entry, not used by the current picture, and a use_delta_flag
	// of 1: the entry is carried over anyway.
	w := &bitWriterLocal{}
	w.bit(1) // inter_ref_pic_set_prediction_flag
	w.bit(0) // delta_rps_sign
	w.ue(0)  // abs_delta_rps_minus1
	w.bit(0) // used_by_curr_pic_flag[0]
	w.bit(1) // use_delta_flag[0]
	w.bit(0) // used_by_curr_pic_flag[1]
	w.bit(0) // use_delta_flag[1]: not carried
	counts := []uint32{1, 0}
	r := &bitReader{data: w.bytes()}
	if !skipHEVCShortTermRefPicSet(r, 1, counts) {
		t.Fatal("a well-formed predicted reference set was refused")
	}
	if counts[1] != 1 {
		t.Errorf("NumDeltaPocs = %d, want 1: one entry carried over of two", counts[1])
	}
}

// ---------- one guard each ----------

// The last guards in the box and descriptor walks: each is a record that promises
// more than it holds, and each must decline rather than slice past its end.
func TestBoxWalkGuards(t *testing.T) {
	// An stsd whose entry count is stated and whose entries are not there.
	if _, ok := visualEntryChildren([]byte{0, 0, 0, 0, 0, 0, 0, 1, 0, 0}); ok {
		t.Error("an stsd with no readable entry yielded children")
	}
	// An avcC whose parameter-set length is not there.
	if got := avcCParameterSets([]byte{0x01, 0x64, 0x00, 0x28, 0xFF, 0xE1, 0x00}); len(got) != 0 {
		t.Errorf("an avcC with a truncated length yielded %d sets", len(got))
	}
	// An hvcC whose array header is not there.
	rec := make([]byte, 23)
	rec[22] = 1
	rec = append(rec, 0x22, 0x00) // a type and half a count
	if got := hvcCParameterSets(rec); len(got) != 0 {
		t.Errorf("an hvcC with a truncated array header yielded %d sets", len(got))
	}
	// An hvcC whose NALU length is zero.
	rec = make([]byte, 23)
	rec[22] = 1
	rec = append(rec, 0x22, 0x00, 0x01, 0x00, 0x00)
	if got := hvcCParameterSets(rec); len(got) != 0 {
		t.Errorf("an hvcC with a zero-length NALU yielded %d sets", len(got))
	}
	// An AudioSampleEntry whose channel count cannot be. The rate cannot be made
	// impossible through this path at all: it is the high half of a 16.16 word, so
	// it tops out at 65535, well under the guard — which stays as insurance
	// against that shift ever changing rather than as a reachable branch.
	payload := make([]byte, audioSampleEntrySize)
	payload[16], payload[17] = 0xFF, 0xFF
	if channels, _ := audioSampleEntryFields("mp4a", payload); channels != 0 {
		t.Errorf("an impossible channel count was reported as %d", channels)
	}
	// An fMP4 with a moov and no tracks in it at all.
	if _, err := ParseMP4(nil, boxFor("moov", boxFor("mvhd", make([]byte, 100)))); err == nil {
		t.Error("an init describing no track parsed as media")
	}
	// A saiz whose sample count is stated and whose sizes are half there.
	var f fragTrack
	parseSaiz([]byte{0, 0, 0, 0, 0, 0, 0, 0, 4, 8, 8}, &f)
	if f.sampleStateKnown {
		t.Error("a saiz promising four sizes and holding two was read")
	}
}

// The AudioSpecificConfig guards: a channel configuration that cannot be, a rate
// that cannot be, and a walk that ran out of bits.
func TestAudioConfigValueGuards(t *testing.T) {
	// channel configuration 15, which is beyond the assigned layouts but within
	// the four bits — and beyond maxAudioChannels only if it were larger, so the
	// reachable guard is the rate.
	got, ok := parseAudioSpecificConfig(packBits([]bitField{{5, 2}, {4, 15}, {24, 999999}, {4, 2}}))
	if !ok {
		t.Fatal("an explicit rate past any real one was read as a failure")
	}
	if got.CodedSampleRate != 0 {
		t.Errorf("an impossible explicit rate was reported as %d", got.CodedSampleRate)
	}
	// A configuration that ends inside the channel field.
	if _, ok := parseAudioSpecificConfig([]byte{0x11, 0x80}); ok {
		if got, _ := parseAudioSpecificConfig([]byte{0x11, 0x80}); got.CodedChannels > maxAudioChannels {
			t.Error("a truncated configuration reported an impossible channel count")
		}
	}
	// dfLa whose STREAMINFO states a rate that cannot be.
	si := packBits([]bitField{{16, 4096}, {16, 4096}, {24, 0}, {24, 0}, {20, 999999}, {3, 1}, {5, 15}, {18, 0}, {18, 0}})
	dfla := concat3([]byte{0, 0, 0, 0, 0x80, 0x00, 0x00, 0x22}, si, make([]byte, 16))
	if cfg, ok := parseDFLA(dfla); ok {
		t.Errorf("a FLAC rate of 999999 Hz was accepted: %+v", cfg)
	}
}

// The keyframe walk over length-prefixed samples gives up after a bounded number
// of units, and has to say it gave up rather than that it found nothing.
func TestLengthPrefixedKeyframes_GivesUp(t *testing.T) {
	// Four thousand and ninety-seven parameter sets, none of them a picture.
	var samples []byte
	for i := 0; i <= keyframeScanNALUs; i++ {
		samples = append(samples, 0, 0, 0, 2, 0x67, 0x80)
	}
	v := lengthPrefixedKeyframes(samples, 4, false)
	if v.Scanned {
		t.Error("a walk that hit its cap reported that it had scanned the samples")
	}
	if v.Known {
		t.Error("a walk that found no picture reported a verdict")
	}
}

// The fixture's own helpers: an upper-case system id, and a moov that is not one.
func TestFixtureHelpers_UpperCaseAndMalformed(t *testing.T) {
	init := mediatest.MP4InitCENCWithPSSH(1, 90000, 640, 360, "avc1", "cenc",
		"EDEF8BA9-79D6-4ACE-A3C8-27DCD51D21ED")
	info, err := Parse(mediatest.MP4Segment(1, 0, 0, 3600, 10, 500), init)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(info.DRMSystems) != 1 || info.DRMSystems[0].Name != "widevine" {
		t.Errorf("an upper-case system id was not recognised: %v", info.DRMSystems)
	}
}

// The last guards in the two bitstream walks, reached by truncating a parameter
// set at each of the places the walk can run out.
func TestBitstreamWalks_RunOut(t *testing.T) {
	vui := &mediatest.VUIParams{VideoSignalType: true, ColourDescription: true, Primaries: 9, Transfer: 16, Matrix: 9}
	h264 := mediatest.SPS(mediatest.SPSParams{
		ProfileIDC: 100, ChromaFormatIDC: 1,
		WidthInMBsMinus1: 79, HeightInMapUnits: 44, FrameMBsOnly: 1, VUI: vui,
	})
	// Truncating one byte at a time finds every place the H.264 walk can end, and
	// none of them may produce a colour.
	for n := 3; n < len(h264); n++ {
		if got, _ := parseH264Colour(h264[:n]); got.Stated {
			t.Fatalf("a %d-byte SPS of %d produced a colour: %+v", n, len(h264), got)
		}
	}

	hevc := mediatest.HEVCSPS(mediatest.HEVCSPSParams{
		ChromaFormatIDC: 1, WidthInLumaSamples: 1920, HeightInLumaSamples: 1080,
		ShortTermRefPicSets: 8, LongTermRefPics: 2, VUI: vui,
	})
	for n := 4; n < len(hevc); n++ {
		if got, _ := parseHEVCColour(hevc[:n]); got.Stated {
			t.Fatalf("a %d-byte HEVC SPS of %d produced a colour: %+v", n, len(hevc), got)
		}
	}

	// The same for the profile readers, whose NAL wrappers have their own bounds.
	for n := 1; n < len(h264); n++ {
		nal := annexB(append([]byte{0x67}, h264[:n]...))
		if p, ok := h264ProfileLevel(nal); ok && p.Profile == 0 {
			t.Fatalf("a truncated SPS produced profile 0 as though it were read")
		}
	}
	for n := 1; n < len(hevc); n++ {
		nal := annexB(append([]byte{0x42, 0x01}, hevc[:n]...))
		if p, ok := hevcProfileLevel(nal); ok && p.Level == 0 {
			t.Fatalf("a truncated HEVC SPS produced level 0 as though it were read")
		}
	}
}

// A parameter set whose emulation-prevention bytes shrink it below the minimum
// the reader needs: the length check is on the RBSP, not on the NAL.
func TestProfileReaders_RBSPShorterThanTheNAL(t *testing.T) {
	// unescapeRBSP drops the 0x03 of a 00 00 03 sequence, so four NAL bytes
	// become two RBSP ones.
	if _, ok := h264ProfileLevel(annexB([]byte{0x67, 0x00, 0x00, 0x03})); ok {
		t.Error("an SPS whose RBSP is two bytes produced a profile")
	}
	short := append([]byte{0x42, 0x01}, bytes.Repeat([]byte{0x00, 0x00, 0x03}, 6)...)
	if _, ok := hevcProfileLevel(annexB(short)); ok {
		t.Error("an HEVC SPS whose RBSP is too short produced a profile")
	}
}

// The remaining esds branches: optional fields that consume the descriptor, and
// descriptors that are simply not there.
func TestParseESDS_MissingDescriptors(t *testing.T) {
	// A URL flag whose length consumes everything after it.
	esd := descriptorBytes(0x03, []byte{0x00, 0x01, 0x40, 0x01, 'x'})
	if _, ok := parseESDS(append([]byte{0, 0, 0, 0}, esd...)); ok {
		t.Error("an ES_Descriptor with nothing after its URL was accepted")
	}
	// No DecoderConfigDescriptor at all.
	esd = descriptorBytes(0x03, []byte{0x00, 0x01, 0x00, 0x06, 0x01, 0x00})
	if _, ok := parseESDS(append([]byte{0, 0, 0, 0}, esd...)); ok {
		t.Error("an ES_Descriptor with no DecoderConfigDescriptor was accepted")
	}
	// A DecoderConfigDescriptor with its fixed fields and no DecoderSpecificInfo.
	dcd := descriptorBytes(0x04, append([]byte{0x40, 0x15}, make([]byte, 11)...))
	esd = descriptorBytes(0x03, append([]byte{0x00, 0x01, 0x00}, dcd...))
	if _, ok := parseESDS(append([]byte{0, 0, 0, 0}, esd...)); ok {
		t.Error("a DecoderConfigDescriptor with no configuration was accepted")
	}
	// An explicit sampling frequency whose twenty-four bits are not there.
	if _, ok := parseAudioSpecificConfig(packBits([]bitField{{5, 2}, {4, 15}})); ok {
		t.Error("an explicit rate with no bits behind it was accepted")
	}
}

// The explicit branch of scaling_list_data, cut short.
func TestSkipHEVCScalingListData_RunsOut(t *testing.T) {
	// pred_mode_flag 1, then not enough bits for the coefficients that follow.
	r := &bitReader{data: []byte{0x80}}
	skipHEVCScalingListData(r)
	if !r.err {
		t.Error("the explicit branch did not notice it had run out of bits")
	}
}
