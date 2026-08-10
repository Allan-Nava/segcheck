// Package media parses media segments — MPEG-TS and fragmented MP4 (CMAF) —
// with the standard library only. No ffmpeg, no ffprobe, no cgo: what segcheck
// reports about a segment comes from reading its bytes here.
//
// The unit of output is SegmentInfo: which tracks the segment carries, their
// codecs and real resolution, and their presentation timeline in timescale
// units. Everything segcheck asserts (continuity, duration drift, measured
// bitrate, ABR alignment) is derived from that.
package media

import (
	"errors"
	"fmt"
	"sort"
)

// Container of a parsed segment.
const (
	ContainerTS  = "ts"
	ContainerMP4 = "mp4"
	// ContainerPackedAudio is a bare audio elementary stream (ADTS AAC or MP3),
	// which HLS permits for audio-only renditions.
	ContainerPackedAudio = "packed-audio"
)

// TrackKind is the broad type of an elementary stream.
type TrackKind string

const (
	Video TrackKind = "video"
	Audio TrackKind = "audio"
	Other TrackKind = "other"
)

// PTSModulus is the wrap point of an MPEG-TS 33-bit presentation timestamp.
const PTSModulus = int64(1) << 33

// Track is one elementary stream inside a segment.
type Track struct {
	ID    uint32    `json:"id"`
	Kind  TrackKind `json:"kind"`
	Codec string    `json:"codec,omitempty"`
	// Width and Height are the coded resolution as the bitstream declares it
	// (H.264 SPS for MPEG-TS, the visual sample entry for MP4). Zero when the
	// container did not state it and it could not be parsed.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	// Timescale is the unit of every timestamp below: ticks per second.
	Timescale uint32 `json:"timescale"`
	// MinPTS and MaxPTS bound the presentation interval. They are min/max, not
	// first/last: with B-frames the stream is not in presentation order.
	MinPTS  int64 `json:"min_pts"`
	MaxPTS  int64 `json:"max_pts"`
	HasPTS  bool  `json:"has_pts"`
	Samples int   `json:"samples"`
	// StatedDur is the duration the container states outright (the sum of MP4
	// sample durations). Zero when it has to be derived from the PTS span.
	StatedDur int64 `json:"stated_dur,omitempty"`
	// FrameDur is the median gap between consecutive presentation timestamps —
	// the tail of the last frame, which the PTS span alone does not cover.
	FrameDur int64 `json:"frame_dur,omitempty"`
	// Encrypted marks a track whose samples are protected (encv/enca sample
	// entry, or a TS payload flagged scrambled).
	Encrypted bool `json:"encrypted,omitempty"`
	// CCErrors counts breaks in the MPEG-TS continuity counter: packets the
	// transport lost between the packager and us. Always 0 for fMP4, which has
	// no equivalent packet-level counter.
	CCErrors int `json:"cc_errors,omitempty"`
}

// StartSec is the start of the track's presentation interval, in seconds.
func (t Track) StartSec() (float64, bool) {
	if !t.HasPTS || t.Timescale == 0 {
		return 0, false
	}
	return float64(t.MinPTS) / float64(t.Timescale), true
}

// DurationSec is the real duration of the track's media in this segment.
//
// When the container states the duration (MP4 sample durations) that value
// wins. Otherwise it is the PTS span plus one frame: the span from first to
// last timestamp leaves out how long the last frame is shown, which for a
// 25fps segment is a 40ms understatement — enough to trip a 1% drift check.
func (t Track) DurationSec() (float64, bool) {
	if t.Timescale == 0 {
		return 0, false
	}
	if t.StatedDur > 0 {
		return float64(t.StatedDur) / float64(t.Timescale), true
	}
	if !t.HasPTS || t.Samples < 2 {
		return 0, false
	}
	return float64(t.MaxPTS-t.MinPTS+t.FrameDur) / float64(t.Timescale), true
}

// SegmentInfo is everything a single parsed segment tells us.
type SegmentInfo struct {
	Container string  `json:"container"`
	Bytes     int64   `json:"bytes"`
	Tracks    []Track `json:"tracks"`
	// Sequence is the fMP4 fragment sequence number (moof/mfhd); 0 for TS.
	Sequence uint32 `json:"sequence,omitempty"`
	// Channels is the channel configuration of packed audio, 0 when unknown.
	Channels int `json:"channels,omitempty"`
}

// Track returns the first track of the given kind.
func (s SegmentInfo) Track(kind TrackKind) (Track, bool) {
	for _, t := range s.Tracks {
		if t.Kind == kind {
			return t, true
		}
	}
	return Track{}, false
}

