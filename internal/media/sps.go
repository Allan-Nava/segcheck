package media

// H.264 resolution recovery.
//
// An MPEG-TS segment states no resolution anywhere in the container: the only
// place the real frame size exists is the sequence parameter set inside the
// video elementary stream. That is why segcheck can say "the manifest declares
// 1920x1080 and the bitstream codes 1280x720" while a manifest-only checker
// cannot.

// bitReader reads big-endian bit fields, and the Exp-Golomb codes H.264 uses
// for most syntax elements. Every read is bounds-checked: a truncated or
// mis-detected SPS makes it return an error instead of a wrong resolution.
type bitReader struct {
	data []byte
	pos  int // bit position
	err  bool
}

func (r *bitReader) bit() uint32 {
	if r.pos >= len(r.data)*8 {
		r.err = true
		return 0
	}
	b := r.data[r.pos/8]
	shift := 7 - uint(r.pos%8)
	r.pos++
	return uint32(b>>shift) & 1
}

func (r *bitReader) bits(n int) uint32 {
	var v uint32
	for i := 0; i < n; i++ {
		v = v<<1 | r.bit()
	}
	return v
}

// ue reads an unsigned Exp-Golomb code.
func (r *bitReader) ue() uint32 {
	zeros := 0
	for r.bit() == 0 {
		if r.err || zeros > 32 {
			r.err = true
			return 0
		}
		zeros++
	}
	if zeros == 0 {
		return 0
	}
	return (1 << uint(zeros)) - 1 + r.bits(zeros)
}

// se reads a signed Exp-Golomb code.
func (r *bitReader) se() int32 {
	k := r.ue()
	if k%2 == 0 {
		return -int32(k / 2)
	}
	return int32((k + 1) / 2)
}

// h264Resolution finds the first SPS in an Annex-B elementary stream and
// returns the cropped display resolution.
func h264Resolution(es []byte) (width, height int, ok bool) {
	for _, nal := range annexBNALUs(es) {
		if len(nal) < 4 || nal[0]&0x1F != 7 { // nal_unit_type 7 = SPS
			continue
		}
		if w, h, ok := parseH264SPS(unescapeRBSP(nal[1:])); ok {
			return w, h, true
		}
	}
	return 0, 0, false
}

// annexBNALUs splits an Annex-B byte stream on its start codes. It stops after
// a bounded number of units: the SPS precedes the first slice, so scanning the
// whole segment would be work with no possible payoff.
func annexBNALUs(es []byte) [][]byte {
	nals, _ := annexBNALUsLimit(es, 64)
	return nals
}

// annexBNALUsLimit is annexBNALUs with the cap chosen by the caller, and it says
// whether the cap was what stopped it.
//
// That second return is not cosmetic. The keyframe check has to distinguish "this
// segment contains no random access point" from "the walk gave up before reaching
// one", and the two look identical from the slice alone. A 1080p picture split
// across dozens of slices pushes the following IDR past a 64-unit cap, which is
// how a stricter first draft of that check reported Apple's reference stream as
// having no keyframe at all.
func annexBNALUsLimit(es []byte, maxNALUs int) (nalus [][]byte, truncated bool) {
	var out [][]byte
	i := 0
	for i+3 < len(es) {
		if len(out) >= maxNALUs {
			return out, true
		}
		if es[i] != 0x00 || es[i+1] != 0x00 {
			i++
			continue
		}
		var start int
		switch {
		case es[i+2] == 0x01:
			start = i + 3
		case es[i+2] == 0x00 && i+3 < len(es) && es[i+3] == 0x01:
			start = i + 4
		default:
			i++
			continue
		}
		// Find the next start code to bound this unit.
		end := len(es)
		for j := start; j+2 < len(es); j++ {
			if es[j] == 0x00 && es[j+1] == 0x00 && (es[j+2] == 0x01 || (es[j+2] == 0x00 && j+3 < len(es) && es[j+3] == 0x01)) {
				end = j
				break
			}
		}
		if end > start {
			out = append(out, es[start:end])
		}
		i = end
	}
	return out, false
}

