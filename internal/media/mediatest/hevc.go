package mediatest

// An HEVC sequence parameter set writer — the inverse of the reader under test,
// exactly as SPS is for H.264.
//
// HEVC is the harder round trip of the two. Everything that decides the
// resolution sits behind profile_tier_level, whose length depends on how many
// temporal sub-layers the stream declares; a reader that gets that tail wrong
// still returns a number, just not the right one. Writing parameter sets with
// several sub-layer counts and reading them back is what catches it.

// HEVCSPSParams are the sequence parameter set fields that decide the
// resolution. The zero value is a single-sub-layer 4:2:0 set with no
// conformance window.
type HEVCSPSParams struct {
	MaxSubLayersMinus1  uint32
	ChromaFormatIDC     uint32
	WidthInLumaSamples  uint32
	HeightInLumaSamples uint32
	ConformanceWindow   bool
	ConfWinLeft         uint32
	ConfWinRight        uint32
	ConfWinTop          uint32
	ConfWinBottom       uint32
}

// HEVCSPSFor builds a 4:2:0 parameter set that displays exactly width x height.
//
// HEVC codes in multiples of the minimum coding block size, 8 luma samples, so
// most ladder rungs need no conformance window at all — unlike H.264, where
// 1080 lines are always coded as 1088. When a dimension is not a multiple of 8
// it is rounded up and the difference is cropped, which is the case that
// exercises the offset arithmetic.
func HEVCSPSFor(width, height int) []byte {
	codedW := (width + 7) / 8 * 8
	codedH := (height + 7) / 8 * 8
	p := HEVCSPSParams{
		ChromaFormatIDC:     1,
		WidthInLumaSamples:  uint32(codedW),
		HeightInLumaSamples: uint32(codedH),
	}
	// 4:2:0 offsets are counted in chroma samples: one unit is two luma lines.
	if codedW != width || codedH != height {
		p.ConformanceWindow = true
		p.ConfWinRight = uint32((codedW - width) / 2)
		p.ConfWinBottom = uint32((codedH - height) / 2)
	}
	return HEVCSPS(p)
}

// HEVCSPS encodes a sequence parameter set RBSP, without the two-byte NAL
// header.
func HEVCSPS(p HEVCSPSParams) []byte {
	w := &bitWriter{}
	w.u(4, 0)                    // sps_video_parameter_set_id
	w.u(3, p.MaxSubLayersMinus1) // sps_max_sub_layers_minus1
	w.bit(1)                     // sps_temporal_id_nesting_flag
	hevcProfileTierLevel(w, p.MaxSubLayersMinus1)
	w.ue(0) // sps_seq_parameter_set_id
	w.ue(p.ChromaFormatIDC)
	if p.ChromaFormatIDC == 3 {
		w.bit(0) // separate_colour_plane_flag
	}
	w.ue(p.WidthInLumaSamples)
	w.ue(p.HeightInLumaSamples)
	if p.ConformanceWindow {
		w.bit(1)
		w.ue(p.ConfWinLeft)
		w.ue(p.ConfWinRight)
		w.ue(p.ConfWinTop)
		w.ue(p.ConfWinBottom)
	} else {
		w.bit(0)
	}
	// The reader stops here, but a real set continues; writing a little more
	// keeps the fixture honest about what follows the fields under test.
	w.ue(0)  // bit_depth_luma_minus8
	w.ue(0)  // bit_depth_chroma_minus8
	w.ue(4)  // log2_max_pic_order_cnt_lsb_minus4
	w.bit(1) // rbsp_stop_one_bit
	return w.bytes()
}

// hevcProfileTierLevel writes profile_tier_level with profilePresentFlag set.
//
// The fixed part is 12 bytes. What follows depends on the sub-layer count, and
// it is the part readers get wrong: two presence flags per sub-layer, then —
// only when there is more than one sub-layer — two reserved bits for every
// slot up to eight, and then the sub-layer bodies themselves.
func hevcProfileTierLevel(w *bitWriter, maxSubLayersMinus1 uint32) {
	w.u(2, 0) // general_profile_space
	w.u(1, 0) // general_tier_flag
	w.u(5, 1) // general_profile_idc: Main
	for i := 0; i < 32; i++ {
		w.bit(0) // general_profile_compatibility_flag[i]
	}
	w.bit(1) // general_progressive_source_flag
	w.bit(0) // general_interlaced_source_flag
	w.bit(0) // general_non_packed_constraint_flag
	w.bit(1) // general_frame_only_constraint_flag
	for i := 0; i < 43; i++ {
		w.bit(0) // reserved / constraint flags
	}
	w.bit(0)   // general_inbld_flag or reserved
	w.u(8, 93) // general_level_idc: level 3.1

	profilePresent := make([]bool, maxSubLayersMinus1)
	levelPresent := make([]bool, maxSubLayersMinus1)
	for i := uint32(0); i < maxSubLayersMinus1; i++ {
		// Alternate so the fixture exercises both branches of the reader.
		profilePresent[i] = i%2 == 0
		levelPresent[i] = true
		w.bit(boolBit(profilePresent[i]))
		w.bit(boolBit(levelPresent[i]))
	}
	if maxSubLayersMinus1 > 0 {
		for i := maxSubLayersMinus1; i < 8; i++ {
			w.u(2, 0) // reserved_zero_2bits[i]
		}
	}
	for i := uint32(0); i < maxSubLayersMinus1; i++ {
		if profilePresent[i] {
			for j := 0; j < 88; j++ {
				w.bit(0) // the sub-layer's own profile block
			}
		}
		if levelPresent[i] {
			w.u(8, 93) // sub_layer_level_idc[i]
		}
	}
}