// Timeline returns the track that defines the segment's timeline: video when
// present, else audio, else the first track with timestamps. Every cross-
// segment assertion uses one consistent track so it never compares a video
// start against an audio start.
func (s SegmentInfo) Timeline() (Track, bool) {
	for _, kind := range []TrackKind{Video, Audio} {
		if t, ok := s.Track(kind); ok && t.HasPTS {
			return t, true
		}
	}
	for _, t := range s.Tracks {
		if t.HasPTS {
			return t, true
		}
	}
	return Track{}, false
}

// Encrypted reports whether any track in the segment is protected.
func (s SegmentInfo) Encrypted() bool {
	for _, t := range s.Tracks {
		if t.Encrypted {
			return true
		}
	}
	return false
}

// Codecs lists the codecs present, video first, for display.
func (s SegmentInfo) Codecs() []string {
	var out []string
	for _, kind := range []TrackKind{Video, Audio, Other} {
		for _, t := range s.Tracks {
			if t.Kind == kind && t.Codec != "" {
				out = append(out, t.Codec)
			}
		}
	}
	return out
}

// ErrUnknownContainer is returned for bytes that are neither MPEG-TS nor
// ISO-BMFF. It is a finding about the stream, not a bug: an origin serving
// HTML error pages with a 200 lands here.
var ErrUnknownContainer = errors.New("unrecognised container: not MPEG-TS (no 0x47 sync), not ISO-BMFF (no box header), not packed audio")

// Parse detects the container and parses the segment. init may be nil; for
// fMP4 it is the EXT-X-MAP / SegmentTemplate initialisation segment, which is
// where the timescale, codecs and resolution live — without it a media
// fragment only yields timestamps.
func Parse(data, init []byte) (SegmentInfo, error) {
	switch {
	case looksMP4(data), looksMP4(init):
		return ParseMP4(data, init)
	case looksTS(data):
		return ParseTS(data)
	case looksPackedAudio(data):
		return ParsePackedAudio(data)
	default:
		return SegmentInfo{}, ErrUnknownContainer
	}
}

// looksTS reports whether data plausibly starts an MPEG-TS stream: a 0x47 sync
// byte that repeats at the 188-byte packet stride.
func looksTS(data []byte) bool {
	if len(data) < 188*2 {
		return len(data) >= 188 && data[0] == 0x47
	}
	return data[0] == 0x47 && data[188] == 0x47
}

// looksMP4 reports whether data starts with an ISO-BMFF box we expect at the
// head of an init or media segment.
func looksMP4(data []byte) bool {
	if len(data) < 8 {
		return false
	}
	switch string(data[4:8]) {
	case "ftyp", "styp", "moof", "moov", "sidx", "free", "skip", "emsg":
		return true
	}
	return false
}

// medianDelta is the median gap between sorted presentation timestamps, used as
// the frame duration. The median rather than the mean so a single discontinuity
// inside the segment does not distort it.
func medianDelta(pts []int64) int64 {
	if len(pts) < 2 {
		return 0
	}
	sorted := make([]int64, len(pts))
	copy(sorted, pts)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	deltas := make([]int64, 0, len(sorted)-1)
	for i := 1; i < len(sorted); i++ {
		if d := sorted[i] - sorted[i-1]; d > 0 {
			deltas = append(deltas, d)
		}
	}
	if len(deltas) == 0 {
		return 0
	}
	sort.Slice(deltas, func(i, j int) bool { return deltas[i] < deltas[j] })
	return deltas[len(deltas)/2]
}

// UnwrapDelta returns b-a corrected for MPEG-TS 33-bit PTS wraparound. A jump
// beyond half the modulus in either direction is read as a wrap rather than as
// a ~13-hour seek, which is the only interpretation that makes sense for two
// consecutive segments.
func UnwrapDelta(a, b int64) int64 {
	d := b - a
	switch {
	case d > PTSModulus/2:
		return d - PTSModulus
	case d < -PTSModulus/2:
		return d + PTSModulus
	default:
		return d
	}
}

func (t TrackKind) String() string { return string(t) }

// Describe renders a track for human output: "h264 1920x1080" / "aac".
func (t Track) Describe() string {
	s := t.Codec
	if s == "" {
		s = string(t.Kind)
	}
	if t.Width > 0 && t.Height > 0 {
		s += fmt.Sprintf(" %dx%d", t.Width, t.Height)
	}
	return s
}
