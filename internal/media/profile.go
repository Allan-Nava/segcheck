package media

// A CODECS string is not one value. `avc1.640028` names a profile, a set of
// constraint flags and a level; `hvc1.2.4.L153.B0` adds a tier;
// `av01.0.13M.08` names a profile, a level and a tier of its own. Only the first
// component — the four-character code — has ever been read, and everything after
// it is where the interesting failures live.
//
// They fail in opposite directions, which is why both have to be reported. A
// level declared *below* the media's is a decoder that rejects the stream up
// front: the device reads the manifest, decides it cannot play this, and never
// asks for a segment. A profile or level declared *above* the media's silently
// excludes devices that could have played it perfectly — nobody sees an error,
// the top rung simply has fewer viewers than it should.
//
// Where the media states it differs by container, and it is the same split
// resolution has: in fMP4 the configuration record states it outright — avcC,
// hvcC, av1C, vpcC — and in MPEG-TS it has to come out of the bitstream.

// CodecProfile is what the media says about which decoder can play it.
type CodecProfile struct {
	// Profile is profile_idc for H.264, general_profile_idc for HEVC,
	// seq_profile for AV1, profile for VP9.
	Profile int `json:"profile"`
	// Level is level_idc, general_level_idc, seq_level_idx or level. The scales
	// are not comparable between codecs, which is why nothing here converts them
	// to a decimal: 40 is H.264 level 4.0 and 153 is HEVC level 5.1, and a
	// finding that printed "level 4" for both would be inventing a comparison.
	Level int `json:"level"`
	// Tier is general_tier_flag for HEVC and seq_tier for AV1: 0 main, 1 high.
	Tier int `json:"tier,omitempty"`
	// Constraints is the constraint-flags byte an H.264 codec string spells as
	// its middle pair of hex digits.
	Constraints int `json:"constraints,omitempty"`
	// Stated is false when nothing said, and the other fields must be ignored: an
	// unstated level is not level zero, and comparing against it would report
	// every stream as wrong.
	Stated bool `json:"stated,omitempty"`
}

// h264ProfileLevel reads the first three bytes of an SPS, which are exactly what
// an `avc1.PPCCLL` codec string spells out.
func h264ProfileLevel(es []byte) (CodecProfile, bool) {
	for _, nal := range annexBNALUs(es) {
		if len(nal) < 4 || nal[0]&0x1F != 7 {
			continue
		}
		rbsp := unescapeRBSP(nal[1:])
		if len(rbsp) < 3 {
			continue
		}
		return CodecProfile{
			Profile:     int(rbsp[0]),
			Constraints: int(rbsp[1]),
			Level:       int(rbsp[2]),
			Stated:      true,
		}, true
	}
	return CodecProfile{}, false
}

// hevcProfileLevel reads the profile_tier_level the resolution reader walks past.
func hevcProfileLevel(es []byte) (CodecProfile, bool) {
	for _, nal := range annexBNALUs(es) {
		if len(nal) < 4 || (nal[0]>>1)&0x3F != nalTypeHEVCSPS {
			continue
		}
		rbsp := unescapeRBSP(nal[2:])
		if len(rbsp) < 14 {
			continue
		}
		r := &bitReader{data: rbsp}
		r.bits(4) // sps_video_parameter_set_id
		maxSubLayersMinus1 := r.bits(3)
		r.bit() // sps_temporal_id_nesting_flag
		if maxSubLayersMinus1 > maxHEVCSubLayers {
			continue
		}
		r.bits(2) // general_profile_space
		tier := int(r.bit())
		profile := int(r.bits(5))
		r.bits(32) // general_profile_compatibility_flag[32]
		r.bits(24) // the constraint and reserved flags: 48 bits in all
		r.bits(24)
		level := int(r.bits(8))
		if r.err {
			continue
		}
		return CodecProfile{Profile: profile, Level: level, Tier: tier, Stated: true}, true
	}
	return CodecProfile{}, false
}

// profileFromCodecConfig reads the profile and level out of whichever
// configuration record a visual sample entry carries.
//
// Each puts them in a different place, and each of those places is a fixed
// offset — which is the useful part: in fMP4 no bitstream reader is needed for
// this at all, exactly as none is needed for the resolution.
func profileFromCodecConfig(children []byte) (CodecProfile, bool) {
	if avcC, ok := findBox(children, "avcC"); ok && len(avcC) >= 4 {
		// configurationVersion, AVCProfileIndication, profile_compatibility,
		// AVCLevelIndication.
		return CodecProfile{
			Profile:     int(avcC[1]),
			Constraints: int(avcC[2]),
			Level:       int(avcC[3]),
			Stated:      true,
		}, true
	}
	if hvcC, ok := findBox(children, "hvcC"); ok && len(hvcC) >= 13 {
		// Byte 1 packs general_profile_space, general_tier_flag and
		// general_profile_idc; byte 12 is general_level_idc, twelve bytes of
		// compatibility and constraint flags later.
		return CodecProfile{
			Profile: int(hvcC[1] & 0x1F),
			Tier:    int(hvcC[1] >> 5 & 0x01),
			Level:   int(hvcC[12]),
			Stated:  true,
		}, true
	}
	if av1C, ok := findBox(children, "av1C"); ok && len(av1C) >= 3 {
		// Byte 1: seq_profile in the top three bits, seq_level_idx_0 in the low
		// five. Byte 2: seq_tier_0 in the top bit.
		return CodecProfile{
			Profile: int(av1C[1] >> 5 & 0x07),
			Level:   int(av1C[1] & 0x1F),
			Tier:    int(av1C[2] >> 7 & 0x01),
			Stated:  true,
		}, true
	}
	if vpcC, ok := findBox(children, "vpcC"); ok && len(vpcC) >= 6 {
		// version and flags, then profile and level as whole bytes.
		return CodecProfile{
			Profile: int(vpcC[4]),
			Level:   int(vpcC[5]),
			Stated:  true,
		}, true
	}
	return CodecProfile{}, false
}
