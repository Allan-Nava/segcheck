package media

// How the code values in a picture map to light — the colour description — is
// the claim behind HDR, and it is stated in three places depending on the
// container: the VUI of an H.264 or HEVC sequence parameter set, and the `colr`
// box of an fMP4 sample entry. That is the same split resolution already has:
// where the container states it, no bitstream reader is needed.
//
// It matters because a PQ rung whose samples are really BT.709 is tone-mapped
// twice by every device that believes the manifest and once by every device that
// believes the bitstream, so the two halves of the audience see different
// pictures of the same stream — and neither half sees an error.
//
// The trap is where the VUI sits. In H.264 it is behind two optional blocks
// whose lengths vary; in HEVC it is behind the short-term reference picture
// sets, whose sizes depend on each other. Reaching it means parsing all of that
// rather than seeking past it, and a reader that mismeasures does not fail — it
// reads a colour out of the middle of some other field and returns a plausible
// wrong answer. Every value is therefore checked against the assigned ranges
// before it is believed.

// Assigned transfer characteristics worth naming. The rest are read and
// reported as numbers, because the registry grows and a guessed name is worse
// than a number.
const (
	TransferBT709    = 1
	TransferUnspec   = 2
	TransferBT601    = 6
	TransferPQ       = 16 // SMPTE ST 2084
	TransferHLG      = 18 // ARIB STD-B67
	PrimariesBT709   = 1
	PrimariesBT2020  = 9
	MatrixBT709      = 1
	MatrixBT2020NCL  = 9
	maxColourCodeVal = 255
)

// ColourDescription is what a bitstream or a container says about the colour of
// the samples.
//
// Stated and RangeStated are separate because the two claims arrive separately:
// video_signal_type_present_flag brings the range, and
// colour_description_present_flag — nested inside it — brings the three code
// points. A stream may state the first and not the second.
type ColourDescription struct {
	Primaries int  `json:"primaries,omitempty"`
	Transfer  int  `json:"transfer,omitempty"`
	Matrix    int  `json:"matrix,omitempty"`
	FullRange bool `json:"full_range,omitempty"`
	// Stated is whether the three code points were stated. When false they are
	// zero, and zero is *reserved* rather than a default: reading it as BT.709
	// would be a claim segcheck invented.
	Stated bool `json:"stated,omitempty"`
	// RangeStated is whether FullRange was stated.
	RangeStated bool `json:"range_stated,omitempty"`
}

// TransferName is the common name of a transfer characteristic, or the empty
// string for one this tool does not name.
func TransferName(code int) string {
	switch code {
	case TransferBT709:
		return "BT.709"
	case TransferBT601:
		return "BT.601"
	case TransferPQ:
		return "PQ"
	case TransferHLG:
		return "HLG"
	case 8:
		return "linear"
	case 14, 15:
		return "BT.2020"
	}
	return ""
}

// HDR reports whether the transfer characteristic is one of the two high dynamic
// range functions. Everything else — including "unspecified" — is not a claim of
// HDR, and must not be read as one.
func (c ColourDescription) HDR() bool {
	return c.Stated && (c.Transfer == TransferPQ || c.Transfer == TransferHLG)
}

// Label renders the description for a finding: names where there are names,
// numbers where there are not.
func (c ColourDescription) Label() string {
	if !c.Stated {
		return "unstated"
	}
	if n := TransferName(c.Transfer); n != "" {
		return n
	}
	return "transfer " + itoaColour(c.Transfer)
}