// unescapeRBSP removes the emulation prevention bytes: inside a NAL unit the
// encoder inserts 0x03 into any 00 00 0x sequence, and reading the bitstream
// without undoing that desynchronises every field after the first occurrence.
func unescapeRBSP(nal []byte) []byte {
	out := make([]byte, 0, len(nal))
	zeros := 0
	for _, b := range nal {
		if zeros >= 2 && b == 0x03 {
			zeros = 0
			continue // drop the emulation prevention byte
		}
		if b == 0x00 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, b)
	}
	return out
}

// parseH264SPS walks the sequence parameter set as far as the frame cropping
// fields — every element before them has to be read, in order, because they are
// variable length.
func parseH264SPS(rbsp []byte) (width, height int, ok bool) {
	if len(rbsp) < 3 {
		return 0, 0, false
	}
	r := &bitReader{data: rbsp}
	profileIDC := r.bits(8)
	r.bits(8) // constraint flags + reserved
	r.bits(8) // level_idc
	r.ue()    // seq_parameter_set_id

	chromaFormatIDC := uint32(1) // 4:2:0 when the profile does not state it
	separateColourPlane := false
	switch profileIDC {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		chromaFormatIDC = r.ue()
		if chromaFormatIDC == 3 {
			separateColourPlane = r.bit() == 1
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
	switch picOrderCntType := r.ue(); picOrderCntType {
	case 0:
		r.ue() // log2_max_pic_order_cnt_lsb_minus4
	case 1:
		r.bit() // delta_pic_order_always_zero_flag
		r.se()  // offset_for_non_ref_pic
		r.se()  // offset_for_top_to_bottom_field
		n := r.ue()
		if n > 256 {
			return 0, 0, false
		}
		for i := uint32(0); i < n; i++ {
			r.se() // offset_for_ref_frame[i]
		}
	}

	r.ue()  // max_num_ref_frames
	r.bit() // gaps_in_frame_num_value_allowed_flag
	widthInMBs := r.ue() + 1
	heightInMapUnits := r.ue() + 1
	frameMBsOnly := r.bit()
	if frameMBsOnly == 0 {
		r.bit() // mb_adaptive_frame_field_flag
	}
	r.bit() // direct_8x8_inference_flag

	var cropLeft, cropRight, cropTop, cropBottom uint32
	if r.bit() == 1 { // frame_cropping_flag
		cropLeft, cropRight, cropTop, cropBottom = r.ue(), r.ue(), r.ue(), r.ue()
	}
	if r.err {
		return 0, 0, false
	}

	// Crop offsets are counted in chroma samples, so the luma step depends on
	// the subsampling and, vertically, on whether the frame is interlaced.
	cropUnitX, cropUnitY := uint32(1), uint32(2-frameMBsOnly)
	if !separateColourPlane && chromaFormatIDC != 0 {
		subWidthC, subHeightC := uint32(2), uint32(2)
		switch chromaFormatIDC {
		case 2:
			subHeightC = 1
		case 3:
			subWidthC, subHeightC = 1, 1
		}
		cropUnitX = subWidthC
		cropUnitY = subHeightC * (2 - frameMBsOnly)
	}

	w := int(widthInMBs*16) - int((cropLeft+cropRight)*cropUnitX)
	h := int((2-frameMBsOnly)*heightInMapUnits*16) - int((cropTop+cropBottom)*cropUnitY)
	if !plausibleResolution(w, h) {
		return 0, 0, false
	}
	return w, h, true
}

// skipScalingList consumes a scaling list without storing it.
func skipScalingList(r *bitReader, size int) {
	lastScale := int32(8)
	nextScale := int32(8)
	for i := 0; i < size; i++ {
		if nextScale != 0 {
			delta := r.se()
			nextScale = (lastScale + delta + 256) % 256
		}
		if nextScale != 0 {
			lastScale = nextScale
		}
		if r.err {
			return
		}
	}
}
