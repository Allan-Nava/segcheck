package media

// An fMP4 audio track's real configuration is not in its AudioSampleEntry, it is
// in the box beside it: `esds` for AAC, `dac3`/`dec3` for AC-3 and E-AC-3,
// `dOps` for Opus, `dfLa` for FLAC. That is the same split video already has —
// where the container states it, no bitstream reader is needed — and it gives an
// audio track the same footing.
//
// The distinction that makes it worth reading at all is between what a track
// *codes* and what it *renders*. The sample entry describes the output, and for
// HE-AAC that is deliberately not the coding: the core runs at half the rate SBR
// plays it at, and HE-AAC v2 codes a mono core that Parametric Stereo renders as
// stereo. A checker that compared a manifest's 48 kHz against the entry's 24 kHz
// reported correct media as wrong, which is a mistake this project has already
// made once and documented.

// AudioConfig is what a codec configuration box says about how the samples are
// coded, as distinct from what the track renders.
type AudioConfig struct {
	// ObjectType is the MPEG-4 audio object type for AAC: 2 is AAC-LC, 5 is
	// HE-AAC's SBR extension, 29 is HE-AAC v2's Parametric Stereo. Zero for the
	// codecs that have no such number.
	ObjectType int `json:"object_type,omitempty"`
	// CodedSampleRate and CodedChannels are the rate and channel count the
	// configuration states — the core, not the rendered output. For HE-AAC the
	// rendered rate is twice this and the rendered channel count may be twice
	// this too, and both of those are correct.
	CodedSampleRate int `json:"coded_sample_rate,omitempty"`
	CodedChannels   int `json:"coded_channels,omitempty"`
	// SBR and PS record the extensions that make the coded and rendered figures
	// differ, so a check can explain a doubling rather than report it.
	SBR bool `json:"sbr,omitempty"`
	PS  bool `json:"ps,omitempty"`
	// Stated is false when no configuration box was found. A zero channel count
	// is not a claim of silence.
	Stated bool `json:"stated,omitempty"`
}

// ascSampleRates is the sampling frequency table an AudioSpecificConfig indexes
// into. It is fixed by the standard; the trailing entries are reserved, and index
// 15 means an explicit 24-bit rate follows instead.
var ascSampleRates = [16]int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350, 0, 0, 0}

// audioConfigFromEntry reads whichever configuration box an AudioSampleEntry
// carries beside its fixed fields.
func audioConfigFromEntry(typ string, payload []byte) AudioConfig {
	if len(payload) <= audioSampleEntrySize {
		return AudioConfig{}
	}
	children := payload[audioSampleEntrySize:]
	if esds, ok := findBox(children, "esds"); ok {
		if cfg, ok := parseESDS(esds); ok {
			return cfg
		}
	}
	if dops, ok := findBox(children, "dOps"); ok {
		if cfg, ok := parseDOps(dops); ok {
			return cfg
		}
	}
	if dfla, ok := findBox(children, "dfLa"); ok {
		if cfg, ok := parseDFLA(dfla); ok {
			return cfg
		}
	}
	// AC-3 and E-AC-3 are already read for their channel layout, which is the
	// whole of what their configuration boxes state that matters here.
	for _, b := range boxesIn(children) {
		var ch, rate int
		var ok bool
		switch b.typ {
		case "dac3":
			ch, rate, ok = parseDAC3(b.payload)
		case "dec3":
			ch, rate, ok = parseDEC3(b.payload)
		default:
			continue
		}
		if ok {
			return AudioConfig{CodedChannels: ch, CodedSampleRate: rate, Stated: true}
		}
	}
	_ = typ
	return AudioConfig{}
}

// parseESDS walks the ES_Descriptor to the AudioSpecificConfig.
//
// The descriptors are tag-length-value with a variable-length length: each byte
// carries seven bits and a continuation flag, so a reader that assumed one byte
// works on most files and lands in the middle of the payload on the rest.
func parseESDS(b []byte) (AudioConfig, bool) {
	if len(b) < 4 {
		return AudioConfig{}, false
	}
	body := b[4:] // version and flags
	esd, ok := descriptorPayload(body, 0x03)
	if !ok {
		return AudioConfig{}, false
	}
	// ES_Descriptor: ES_ID (2 bytes) and a flags byte, then optional fields the
	// flags select, then the DecoderConfigDescriptor.
	if len(esd) < 3 {
		return AudioConfig{}, false
	}
	flags := esd[2]
	off := 3
	if flags&0x80 != 0 { // streamDependenceFlag
		off += 2
	}
	if flags&0x40 != 0 { // URL_Flag
		if off >= len(esd) {
			return AudioConfig{}, false
		}
		off += 1 + int(esd[off])
	}
	if flags&0x20 != 0 { // OCRstreamFlag
		off += 2
	}
	if off >= len(esd) {
		return AudioConfig{}, false
	}
	dcd, ok := descriptorPayload(esd[off:], 0x04)
	if !ok {
		return AudioConfig{}, false
	}
	// DecoderConfigDescriptor: objectTypeIndication, streamType and buffer
	// figures — thirteen bytes — then the DecoderSpecificInfo.
	if len(dcd) < 13 {
		return AudioConfig{}, false
	}
	asc, ok := descriptorPayload(dcd[13:], 0x05)
	if !ok {
		return AudioConfig{}, false
	}
	return parseAudioSpecificConfig(asc)
}

