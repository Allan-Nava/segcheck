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
	// PicOrderCntType selects how picture order is coded. Type 1 carries a
	// variable-length list of frame offsets, which is the second place — after
	// the scaling matrices — where the fields before the resolution change
	// length. A reader that mismeasures it reads the macroblock counts out of
	// the middle of an offset.
	PicOrderCntType uint32
	// OffsetForRefFrame is written only for PicOrderCntType 1, one signed
	// Exp-Golomb code per entry.
	OffsetForRefFrame []int32
	// VUI, when set, writes video usability information after the cropping
	// offsets. The colour description lives there, behind two optional blocks
	// whose lengths vary — an aspect ratio that may or may not carry an extended
	// SAR, and an overscan flag — which is the whole reason reaching it means
	// parsing rather than seeking.
	VUI *VUIParams
}

// VUIParams are the video usability fields segcheck reads: how the code values
// in the samples map to light.
type VUIParams struct {
	// AspectRatioIDC, when non-zero, writes the aspect ratio block. 255 is
	// Extended_SAR and adds two sixteen-bit fields, which is the length change a
	// reader has to follow rather than assume.
	AspectRatioIDC uint32
	SARWidth       uint32
	SARHeight      uint32
	Overscan       bool
	// VideoSignalType writes video_format, video_full_range_flag and, when
	// ColourDescription is set, the three colour bytes.
	VideoSignalType   bool
	FullRange         bool
	ColourDescription bool
	Primaries         uint32
	Transfer          uint32
	Matrix            uint32
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
			// Eight lists, or twelve in 4:4:4 — the count the standard ties to
			// chroma_format_idc, and the reason a reader that assumes eight ends
			// up four lists out of step on 4:4:4 content.
			lists := 8
			if p.ChromaFormatIDC == 3 {
				lists = 12
			}
			for i := 0; i < lists; i++ {
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

	w.ue(4) // log2_max_frame_num_minus4
	w.ue(p.PicOrderCntType)
	switch p.PicOrderCntType {
	case 0:
		w.ue(4) // log2_max_pic_order_cnt_lsb_minus4
	case 1:
		w.bit(0) // delta_pic_order_always_zero_flag
		w.se(0)  // offset_for_non_ref_pic
		w.se(0)  // offset_for_top_to_bottom_field
		w.ue(uint32(len(p.OffsetForRefFrame)))
		for _, off := range p.OffsetForRefFrame {
			w.se(off)
		}
	}
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
	if p.VUI == nil {
		w.bit(0) // vui_parameters_present_flag
		w.bit(1) // rbsp_stop_one_bit
		return w.bytes()
	}
	w.bit(1)
	writeVUI(w, *p.VUI)
	w.bit(1) // rbsp_stop_one_bit
	return w.bytes()
}

// writeVUI encodes the leading fields of video usability information, as far as
// the colour description. H.264 and HEVC spell these identically, which is why
// one writer serves both.
func writeVUI(w *bitWriter, v VUIParams) {
	if v.AspectRatioIDC != 0 {
		w.bit(1)
		w.u(8, v.AspectRatioIDC)
		if v.AspectRatioIDC == 255 { // Extended_SAR
			w.u(16, v.SARWidth)
			w.u(16, v.SARHeight)
		}
	} else {
		w.bit(0)
	}
	if v.Overscan {
		w.bit(1)
		w.bit(1) // overscan_appropriate_flag
	} else {
		w.bit(0)
	}
	if !v.VideoSignalType {
		w.bit(0)
		return
	}
	w.bit(1)
	w.u(3, 5) // video_format: unspecified
	w.bit(boolBit(v.FullRange))
	if !v.ColourDescription {
		w.bit(0)
		return
	}
	w.bit(1)
	w.u(8, v.Primaries)
	w.u(8, v.Transfer)
	w.u(8, v.Matrix)
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