func itoaColour(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// h264Colour finds the first SPS in an Annex-B elementary stream and returns its
// colour description.
func h264Colour(es []byte) (ColourDescription, bool) {
	for _, nal := range annexBNALUs(es) {
		if len(nal) < 4 || nal[0]&0x1F != 7 {
			continue
		}
		if c, ok := parseH264Colour(unescapeRBSP(nal[1:])); ok {
			return c, true
		}
	}
	return ColourDescription{}, false
}

// hevcColour is the same for HEVC.
func hevcColour(es []byte) (ColourDescription, bool) {
	for _, nal := range annexBNALUs(es) {
		if len(nal) < 4 || (nal[0]>>1)&0x3F != nalTypeHEVCSPS {
			continue
		}
		if c, ok := parseHEVCColour(unescapeRBSP(nal[2:])); ok {
			return c, true
		}
	}
	return ColourDescription{}, false
}

// parseH264Colour walks a sequence parameter set to the VUI and reads the colour
// description out of it.
//
// It repeats the walk parseH264SPS does rather than sharing it, because the two
// answer different questions and a single function returning both would have to
// keep walking past the cropping offsets even when only the resolution is
// wanted — which is most of the time, on every segment of every rung.
func parseH264Colour(rbsp []byte) (ColourDescription, bool) {
	if len(rbsp) < 3 {
		return ColourDescription{}, false
	}
	r := &bitReader{data: rbsp}
	profileIDC := r.bits(8)
	r.bits(8) // constraint flags + reserved
	r.bits(8) // level_idc
	r.ue()    // seq_parameter_set_id

	chromaFormatIDC := uint32(1)
	switch profileIDC {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		chromaFormatIDC = r.ue()
		if chromaFormatIDC == 3 {
			r.bit() // separate_colour_plane_flag
		}
		r.ue()            // bit_depth_luma_minus8
		r.ue()            // bit_depth_chroma_minus8
		r.bit()           // qpprime_y_zero_transform_bypass_flag
		if r.bit() == 1 { // seq_scaling_matrix_present_flag
			lists := 8
			if chromaFormatIDC == 3 {
				lists = 12
			}
			for i := 0; i < lists; i++ {
				if r.bit() == 1 {
					size := 16
					if i >= 6 {
						size = 64
					}
					skipScalingList(r, size)
				}
			}
		}
	}

	r.ue() // log2_max_frame_num_minus4
	switch r.ue() {
	case 0:
		r.ue() // log2_max_pic_order_cnt_lsb_minus4
	case 1:
		r.bit() // delta_pic_order_always_zero_flag
		r.se()  // offset_for_non_ref_pic
		r.se()  // offset_for_top_to_bottom_field
		n := r.ue()
		if n > 256 {
			return ColourDescription{}, false
		}
		for i := uint32(0); i < n; i++ {
			r.se()
		}
	}

	r.ue()  // max_num_ref_frames
	r.bit() // gaps_in_frame_num_value_allowed_flag
	r.ue()  // pic_width_in_mbs_minus1
	r.ue()  // pic_height_in_map_units_minus1
	if r.bit() == 0 {
		r.bit() // mb_adaptive_frame_field_flag
	}
	r.bit()           // direct_8x8_inference_flag
	if r.bit() == 1 { // frame_cropping_flag
		r.ue()
		r.ue()
		r.ue()
		r.ue()
	}
	if r.err {
		return ColourDescription{}, false
	}
	if r.bit() != 1 { // vui_parameters_present_flag
		return ColourDescription{}, r.err == false
	}
	return parseVUIColour(r)
}

// parseVUIColour reads the leading VUI fields, which H.264 and HEVC spell
// identically as far as the colour description.
func parseVUIColour(r *bitReader) (ColourDescription, bool) {
	var c ColourDescription
	if r.bit() == 1 { // aspect_ratio_info_present_flag
		if r.bits(8) == 255 { // Extended_SAR moves everything after it by 32 bits
			r.bits(16)
			r.bits(16)
		}
	}
	if r.bit() == 1 { // overscan_info_present_flag
		r.bit() // overscan_appropriate_flag
	}
	if r.bit() != 1 { // video_signal_type_present_flag
		return c, !r.err
	}
	r.bits(3) // video_format
	c.FullRange = r.bit() == 1
	c.RangeStated = true
	if r.bit() != 1 { // colour_description_present_flag
		if r.err {
			return ColourDescription{}, false
		}
		return c, true
	}
	primaries := int(r.bits(8))
	transfer := int(r.bits(8))
	matrix := int(r.bits(8))
	if r.err {
		// The walk ran off the end, so whatever it read is not a colour.
		return ColourDescription{}, false
	}
	if !plausibleColour(primaries, transfer, matrix) {
		return ColourDescription{}, false
	}
	c.Primaries, c.Transfer, c.Matrix, c.Stated = primaries, transfer, matrix, true
	return c, true
}

// plausibleColour rejects code points outside the assigned ranges.
//
// It is the guard against the failure mode that matters here: a walk that
// mismeasured a variable-length field does not run off the end of the buffer, it
// reads three bytes out of the middle of something else and returns a colour
// that looks like a colour. The assigned values are sparse enough that garbage
// usually lands outside them, and a rejected read reports "unstated" — which is
// the honest answer for a reader that lost its place.
func plausibleColour(primaries, transfer, matrix int) bool {
	if primaries < 0 || primaries > maxColourCodeVal ||
		transfer < 0 || transfer > maxColourCodeVal ||
		matrix < 0 || matrix > maxColourCodeVal {
		return false
	}
	// 0 and 3 are reserved in all three registries, and 2 is "unspecified" —
	// legal, and carrying no claim.
	if primaries == 0 || primaries == 3 || transfer == 0 || transfer == 3 || matrix == 3 {
		return false
	}
	// Assigned ranges as of H.273: primaries 1..22, transfer 1..18, matrix 0..14.
	return primaries <= 22 && transfer <= 18 && matrix <= 14
}

// parseHEVCColour walks an HEVC sequence parameter set to the VUI.
//
// Everything between the conformance window and the VUI has to be measured
// exactly, and most of it is variable length: the sub-layer ordering info, the
// coding-block sizes, the scaling lists, and above all the short-term reference
// picture sets, whose sizes depend on one another because a set may be coded as
// a prediction from the one before it.
func parseHEVCColour(rbsp []byte) (ColourDescription, bool) {
	if len(rbsp) < 4 {
		return ColourDescription{}, false
	}
	r := &bitReader{data: rbsp}

	r.bits(4) // sps_video_parameter_set_id
	maxSubLayersMinus1 := r.bits(3)
	r.bit() // sps_temporal_id_nesting_flag
	if maxSubLayersMinus1 > maxHEVCSubLayers {
		return ColourDescription{}, false
	}
	skipHEVCProfileTierLevel(r, maxSubLayersMinus1)

	r.ue() // sps_seq_parameter_set_id
	chromaFormatIDC := r.ue()
	if chromaFormatIDC > 3 {
		return ColourDescription{}, false
	}
	if chromaFormatIDC == 3 {
		r.bit() // separate_colour_plane_flag
	}
	r.ue()            // pic_width_in_luma_samples
	r.ue()            // pic_height_in_luma_samples
	if r.bit() == 1 { // conformance_window_flag
		r.ue()
		r.ue()
		r.ue()
		r.ue()
	}
	r.ue() // bit_depth_luma_minus8
	r.ue() // bit_depth_chroma_minus8
	log2MaxPocLsb := r.ue() + 4
	if log2MaxPocLsb > 16 {
		return ColourDescription{}, false
	}

	first := maxSubLayersMinus1
	if r.bit() == 1 { // sps_sub_layer_ordering_info_present_flag
		first = 0
	}
	for i := first; i <= maxSubLayersMinus1; i++ {
		r.ue() // sps_max_dec_pic_buffering_minus1
		r.ue() // sps_max_num_reorder_pics
		r.ue() // sps_max_latency_increase_plus1
	}

	r.ue()            // log2_min_luma_coding_block_size_minus3
	r.ue()            // log2_diff_max_min_luma_coding_block_size
	r.ue()            // log2_min_luma_transform_block_size_minus2
	r.ue()            // log2_diff_max_min_luma_transform_block_size
	r.ue()            // max_transform_hierarchy_depth_inter
	r.ue()            // max_transform_hierarchy_depth_intra
	if r.bit() == 1 { // scaling_list_enabled_flag
		if r.bit() == 1 { // sps_scaling_list_data_present_flag
			skipHEVCScalingListData(r)
		}
	}
	r.bit()           // amp_enabled_flag
	r.bit()           // sample_adaptive_offset_enabled_flag
	if r.bit() == 1 { // pcm_enabled_flag
		r.bits(4) // pcm_sample_bit_depth_luma_minus1
		r.bits(4) // pcm_sample_bit_depth_chroma_minus1
		r.ue()    // log2_min_pcm_luma_coding_block_size_minus3
		r.ue()    // log2_diff_max_min_pcm_luma_coding_block_size
		r.bit()   // pcm_loop_filter_disabled_flag
	}

	numSets := r.ue()
	if r.err || numSets > 64 { // the specification's own maximum
		return ColourDescription{}, false
	}
	numDeltaPocs := make([]uint32, numSets+1)
	for i := uint32(0); i < numSets; i++ {
		if !skipHEVCShortTermRefPicSet(r, i, numDeltaPocs) {
			return ColourDescription{}, false
		}
	}

	if r.bit() == 1 { // long_term_ref_pics_present_flag
		n := r.ue()
		if r.err || n > 32 {
			return ColourDescription{}, false
		}
		for i := uint32(0); i < n; i++ {
			r.bits(int(log2MaxPocLsb)) // lt_ref_pic_poc_lsb_sps[i]
			r.bit()                    // used_by_curr_pic_lt_sps_flag[i]
		}
	}
	r.bit() // sps_temporal_mvp_enabled_flag
	r.bit() // strong_intra_smoothing_enabled_flag
	if r.err {
		return ColourDescription{}, false
	}
	if r.bit() != 1 { // vui_parameters_present_flag
		return ColourDescription{}, !r.err
	}
	return parseVUIColour(r)
}

// skipHEVCShortTermRefPicSet consumes one st_ref_pic_set and records how many
// delta POCs it carries, which is what the next set's prediction loop is sized
// by. That dependency is why these cannot be skipped: a reader has to have
// counted set N-1 to know how long set N is.
func skipHEVCShortTermRefPicSet(r *bitReader, idx uint32, numDeltaPocs []uint32) bool {
	if idx > 0 && r.bit() == 1 { // inter_ref_pic_set_prediction_flag
		r.bit() // delta_rps_sign
		r.ue()  // abs_delta_rps_minus1
		// In an SPS the reference is always the previous set: delta_idx_minus1
		// appears only in a slice header, where idx equals the set count.
		ref := numDeltaPocs[idx-1]
		if ref > 32 {
			return false
		}
		carried := uint32(0)
		for j := uint32(0); j <= ref; j++ {
			used := r.bit() == 1
			if !used {
				if r.bit() == 1 { // use_delta_flag[j]
					carried++
				}
				continue
			}
			carried++
		}
		numDeltaPocs[idx] = carried
		return !r.err
	}
	negative := r.ue()
	positive := r.ue()
	if r.err || negative > 16 || positive > 16 {
		return false
	}
	for i := uint32(0); i < negative+positive; i++ {
		r.ue()  // delta_poc_sX_minus1
		r.bit() // used_by_curr_pic_sX_flag
	}
	numDeltaPocs[idx] = negative + positive
	return !r.err
}

// skipHEVCScalingListData consumes scaling_list_data, whose entries are either a
// reference to an earlier list or a run of signed codes.
func skipHEVCScalingListData(r *bitReader) {
	for sizeID := 0; sizeID < 4; sizeID++ {
		step := 1
		if sizeID == 3 {
			step = 3
		}
		for matrixID := 0; matrixID < 6; matrixID += step {
			if r.bit() == 0 { // scaling_list_pred_mode_flag
				r.ue() // scaling_list_pred_matrix_id_delta
				continue
			}
			coefNum := 64
			if sizeID == 0 {
				coefNum = 16
			}
			if sizeID > 1 {
				r.se() // scaling_list_dc_coef_minus8
			}
			for i := 0; i < coefNum; i++ {
				r.se() // scaling_list_delta_coef
			}
			if r.err {
				return
			}
		}
	}
}
