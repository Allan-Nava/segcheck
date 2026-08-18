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
	ContainerWebVTT      = "webvtt"
	ContainerTTML        = "ttml"
)

// TrackKind is the broad type of an elementary stream.
type TrackKind string

const (
	Video TrackKind = "video"
	Audio TrackKind = "audio"
	// Text is a subtitle or caption track: a WebVTT or TTML segment, or an fMP4
	// track with a wvtt or stpp sample entry.
	Text  TrackKind = "text"
	Other TrackKind = "other"
)

// maxCodedDimension bounds a frame size the readers will report.
//
// The bitstream readers have always refused anything larger, because a parameter
// set read a few bits out of step yields a plausible-looking but absurd number.
// The container readers did not, so a malformed tkhd or sample entry could report
// a 16688x12336 rendition — and `resolution` would then report a mismatch against
// a manifest that says nothing of the kind. One rule for both: a size past this is
// not a measurement, it is a misread.
const maxCodedDimension = 16384

// plausibleResolution reports whether a frame size is one the readers will state.
func plausibleResolution(w, h int) bool {
	return w > 0 && h > 0 && w <= maxCodedDimension && h <= maxCodedDimension
}

// The bounds on what counts as a stated audio format. 22.2 surround is 24
// channels and the highest rate anyone ships is 384kHz; past either, the reader is
// looking at the wrong bytes rather than at an unusual stream.
const (
	maxAudioChannels   = 64
	maxAudioSampleRate = 384000
)

// maxPlausibleFPS bounds what counts as a measured frame rate. High-speed capture
// reaches a few hundred; anything past this is arithmetic on timestamps that did
// not advance, not a rate the pictures are shown at.
const maxPlausibleFPS = 1000

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
	// SampleRate and Channels are what an audio track actually runs at, read from
	// the AudioSampleEntry in fMP4 and from the ADTS header in MPEG-TS and packed
	// audio. Zero when the container did not state it.
	//
	// A rendition whose either value changes mid-stream forces a decoder reset,
	// which most players show as a gap in the audio — and the manifest cannot
	// reveal it, because it states one value for the whole rendition.
	SampleRate int `json:"sample_rate,omitempty"`
	Channels   int `json:"channels,omitempty"`
	// Cues is how many subtitle cues a text track's samples carry, and CuesRead
	// whether they were readable at all. Zero cues and no cue count lead to
	// opposite verdicts: the first is a rendition that says nothing, the second one
	// nobody could look inside.
	//
	// For a WebVTT or TTML segment the cues are the samples, so Samples and Cues
	// agree. For an fMP4-wrapped track they do not: a sample may hold several cues,
	// or the empty-cue box that holds none.
	Cues     int  `json:"cues,omitempty"`
	CuesRead bool `json:"cues_read,omitempty"`
	// CueMin and CueMax are where the cues actually sit on the media timeline, on
	// this track's timescale: the earliest cue start to the latest cue end. For a
	// text segment they are MinPTS and MaxPTS; for an fMP4-wrapped one they come from
	// the cue times inside the samples plus the fragment's own decode time, which is
	// the only way a wrapped rendition can be checked for the same drift a WebVTT one
	// is. HasCueSpan is false when the format states no per-cue timing to derive them
	// from — a wvtt sample times its cue by the sample's duration, not inside it.
	CueMin, CueMax int  `json:"-"`
	HasCueSpan     bool `json:"has_cue_span,omitempty"`
	// CuesAnchored reports whether anything ties the cue clock to the media clock. A
	// WebVTT segment needs X-TIMESTAMP-MAP for that, and HLS requires it — but DASH
	// does not use the tag at all and puts cue times on the presentation timeline
	// directly, so its absence is only a problem in HLS. TTML is absolute by
	// definition and is always anchored.
	CuesAnchored bool `json:"cues_anchored,omitempty"`
	// Captions is the closed-caption data found in a video track's bitstream.
	// Its Scanned field is what separates "no captions here" from "nobody
	// looked", which lead to opposite verdicts.
	Captions CaptionPresence `json:"captions,omitempty"`
	// Protection is the CENC scheme protecting this track's samples — cenc, cbcs,
	// cens or cbc1 — read from the sinf/schm box. Empty when the track is not
	// protected, or when it is and the packager stated no scheme.
	Protection string `json:"protection,omitempty"`
	// SamplesEncrypted marks a track whose *samples* are protected while its
	// container is not. This is the opposite shape of trouble from full-segment
	// AES-128: nothing fails, so the bitstream readers succeed and find nothing —
	// and "scanned, no captions" against a manifest declaring CC1 is a BAD on media
	// that is entirely correct. Every reader that looks inside a sample has to stay
	// out when this is set.
	SamplesEncrypted bool `json:"samples_encrypted,omitempty"`
	// Encrypted marks a track whose samples are protected (encv/enca sample
	// entry, or a TS payload flagged scrambled).
	Encrypted bool `json:"encrypted,omitempty"`
	// CCErrors counts breaks in the MPEG-TS continuity counter: packets the
	// transport lost between the packager and us. Always 0 for fMP4, which has
	// no equivalent packet-level counter.
	CCErrors int `json:"cc_errors,omitempty"`
	// OpensOnKeyframe reports whether the segment's first picture is a random
	// access point — an IDR for H.264, an IRAP for HEVC, a sync sample for fMP4.
	// Only meaningful when KeyframeKnown is true.
	OpensOnKeyframe bool `json:"opens_on_keyframe,omitempty"`
	// HasKeyframe reports that a random access point was positively found in the
	// segment's opening bytes, whether or not it was the very first picture.
	HasKeyframe bool `json:"has_keyframe,omitempty"`
	// KeyframeKnown is false when the segment says nothing about the matter: an
	// fMP4 fragment carrying no sample flags, or a TS segment whose video payload
	// could not be read. A check must stay quiet rather than call that a defect.
	KeyframeKnown bool `json:"keyframe_known,omitempty"`
	// KeyframeScanned records that the bitstream really was walked looking for a
	// random access point. It is what makes HasKeyframe == false mean "there is
	// none" rather than "nobody looked": an fMP4 fragment's sample flags describe
	// its first sample only, so they settle OpensOnKeyframe without settling
	// HasKeyframe.
	KeyframeScanned bool `json:"keyframe_scanned,omitempty"`
	// KeyframeStated records that the *container* said whether the first sample
	// is a random access point, rather than a bitstream walk having inferred it.
	// The two are not the same evidence. An fMP4 trun's first-sample flags are an
	// assertion with no room to argue; an MPEG-TS answer comes from walking the
	// stream in decode order, where with B-frames the first coded picture need
	// not be the first presented one and the reader may simply not have reached
	// the IDR. Reading them as one number turns Apple's own reference stream into
	// a conformance failure.
	KeyframeStated bool `json:"keyframe_stated,omitempty"`
}

