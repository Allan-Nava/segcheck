package media

// Closed captions.
//
// CEA-608 and CEA-708 captions are not a track. They ride inside the video
// elementary stream, in an SEI message of type 4 —
// user_data_registered_itu_t_t35 — carrying the ATSC A/53 user data whose
// user_data_type_code 0x03 is a cc_data() payload. A manifest can therefore
// declare captions that the encoder stopped emitting three hours ago and no
// manifest-level checker will ever notice, because nothing in the manifest
// changed.
//
// What this reader reports, and what it deliberately does not:
//
//   - CEA-608 is reported per *field*. CC1 and CC3 share field 1, CC2 and CC4
//     share field 2, and separating them means decoding the line-21 control
//     codes and tracking channel-switch commands. That is a decoder, not a
//     reader, so the field is what is stated — and a field carrying nothing is
//     still a definite answer for every channel on it.
//   - CEA-708 is reported per *service number*, because the DTVCC packet layer
//     names them outright and SERVICE1 versus SERVICE6 is exactly what an
//     INSTREAM-ID declares.

// maxCaptionServices bounds the service numbers kept. CEA-708 allows 63, and a
// stream claiming more than a handful is malformed rather than ambitious.
const maxCaptionServices = 63

// captionScanNALUs bounds the walk. Captions ride on access units throughout a
// segment, but the check only needs to know whether any are there, and the first
// few access units answer that.
const captionScanNALUs = 4096

// CaptionPresence is what closed-caption data a segment actually carries.
//
// Scanned is the difference between "this segment has no captions" and "nobody
// looked", which lead to opposite verdicts: the first is a defect when the
// manifest declares captions, the second is a limit of this tool.
type CaptionPresence struct {
	// Field1 and Field2 are the CEA-608 line-21 fields. Field 1 carries CC1 and
	// CC3, field 2 carries CC2 and CC4.
	Field1 bool `json:"field1,omitempty"`
	Field2 bool `json:"field2,omitempty"`
	// Services are the CEA-708 DTVCC service numbers seen, ascending.
	Services []int `json:"services,omitempty"`
	// Track608 and Track708 record a CMAF closed-caption track — a c608 or c708
	// sample entry — that carries samples in this segment. This is how Apple's own
	// fMP4 reference stream delivers CEA-608, rather than in the video SEI.
	//
	// The track states the standard, not which field or service: attributing a
	// c608 track's data needs its samples located in the mdat and their cdat/cdt2
	// boxes read, which is SC-91. Until then a channel declared against such a
	// track can be neither confirmed nor disproved, and an empty caption track is
	// the whole defect anyway.
	Track608 bool `json:"track608,omitempty"`
	Track708 bool `json:"track708,omitempty"`
	// Scanned records that the bitstream was walked at all.
	Scanned bool `json:"scanned,omitempty"`
}

// Any reports whether any caption data was found.
func (c CaptionPresence) Any() bool {
	return c.Field1 || c.Field2 || len(c.Services) > 0 || c.Track608 || c.Track708
}

// Attributable reports whether the field or service carrying the captions is
// known. A CMAF caption track says which standard it is and no more, so a
// declared channel over one can be neither confirmed nor contradicted.
func (c CaptionPresence) Attributable() bool {
	return c.Field1 || c.Field2 || len(c.Services) > 0
}

// captionScanner accumulates presence across NAL units, reassembling the DTVCC
// packets that span several of them.
type captionScanner struct {
	out CaptionPresence
	// pkt is the DTVCC packet being reassembled. CEA-708 spreads one packet over
	// as many cc_data_pkt triplets as it needs, so a scanner that looked at each
	// triplet alone would never see a service block header.
	pkt []byte
}

// h264Captions walks an Annex-B stream for caption SEI messages.
func h264Captions(es []byte) CaptionPresence {
	var s captionScanner
	nalus, _ := annexBNALUsLimit(es, captionScanNALUs)
	for _, nal := range nalus {
		if len(nal) < 2 || nal[0]&0x1F != 6 {
			continue
		}
		s.readSEI(unescapeRBSP(nal[1:]))
	}
	s.flush()
	s.out.Scanned = true
	return s.out
}

// hevcCaptions is h264Captions for HEVC, whose NAL header is two bytes and whose
// prefix and suffix SEI are types 39 and 40 rather than 6.
func hevcCaptions(es []byte) CaptionPresence {
	var s captionScanner
	nalus, _ := annexBNALUsLimit(es, captionScanNALUs)
	for _, nal := range nalus {
		if len(nal) < 3 {
			continue
		}
		switch (nal[0] >> 1) & 0x3F {
		case 39, 40:
			s.readSEI(unescapeRBSP(nal[2:]))
		}
	}
	s.flush()
	s.out.Scanned = true
	return s.out
}