func boolBit(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

// HEVCAnnexB wraps a parameter set for width x height in an Annex-B elementary
// stream, preceded by a VPS and followed by a PPS — the reader has to find the
// SPS among them rather than assume it comes first.
func HEVCAnnexB(width, height int) []byte {
	var out []byte
	out = append(out, startCode...)
	out = append(out, hevcNAL(32, []byte{0x0c, 0x01, 0xff, 0xff})...) // VPS
	out = append(out, startCode...)
	out = append(out, hevcNAL(33, HEVCSPSFor(width, height))...) // SPS
	out = append(out, startCode...)
	out = append(out, hevcNAL(34, []byte{0xc0, 0xf3, 0xc0, 0x02})...) // PPS
	return out
}

// hevcNAL prefixes an RBSP with the two-byte HEVC NAL header: the type sits in
// bits 1..6 of the first byte, not in the low five bits as it does in H.264.
func hevcNAL(nalType byte, rbsp []byte) []byte {
	out := make([]byte, 0, len(rbsp)+2)
	out = append(out, nalType<<1, 0x01) // nuh_layer_id 0, nuh_temporal_id_plus1 1
	return append(out, escapeRBSP(rbsp)...)
}

var startCode = []byte{0x00, 0x00, 0x00, 0x01}

// AnnexB wraps an H.264 SPS RBSP in a start code and a NAL header, so a test
// can hand the reader an elementary stream rather than a bare parameter set.
func AnnexB(spsRBSP []byte) []byte {
	out := append([]byte{}, startCode...)
	out = append(out, 0x67) // nal_ref_idc 3, nal_unit_type 7 (SPS)
	return append(out, escapeRBSP(spsRBSP)...)
}

// escapeRBSP inserts the emulation prevention bytes a real encoder inserts, so
// the fixture exercises the reader's unescaping instead of quietly avoiding it.
func escapeRBSP(rbsp []byte) []byte {
	out := make([]byte, 0, len(rbsp))
	zeros := 0
	for _, b := range rbsp {
		if zeros >= 2 && b <= 0x03 {
			out = append(out, 0x03)
			zeros = 0
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

// HEVC NAL unit types for an opening picture. 16 through 21 are the random
// access points a segment can be switched into; anything below is not.
const (
	// HEVCIDRWRadl is the usual opening picture of a CMAF or HLS segment.
	HEVCIDRWRadl = byte(19)
	// HEVCCRANut is also a random access point, reached by a different route.
	HEVCCRANut = byte(21)
	// HEVCTrailR is an ordinary trailing picture: not switchable into.
	HEVCTrailR = byte(1)
)

// TSWithHEVCSPS is TS with an HEVC elementary stream: stream type 0x24 in the
// PMT, and a VPS/SPS/PPS ahead of the first frame. sps is the raw parameter set
// RBSP, without the start code and without the two NAL header bytes. The segment
// opens on an IDR.
func TSWithHEVCSPS(startPTS, frameDur int64, frames int, sps []byte) []byte {
	return TSWithHEVCSPSOpening(startPTS, frameDur, frames, sps, HEVCIDRWRadl)
}

// TSWithHEVCSPSOpening is TSWithHEVCSPS with the opening picture's NAL unit type
// chosen by the caller.
func TSWithHEVCSPSOpening(startPTS, frameDur int64, frames int, sps []byte, firstNAL byte) []byte {
	const (
		pmtPID   = 0x1000
		videoPID = 0x0100
		hevcType = 0x24
	)
	var out []byte
	out = append(out, tsPacket(0x0000, true, 0, pat(pmtPID))...)
	out = append(out, tsPacket(pmtPID, true, 0, pmt(videoPID, hevcType))...)

	var es []byte
	es = append(es, startCode...)
	es = append(es, hevcNAL(32, []byte{0x0c, 0x01, 0xff, 0xff})...) // VPS
	es = append(es, startCode...)
	es = append(es, hevcNAL(33, sps)...) // SPS
	es = append(es, startCode...)
	es = append(es, hevcNAL(34, []byte{0xc0, 0xf3, 0xc0, 0x02})...) // PPS
	es = append(es, startCode...)
	es = append(es, hevcNAL(firstNAL, []byte{0xaf, 0x1b})...) // the opening picture

	cc := 0
	out = append(out, tsPacket(videoPID, true, cc, pes(0xE0, startPTS, es))...)
	cc++
	for i := 1; i < frames; i++ {
		pts := startPTS + int64(i)*frameDur
		trail := append(append([]byte{}, startCode...), hevcNAL(1, []byte{0xaf, 0x1b})...)
		out = append(out, tsPacket(videoPID, true, cc&0x0F, pes(0xE0, pts, trail))...)
		cc++
	}
	return out
}
