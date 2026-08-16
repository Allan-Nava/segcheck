package media

// HEVC/H.265 resolution recovery.
//
// Same problem as H.264 and a harder bitstream: an MPEG-TS segment states no
// resolution anywhere in the container, so the only place the real frame size
// exists is the sequence parameter set. Until this existed, an HEVC rung
// reported its codec and no resolution, and the `resolution` check skipped it
// in silence — which reads exactly like a rung that passed.
//
// Two things differ from H.264 and both are places to get it wrong:
//
//   - The NAL header is two bytes, and the type is bits 1..6 of the first one
//     rather than the low five bits.
//   - profile_tier_level sits between the header and the resolution, and its
//     length depends on how many temporal sub-layers the stream declares. A
//     reader that mismeasures it does not fail; it reads the width from the
//     middle of some other field and returns a plausible wrong number.

const (
	nalTypeHEVCSPS = 33
	// maxHEVCSubLayers bounds the sub-layer loop. The spec allows seven
	// (sps_max_sub_layers_minus1 is three bits, so at most 7), and anything
	// larger means we are not reading a parameter set.
	maxHEVCSubLayers = 7
)

// hevcResolution finds the first SPS in an Annex-B HEVC elementary stream and
// returns the resolution after the conformance window is applied.
func hevcResolution(es []byte) (width, height int, ok bool) {
	for _, nal := range annexBNALUs(es) {
		// Two header bytes plus something to parse.
		if len(nal) < 4 {
			continue
		}
		if (nal[0]>>1)&0x3F != nalTypeHEVCSPS {
			continue
		}
		if w, h, ok := parseHEVCSPS(unescapeRBSP(nal[2:])); ok {
			return w, h, true
		}
	}
	return 0, 0, false
}

// parseHEVCSPS walks the sequence parameter set as far as the conformance
// window. Every field before it is read in order because they are variable
// length, and the ones that are skipped still have to be measured exactly.
func parseHEVCSPS(rbsp []byte) (width, height int, ok bool) {
	if len(rbsp) < 4 {
		return 0, 0, false
	}
	r := &bitReader{data: rbsp}

	r.bits(4) // sps_video_parameter_set_id
	maxSubLayersMinus1 := r.bits(3)
	r.bit() // sps_temporal_id_nesting_flag
	if maxSubLayersMinus1 > maxHEVCSubLayers {
		return 0, 0, false
	}
	skipHEVCProfileTierLevel(r, maxSubLayersMinus1)

	r.ue() // sps_seq_parameter_set_id
	chromaFormatIDC := r.ue()
	if chromaFormatIDC > 3 {
		return 0, 0, false
	}
	separateColourPlane := false
	if chromaFormatIDC == 3 {
		separateColourPlane = r.bit() == 1
	}

	picWidth := r.ue()
	picHeight := r.ue()

	var winLeft, winRight, winTop, winBottom uint32
	if r.bit() == 1 { // conformance_window_flag
		winLeft, winRight, winTop, winBottom = r.ue(), r.ue(), r.ue(), r.ue()
	}
	if r.err {
		return 0, 0, false
	}

	// The offsets are counted in chroma samples, so the luma step is the
	// subsampling factor. separate_colour_plane_flag makes ChromaArrayType 0,
	// which is not subsampled whatever chroma_format_idc says.
	subWidthC, subHeightC := uint32(1), uint32(1)
	if !separateColourPlane {
		switch chromaFormatIDC {
		case 1: // 4:2:0
			subWidthC, subHeightC = 2, 2
		case 2: // 4:2:2 — horizontal only
			subWidthC, subHeightC = 2, 1
		}
	}

	w := int(picWidth) - int((winLeft+winRight)*subWidthC)
	h := int(picHeight) - int((winTop+winBottom)*subHeightC)
	if !plausibleResolution(w, h) {
		return 0, 0, false
	}
	return w, h, true
}

// skipHEVCProfileTierLevel consumes profile_tier_level with profilePresentFlag
// set, which is how it always appears inside an SPS.
//
// The fixed part is 96 bits. What follows is the part that bites: two presence
// flags per sub-layer, then — only when there is more than one sub-layer — two
// reserved bits for every slot up to eight, and then a body per sub-layer whose
// size depends on which flags were set.
func skipHEVCProfileTierLevel(r *bitReader, maxSubLayersMinus1 uint32) {
	r.bits(8)  // general_profile_space, general_tier_flag, general_profile_idc
	r.bits(32) // general_profile_compatibility_flag[32]
	// general_progressive_source_flag, general_interlaced_source_flag,
	// general_non_packed_constraint_flag, general_frame_only_constraint_flag,
	// 43 reserved bits, and general_inbld_flag: 48 bits in all.
	r.bits(24)
	r.bits(24)
	r.bits(8) // general_level_idc

	var profilePresent, levelPresent [maxHEVCSubLayers]bool
	for i := uint32(0); i < maxSubLayersMinus1; i++ {
		profilePresent[i] = r.bit() == 1
		levelPresent[i] = r.bit() == 1
	}
	if maxSubLayersMinus1 > 0 {
		for i := maxSubLayersMinus1; i < 8; i++ {
			r.bits(2) // reserved_zero_2bits[i]
		}
	}
	for i := uint32(0); i < maxSubLayersMinus1; i++ {
		if profilePresent[i] {
			r.bits(32)
			r.bits(32)
			r.bits(24) // 88 bits: the sub-layer's own profile block
		}
		if levelPresent[i] {
			r.bits(8) // sub_layer_level_idc[i]
		}
		if r.err {
			return
		}
	}
}
