package mediatest

// An H.264 sequence parameter set writer — the inverse of the reader under
// test. Encoding a parameter set with known field values and reading it back is
// what validates the two things that break in practice: the order and variable
// length of the fields before the cropping offsets, and the crop arithmetic.

// SPSParams are the sequence parameter set fields that decide the resolution.
type SPSParams struct {
	ProfileIDC       uint32
	ChromaFormatIDC  uint32
	WidthInMBsMinus1 uint32
	HeightInMapUnits uint32 // pic_height_in_map_units_minus1
	FrameMBsOnly     uint32
	FrameCropping    bool
	CropLeft         uint32
	CropRight        uint32
	CropTop          uint32
	CropBottom       uint32
	ScalingMatrix    bool
}

// SPSFor builds a High-profile progressive 4:2:0 SPS that codes exactly
// width x height, deriving the macroblock counts and the cropping offsets. It is
// the shape every real ladder rung has: 1080 lines are coded as 1088 and 8 are
// cropped, because 1080 is not a multiple of 16.
func SPSFor(width, height int) []byte {
	widthInMBs := (width + 15) / 16
	heightInMapUnits := (height + 15) / 16
	// cropUnitX and cropUnitY are both 2 for progressive 4:2:0.
	return SPS(SPSParams{
		ProfileIDC:       100,
		ChromaFormatIDC:  1,
		WidthInMBsMinus1: uint32(widthInMBs - 1),
		HeightInMapUnits: uint32(heightInMapUnits - 1),
		FrameMBsOnly:     1,
		FrameCropping:    true,
		CropRight:        uint32((widthInMBs*16 - width) / 2),
		CropBottom:       uint32((heightInMapUnits*16 - height) / 2),
	})
}

// SPS encodes a sequence parameter set RBSP, without the NAL header byte.
func SPS(p SPSParams) []byte {
	w := &bitWriter{}
	w.u(8, p.ProfileIDC)
	w.u(8, 0)  // constraint flags + reserved
	w.u(8, 40) // level_idc
	w.ue(0)    // seq_parameter_set_id

	switch p.ProfileIDC {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		w.ue(p.ChromaFormatIDC)
		if p.ChromaFormatIDC == 3 {
			w.bit(0) // separate_colour_plane_flag
		}
		w.ue(0)  // bit_depth_luma_minus8
		w.ue(0)  // bit_depth_chroma_minus8
		w.bit(0) // qpprime_y_zero_transform_bypass_flag
		if p.ScalingMatrix {
			w.bit(1)
			for i := 0; i < 8; i++ {
				w.bit(1) // this list is present
				size := 16
				if i >= 6 {
					size = 64
				}
				for j := 0; j < size; j++ {
					w.se(0) // delta_scale 0 keeps nextScale at 8
				}
			}
		} else {
			w.bit(0)
		}
	}

	w.ue(4)  // log2_max_frame_num_minus4
	w.ue(0)  // pic_order_cnt_type
	w.ue(4)  // log2_max_pic_order_cnt_lsb_minus4
	w.ue(2)  // max_num_ref_frames
	w.bit(0) // gaps_in_frame_num_value_allowed_flag
	w.ue(p.WidthInMBsMinus1)
	w.ue(p.HeightInMapUnits)
	w.bit(p.FrameMBsOnly)
	if p.FrameMBsOnly == 0 {
		w.bit(0) // mb_adaptive_frame_field_flag
	}
	w.bit(1) // direct_8x8_inference_flag
	if p.FrameCropping {
		w.bit(1)
		w.ue(p.CropLeft)
		w.ue(p.CropRight)
		w.ue(p.CropTop)
		w.ue(p.CropBottom)
	} else {
		w.bit(0)
	}
	w.bit(0) // vui_parameters_present_flag
	w.bit(1) // rbsp_stop_one_bit
	return w.bytes()
}

// bitWriter accumulates single bits and the Exp-Golomb codes H.264 uses.
type bitWriter struct {
	bits []byte
}

func (w *bitWriter) bit(v uint32) { w.bits = append(w.bits, byte(v&1)) }

func (w *bitWriter) u(n int, v uint32) {
	for i := n - 1; i >= 0; i-- {
		w.bit(v >> uint(i))
	}
}

// ue writes an unsigned Exp-Golomb code.
func (w *bitWriter) ue(v uint32) {
	v++
	n := 0
	for t := v; t > 1; t >>= 1 {
		n++
	}
	for i := 0; i < n; i++ {
		w.bit(0)
	}
	for i := n; i >= 0; i-- {
		w.bit(v >> uint(i))
	}
}

func (w *bitWriter) se(v int32) {
	if v <= 0 {
		w.ue(uint32(-2 * v))
		return
	}
	w.ue(uint32(2*v - 1))
}

func (w *bitWriter) bytes() []byte {
	out := make([]byte, (len(w.bits)+7)/8)
	for i, b := range w.bits {
		if b != 0 {
			out[i/8] |= 1 << uint(7-i%8)
		}
	}
	return out
}