// lengthPrefixedCaptions walks the length-prefixed NAL units of an fMP4 mdat.
// There are no start codes there, so the Annex-B walk finds nothing at all.
func lengthPrefixedCaptions(samples []byte, lengthSize int, hevc bool) CaptionPresence {
	var s captionScanner
	if lengthSize < 1 || lengthSize > 4 {
		return s.out // not scanned: an unreadable length prefix is not an absence
	}
	units := 0
	for pos := 0; pos+lengthSize <= len(samples) && units < captionScanNALUs; units++ {
		n := 0
		for i := 0; i < lengthSize; i++ {
			n = n<<8 | int(samples[pos+i])
		}
		pos += lengthSize
		if n <= 0 || pos+n > len(samples) {
			break
		}
		nal := samples[pos : pos+n]
		pos += n

		switch {
		case hevc && len(nal) >= 3 && ((nal[0]>>1)&0x3F == 39 || (nal[0]>>1)&0x3F == 40):
			s.readSEI(unescapeRBSP(nal[2:]))
		case !hevc && len(nal) >= 2 && nal[0]&0x1F == 6:
			s.readSEI(unescapeRBSP(nal[1:]))
		}
	}
	s.flush()
	s.out.Scanned = true
	return s.out
}

// readSEI walks the SEI messages in one NAL's RBSP. Each message states its type
// and size as a chain of 0xFF bytes terminated by a smaller one.
func (s *captionScanner) readSEI(rbsp []byte) {
	pos := 0
	readChain := func() (int, bool) {
		total := 0
		for pos < len(rbsp) {
			b := rbsp[pos]
			pos++
			total += int(b)
			if b != 0xFF {
				return total, true
			}
		}
		return 0, false
	}
	for pos < len(rbsp) {
		if rbsp[pos] == 0x80 {
			return // rbsp_trailing_bits: the messages are over
		}
		payloadType, ok := readChain()
		if !ok {
			return
		}
		size, ok := readChain()
		if !ok || size < 0 || pos+size > len(rbsp) {
			return
		}
		if payloadType == 4 {
			s.readATSCUserData(rbsp[pos : pos+size])
		}
		pos += size
	}
}

// readATSCUserData reads a user_data_registered_itu_t_t35 payload, which carries
// captions only when it is the ATSC A/53 "GA94" flavour with type code 3.
func (s *captionScanner) readATSCUserData(b []byte) {
	const header = 9 // country, provider, identifier, type code
	if len(b) < header+2 {
		return
	}
	if b[0] != 0xB5 || b[1] != 0x00 || b[2] != 0x31 {
		return // not the United States / ATSC registration
	}
	if string(b[3:7]) != "GA94" || b[7] != 0x03 {
		return // not cc_data
	}
	count := int(b[8] & 0x1F)
	// b[9] is em_data; the triplets follow it.
	pkts := b[header+1:]
	if count*3 > len(pkts) {
		count = len(pkts) / 3
	}
	for i := 0; i < count; i++ {
		t := pkts[i*3]
		if t&0x04 == 0 {
			continue // cc_valid clear: this triplet carries no caption data
		}
		d1, d2 := pkts[i*3+1], pkts[i*3+2]
		switch t & 0x03 {
		case 0:
			s.out.Field1 = true
		case 1:
			s.out.Field2 = true
		case 3:
			// A new DTVCC packet begins; whatever was open is done.
			s.flush()
			s.pkt = []byte{d1, d2}
		case 2:
			if s.pkt != nil {
				s.pkt = append(s.pkt, d1, d2)
			}
		}
	}
}

// flush parses the DTVCC packet held open, recording the services it carries.
func (s *captionScanner) flush() {
	pkt := s.pkt
	s.pkt = nil
	if len(pkt) < 2 {
		return
	}
	// The header byte codes the whole packet, itself included, as 2*packet_size
	// bytes; 0 means 128. Anything longer than what arrived is a truncated packet,
	// and the blocks that did arrive are still readable.
	size := int(pkt[0]&0x3F) * 2
	if size == 0 {
		size = 128
	}
	if size > len(pkt) {
		size = len(pkt)
	}
	data := pkt[1:size]

	for i := 0; i < len(data); {
		h := data[i]
		i++
		service := int(h >> 5)
		blockSize := int(h & 0x1F)
		if service == 7 && blockSize != 0 {
			// The extended header: the real service number in the next byte.
			if i >= len(data) {
				return
			}
			service = int(data[i] & 0x3F)
			i++
		}
		if service == 0 {
			return // the null service is padding, and the packet ends there
		}
		if service <= maxCaptionServices {
			s.addService(service)
		}
		i += blockSize
	}
}

// addService records a service number once, keeping the list ascending so the
// output is stable whatever order the packets arrived in.
func (s *captionScanner) addService(n int) {
	for i, v := range s.out.Services {
		switch {
		case v == n:
			return
		case v > n:
			s.out.Services = append(s.out.Services, 0)
			copy(s.out.Services[i+1:], s.out.Services[i:])
			s.out.Services[i] = n
			return
		}
	}
	s.out.Services = append(s.out.Services, n)
}