// FrameRateFPS is the rate the pictures are actually shown at, in frames per
// second (SC-17).
//
// It comes from FrameDur, the *median* gap between presentation timestamps, and
// that choice is what makes it survive real content: with B-frames the stream is
// not in presentation order, so anything derived from consecutive decode-order
// timestamps is wrong, and a single discontinuity inside the segment would drag a
// mean off by however large the jump was.
//
// The second return follows this package's protocol. A rate of zero, or one
// derived from an unknown clock, would be compared against the manifest's
// FRAME-RATE and reported as a defect in media nobody managed to measure.
func (t Track) FrameRateFPS() (float64, bool) {
	// Audio has a sample rate, not a frame rate. Answering would invite a check
	// to compare it against a video rendition's declared FRAME-RATE.
	if t.Kind != Video || t.Timescale == 0 || t.FrameDur <= 0 || !t.HasPTS {
		return 0, false
	}
	fps := float64(t.Timescale) / float64(t.FrameDur)
	// A frame duration of a few ticks on a 90kHz clock is not a frame rate: it is
	// timestamps that failed to advance. Reporting the 90000fps that falls out of
	// a one-tick gap would have the framerate check compare it against the
	// manifest and call the rendition wrong.
	if fps > maxPlausibleFPS {
		return 0, false
	}
	return fps, true
}

// ContainsKeyframe reports whether a random access point was found anywhere in the
// segment's opening bytes, and whether the bitstream was walked at all.
//
// A segment with none cannot be switched into by any route, which is the severe
// case. One that merely does not *open* on a keyframe is a much weaker signal:
// Apple's own byte-range reference stream does that, because its segment
// boundaries fall on transport packets rather than on access units, and it plays
// everywhere.
func (t Track) ContainsKeyframe() (bool, bool) {
	if t.Kind != Video {
		return false, false
	}
	return t.HasKeyframe, t.KeyframeScanned
}

