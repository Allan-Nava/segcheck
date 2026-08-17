package mediatest

// Packed audio builders: an ID3 tag carrying the Apple transport-stream
// timestamp, followed by ADTS AAC frames. This is how HLS delivers audio-only
// renditions when they are not wrapped in MPEG-TS or fMP4.

const (
	// adtsRateIndex 3 is 48000 Hz, so one 1024-sample frame is exactly 1920
	// ticks of the 90kHz clock — no rounding in the test arithmetic.
	adtsRateIndex   = 3
	adtsSampleRate  = 48000
	adtsChannelCfg  = 2 // stereo
	adtsPayloadSize = 100
	// ADTSTicksPerFrame is one AAC frame in 90kHz ticks, at the rate above.
	ADTSTicksPerFrame = int64(1024) * 90000 / adtsSampleRate
)

// PackedAudio builds an ADTS AAC segment preceded by an ID3 tag stating
// startPTS on the 90kHz clock.
func PackedAudio(startPTS int64, frames int) []byte {
	out := ID3Timestamp(startPTS)
	for i := 0; i < frames; i++ {
		out = append(out, adtsFrame(adtsPayloadSize)...)
	}
	return out
}

// PackedAudioAt is PackedAudio at a chosen sampling rate and channel count, for
// a caller that needs the format to be something other than the 48kHz stereo
// default — or to change part-way through a rendition.
func PackedAudioAt(startPTS int64, frames, sampleRate, channels int) []byte {
	out := ID3Timestamp(startPTS)
	for i := 0; i < frames; i++ {
		out = append(out, ADTSFrame(sampleRate, channels, adtsPayloadSize)...)
	}
	return out
}

// PackedAudioNoID3 is PackedAudio without the timestamp tag: parseable, but with
// no timeline of its own.
func PackedAudioNoID3(frames int) []byte {
	var out []byte
	for i := 0; i < frames; i++ {
		out = append(out, adtsFrame(adtsPayloadSize)...)
	}
	return out
}

// ID3Timestamp builds an ID3v2.4 tag holding one PRIV frame with the Apple
// transport-stream timestamp.
func ID3Timestamp(pts int64) []byte {
	owner := "com.apple.streaming.transportStreamTimestamp"
	body := append([]byte(owner), 0x00)
	body = append(body, u64(uint64(pts))...)

	frame := []byte("PRIV")
	frame = append(frame, syncsafeBytes(len(body))...)
	frame = append(frame, 0x00, 0x00) // flags
	frame = append(frame, body...)

	tag := []byte("ID3")
	tag = append(tag, 0x04, 0x00) // version 2.4
	tag = append(tag, 0x00)       // flags
	tag = append(tag, syncsafeBytes(len(frame))...)
	return append(tag, frame...)
}

// adtsFrame builds one ADTS AAC frame header plus payloadSize bytes of payload.
func adtsFrame(payloadSize int) []byte {
	return adtsFrameAt(adtsRateIndex, adtsChannelCfg, payloadSize)
}

// adtsSampleRateIndices maps a sampling rate to the index ADTS states it by. The
// table is duplicated here rather than shared with the parser on purpose: a
// builder that derived its answer from the reader under test would agree with it
// however wrong the reader was.
var adtsSampleRateIndices = map[int]int{
	96000: 0, 88200: 1, 64000: 2, 48000: 3, 44100: 4, 32000: 5,
	24000: 6, 22050: 7, 16000: 8, 12000: 9, 11025: 10, 8000: 11, 7350: 12,
}

// ADTSHeaderBytes is the seven-byte header alone, for a caller that needs to
// place it somewhere by hand rather than as a whole frame.
func ADTSHeaderBytes(sampleRate, channels int) []byte {
	return ADTSFrame(sampleRate, channels, 0)[:7]
}

// ADTSFrame builds one frame stating a sampling rate and channel count, for a
// caller that needs a format other than the package default.
func ADTSFrame(sampleRate, channels, payloadSize int) []byte {
	idx, ok := adtsSampleRateIndices[sampleRate]
	if !ok {
		idx = adtsRateIndex
	}
	// channel_configuration is an index, not a count: 7 means eight channels.
	cfg := channels
	if channels == 8 {
		cfg = 7
	}
	return adtsFrameAt(idx, cfg, payloadSize)
}

func adtsFrameAt(rateIndex, channelCfg, payloadSize int) []byte {
	const headerLen = 7
	frameLen := headerLen + payloadSize

	h := make([]byte, headerLen)
	h[0] = 0xFF
	h[1] = 0xF1 // MPEG-4, layer 00, protection_absent = 1 (no CRC)
	h[2] = byte(1<<6) | byte(rateIndex<<2) | byte(channelCfg>>2)
	h[3] = byte((channelCfg&0x03)<<6) | byte(frameLen>>11)
	h[4] = byte((frameLen >> 3) & 0xFF)
	// buffer fullness 0x7FF (variable rate) and one raw data block.
	h[5] = byte((frameLen&0x07)<<5) | 0x1F
	h[6] = 0xFC

	return append(h, make([]byte, payloadSize)...)
}

func syncsafeBytes(n int) []byte {
	return []byte{
		byte((n >> 21) & 0x7F),
		byte((n >> 14) & 0x7F),
		byte((n >> 7) & 0x7F),
		byte(n & 0x7F),
	}
}