// descriptorPayload finds the first descriptor of the given tag and returns its
// payload, honouring the variable-length length encoding.
func descriptorPayload(b []byte, tag byte) ([]byte, bool) {
	for pos := 0; pos < len(b); {
		t := b[pos]
		pos++
		length := 0
		for i := 0; i < 4; i++ {
			if pos >= len(b) {
				return nil, false
			}
			c := b[pos]
			pos++
			length = length<<7 | int(c&0x7F)
			if c&0x80 == 0 {
				break
			}
		}
		if length < 0 || pos+length > len(b) {
			return nil, false
		}
		if t == tag {
			return b[pos : pos+length], true
		}
		pos += length
	}
	return nil, false
}

// parseAudioSpecificConfig reads the object type, sampling frequency and channel
// configuration, and the SBR and PS signalling that makes the coded figures
// differ from the rendered ones.
func parseAudioSpecificConfig(asc []byte) (AudioConfig, bool) {
	if len(asc) < 2 {
		return AudioConfig{}, false
	}
	r := &bitReader{data: asc}
	aot := readAudioObjectType(r)
	freqIndex := int(r.bits(4))
	rate := 0
	if freqIndex == 15 {
		rate = int(r.bits(24))
	} else if freqIndex < len(ascSampleRates) {
		rate = ascSampleRates[freqIndex]
	}
	channelCfg := int(r.bits(4))
	if r.err {
		return AudioConfig{}, false
	}

	cfg := AudioConfig{ObjectType: aot, CodedSampleRate: rate, CodedChannels: channelCfg, Stated: true}
	switch aot {
	case 5: // the SBR extension, signalled explicitly
		cfg.SBR = true
	case 29: // Parametric Stereo, which implies SBR
		cfg.SBR, cfg.PS = true, true
	}
	if rate <= 0 || rate > maxAudioSampleRate {
		cfg.CodedSampleRate = 0
	}
	if channelCfg < 0 || channelCfg > maxAudioChannels {
		cfg.CodedChannels = 0
	}
	return cfg, true
}

// readAudioObjectType reads the five-bit form, or the escape value plus six more
// bits for a type of 32 or above.
func readAudioObjectType(r *bitReader) int {
	aot := int(r.bits(5))
	if aot == 31 {
		return 32 + int(r.bits(6))
	}
	return aot
}

// parseDOps reads the Opus configuration. Opus always decodes to 48 kHz whatever
// the input rate was, so that is the rate the track really runs at and
// InputSampleRate is only a note about the original material.
func parseDOps(b []byte) (AudioConfig, bool) {
	if len(b) < 11 {
		return AudioConfig{}, false
	}
	channels := int(b[1])
	if channels <= 0 || channels > maxAudioChannels {
		return AudioConfig{}, false
	}
	return AudioConfig{CodedChannels: channels, CodedSampleRate: 48000, Stated: true}, true
}

// parseDFLA reads the FLAC STREAMINFO block, whose sample rate is twenty bits
// and whose channel count is three bits one less than the real number.
func parseDFLA(b []byte) (AudioConfig, bool) {
	if len(b) < 4+4+18 {
		return AudioConfig{}, false
	}
	// version and flags, then a metadata block header of four bytes.
	si := b[8:]
	r := &bitReader{data: si}
	r.bits(16) // min_blocksize
	r.bits(16) // max_blocksize
	r.bits(24) // min_framesize
	r.bits(24) // max_framesize
	rate := int(r.bits(20))
	channels := int(r.bits(3)) + 1
	if r.err || rate <= 0 || rate > maxAudioSampleRate || channels > maxAudioChannels {
		return AudioConfig{}, false
	}
	return AudioConfig{CodedChannels: channels, CodedSampleRate: rate, Stated: true}, true
}