// StartsOnKeyframe reports whether the segment opens on a random access point,
// and whether that could be determined at all.
//
// A segment that does not open on one cannot be switched into: a decoder arriving
// there has no reference picture. It is the defect behind "ABR switching stutters
// even though the segment boundaries line up", and no manifest-level check can
// see it — the boundaries really are aligned.
//
// The second return follows this package's protocol: false means unmeasurable,
// and the caller must not read the first value.
func (t Track) StartsOnKeyframe() (bool, bool) {
	if t.Kind != Video {
		// Every audio frame is independently decodable, so the question does not
		// apply; answering it would invite a check to report on audio rungs.
		return false, false
	}
	return t.OpensOnKeyframe, t.KeyframeKnown
}

// OpensOnStatedKeyframe reports whether the container itself says the segment
// opens on a random access point, and whether it says anything at all.
//
// It is deliberately narrower than StartsOnKeyframe: a caller that will report
// a defect on the answer wants only the assertion, never the inference.
func (t Track) OpensOnStatedKeyframe() (opens, stated bool) {
	if t.Kind != Video || !t.KeyframeStated || !t.KeyframeKnown {
		return false, false
	}
	return t.OpensOnKeyframe, true
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
	ticks := t.MaxPTS - t.MinPTS + t.FrameDur
	if ticks <= 0 {
		// Timestamps that never advance measure nothing. Returning zero as though
		// it were a measurement makes the duration check report the media as 100%
		// shorter than declared, against a segment nobody managed to time.
		return 0, false
	}
	return float64(ticks) / float64(t.Timescale), true
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
	// DRMSystems are the DRM systems the initialisation segment advertises, one
	// per pssh box, in the order it lists them. It is a claim only the init can
	// make: a ladder whose manifest advertises PlayReady and whose CMAF init
	// carries only a Widevine pssh plays on Chrome and dies on Xbox and Edge,
	// and the manifest reads perfectly on the way down.
	DRMSystems []DRMSystem `json:"drm_systems,omitempty"`
	// Splices are the SCTE-35 splice information sections the segment carries, in
	// the order they appeared. They belong to the segment rather than to a track:
	// the signalling rides on a PID or in an emsg of its own, and it describes the
	// whole programme.
	Splices []SplicePoint `json:"splices,omitempty"`
}

// DRMSystem is one protection system a pssh box names.
type DRMSystem struct {
	// SystemID is the registered UUID, lower-case and hyphenated.
	SystemID string `json:"system_id"`
	// Name is the common name where segcheck knows one, and empty where it does
	// not. The list of DRM systems is not closed, and a guessed name is worse
	// than none: an operator argues about "PlayReady", and about an unrecognised
	// UUID they at least argue about the right sixteen bytes.
	Name string `json:"name,omitempty"`
}

// Label is how a finding names the system: the common name where there is one,
// the UUID where there is not.
func (d DRMSystem) Label() string {
	if d.Name != "" {
		return d.Name
	}
	return d.SystemID
}

// drmSystemNames maps the registered system UUIDs to the names people use.
var drmSystemNames = map[string]string{
	"edef8ba9-79d6-4ace-a3c8-27dcd51d21ed": "widevine",
	"9a04f079-9840-4286-ab92-e65be0885f95": "playready",
	"94ce86fb-07ff-4f43-adb8-93d2fa968ca2": "fairplay",
	"1077efec-c0b2-4d02-ace3-3c1e52e2fb4b": "cenc-common",
	"e2719d58-a985-b3c9-781a-b030af78d30e": "clearkey",
	"f239e769-efa3-4850-9c16-a903c6932efb": "adobe-primetime",
	"5e629af5-38da-4063-8977-97ffbd9902d4": "marlin",
}

// DRMSystemFor names a system UUID, or leaves it unnamed.
func DRMSystemFor(uuid string) DRMSystem {
	return DRMSystem{SystemID: uuid, Name: drmSystemNames[uuid]}
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
	case looksWebVTT(data):
		return ParseWebVTT(data)
	case looksTTML(data):
		return ParseTTML(data)
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
