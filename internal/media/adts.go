package media

import (
	"errors"
	"fmt"
)

// Packed audio.
//
// HLS allows an audio rendition to be delivered as a bare elementary stream
// rather than wrapped in MPEG-TS or fMP4 — Apple's own reference streams do it,
// and treating those segments as an unknown container would report a defect
// where there is none.
//
// A packed-audio segment carries no PTS of its own. The spec puts the timestamp
// in an ID3 PRIV tag at the head of the segment, which is what makes continuity
// checking possible here at all: parse the tag for where the segment starts, and
// count ADTS frames for how long it lasts.

// ErrUnsupportedContainer marks bytes segcheck recognises but cannot analyse.
// It is a limit of this tool, not a defect in the stream, and the checks report
// it as such.
var ErrUnsupportedContainer = errors.New("recognised container, not analysed")

// appleTimestampOwner is the ID3 PRIV owner that carries a packed-audio
// segment's MPEG-2 timestamp, on the same 90kHz clock as MPEG-TS.
const appleTimestampOwner = "com.apple.streaming.transportStreamTimestamp"

// adtsChannelCounts maps channel_configuration to a channel count. Only index 7
// differs from itself: it means 7.1, which is eight channels.
var adtsChannelCounts = [8]int{0, 1, 2, 3, 4, 5, 6, 8}

// adtsHeaderFields reads the sampling rate and channel count from the first ADTS
// header in b. MPEG-TS states neither in the container, so for an AAC track the
// bitstream is the only place the numbers exist.
func adtsHeaderFields(b []byte) (rate, channels int, ok bool) {
	for off := 0; off+4 <= len(b); off++ {
		if b[off] != 0xFF || b[off+1]&0xF6 != 0xF0 {
			continue
		}
		rate = adtsSampleRates[(b[off+2]>>2)&0x0F]
		cfg := (b[off+2]&0x01)<<2 | (b[off+3] >> 6)
		channels = adtsChannelCounts[cfg&0x07]
		if rate == 0 || channels == 0 {
			// A reserved index, or a configuration that defers the layout to the
			// AudioSpecificConfig: unknown beats a made-up number.
			return 0, 0, false
		}
		return rate, channels, true
	}
	return 0, 0, false
}

// adtsSampleRates is the sampling_frequency_index table.
var adtsSampleRates = [16]int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350, 0, 0, 0}

// ParsePackedAudio parses a bare audio elementary stream: an optional ID3 tag
// followed by ADTS AAC frames.
func ParsePackedAudio(data []byte) (SegmentInfo, error) {
	info := SegmentInfo{Container: ContainerPackedAudio, Bytes: int64(len(data))}

	body := data
	var startPTS int64
	var havePTS bool
	if n, ts, ok := parseID3(data); n > 0 {
		body = data[n:]
		if ok {
			startPTS, havePTS = ts, true
		}
	}

	switch {
	case isADTS(body):
		frames, samples, rate, channels, err := scanADTS(body)
		if err != nil {
			return info, err
		}
		track := Track{
			ID:        1,
			Kind:      Audio,
			Codec:     "aac",
			Timescale: TSTimescale, // report on the 90kHz clock, like MPEG-TS
			Samples:   frames,
			HasPTS:    havePTS,
			MinPTS:    startPTS,
		}
		if rate > 0 {
			// Convert the sample count to 90kHz ticks so a packed-audio
			// rendition sits on the same timeline as a TS one.
			track.StatedDur = int64(samples) * int64(TSTimescale) / int64(rate)
			if frames > 0 {
				track.FrameDur = track.StatedDur / int64(frames)
			}
		}
		track.MaxPTS = startPTS + track.StatedDur - track.FrameDur
		info.Tracks = append(info.Tracks, track)
		if channels > 0 {
			info.Channels = channels
			info.Tracks[0].Channels = channels
		}
		if rate > 0 {
			info.Tracks[0].SampleRate = rate
		}
		return info, nil

	case isMPEGAudio(body):
		// MP3 frame sizes need the full bitrate tables; recognising the format
		// is enough to keep it out of the defect list.
		return info, fmt.Errorf("%w: MPEG audio (MP3) packed audio", ErrUnsupportedContainer)

	case len(body) == 0:
		return info, fmt.Errorf("packed audio segment is an ID3 tag with no audio frames")

	default:
		return info, ErrUnknownContainer
	}
}

// scanADTS walks the frame headers, returning the frame count, the total sample
// count, the sampling rate and the channel configuration.
func scanADTS(b []byte) (frames, samples, rate, channels int, err error) {
	for off := 0; off+7 <= len(b); {
		if b[off] != 0xFF || b[off+1]&0xF0 != 0xF0 {
			// Lost sync. A trailing partial frame is normal at the end of a
			// segment; anything earlier means the stream is not what it claims.
			if frames > 0 {
				break
			}
			return 0, 0, 0, 0, ErrUnknownContainer
		}
		protectionAbsent := b[off+1]&0x01 == 1
		rateIdx := (b[off+2] >> 2) & 0x0F
		chanCfg := ((b[off+2]&0x01)<<2 | (b[off+3] >> 6)) & 0x07
		frameLen := int(b[off+3]&0x03)<<11 | int(b[off+4])<<3 | int(b[off+5])>>5
		rawBlocks := int(b[off+6]&0x03) + 1

		headerLen := 7
		if !protectionAbsent {
			headerLen = 9 // a 16-bit CRC follows the fixed header
		}
		if frameLen < headerLen || off+frameLen > len(b) {
			if frames > 0 {
				break // truncated final frame
			}
			return 0, 0, 0, 0, ErrUnknownContainer
		}
		if frames == 0 {
			rate = adtsSampleRates[rateIdx]
			channels = adtsChannelCounts[chanCfg]
			if rate == 0 {
				return 0, 0, 0, 0, fmt.Errorf("ADTS frame declares reserved sampling_frequency_index %d", rateIdx)
			}
		}
		frames++
		samples += 1024 * rawBlocks // one AAC frame is 1024 samples per block
		off += frameLen
	}
	if frames == 0 {
		return 0, 0, 0, 0, ErrUnknownContainer
	}
	return frames, samples, rate, channels, nil
}

// parseID3 returns the byte length of a leading ID3v2 tag and, when the tag
// carries one, the segment's 90kHz timestamp.
func parseID3(b []byte) (length int, timestamp int64, ok bool) {
	if len(b) < 10 || b[0] != 'I' || b[1] != 'D' || b[2] != '3' {
		return 0, 0, false
	}
	major := b[3]
	size := syncsafe(b[6:10])
	total := 10 + size
	if total > len(b) {
		return 0, 0, false // a truncated tag: treat the bytes as audio
	}
	ts, found := findAppleTimestamp(b[10:total], major)
	return total, ts, found
}

// findAppleTimestamp walks the tag's frames looking for the PRIV frame that
// carries the transport stream timestamp.
func findAppleTimestamp(frames []byte, major byte) (int64, bool) {
	for off := 0; off+10 <= len(frames); {
		id := string(frames[off : off+4])
		if id == "\x00\x00\x00\x00" {
			return 0, false // padding: the frames are over
		}
		var size int
		if major >= 4 {
			size = syncsafe(frames[off+4 : off+8])
		} else {
			size = int(frames[off+4])<<24 | int(frames[off+5])<<16 | int(frames[off+6])<<8 | int(frames[off+7])
		}
		body := off + 10
		if size <= 0 || body+size > len(frames) {
			return 0, false
		}
		if id == "PRIV" {
			payload := frames[body : body+size]
			if i := indexByte(payload, 0); i >= 0 {
				owner := string(payload[:i])
				value := payload[i+1:]
				if owner == appleTimestampOwner && len(value) >= 8 {
					// A 33-bit MPEG-2 timestamp in the low bits of eight bytes.
					v := int64(be64(value[:8])) & (PTSModulus - 1)
					return v, true
				}
			}
		}
		off = body + size
	}
	return 0, false
}

// syncsafe decodes the seven-bits-per-byte integer ID3 uses for sizes.
func syncsafe(b []byte) int {
	if len(b) < 4 {
		return 0
	}
	return int(b[0]&0x7F)<<21 | int(b[1]&0x7F)<<14 | int(b[2]&0x7F)<<7 | int(b[3]&0x7F)
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

// isADTS reports whether b starts with an ADTS AAC frame header.
func isADTS(b []byte) bool {
	return len(b) >= 7 && b[0] == 0xFF && b[1]&0xF6 == 0xF0
}

// isMPEGAudio reports whether b starts with an MPEG-1/2 audio frame header.
func isMPEGAudio(b []byte) bool {
	return len(b) >= 4 && b[0] == 0xFF && b[1]&0xE0 == 0xE0 && b[1]&0x18 != 0x08
}

// looksPackedAudio reports whether the bytes are a bare audio elementary
// stream, possibly behind an ID3 tag.
func looksPackedAudio(data []byte) bool {
	body := data
	if n, _, _ := parseID3(data); n > 0 {
		body = data[n:]
	}
	return isADTS(body) || isMPEGAudio(body)
}
