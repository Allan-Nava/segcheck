// Package manifest parses HLS master/media playlists and DASH MPDs into one
// shared model, so the analyses downstream never have to know which of the two
// they are looking at.
//
// The model keeps what the manifest *declares* — durations, bandwidth,
// resolution, segment start times — precisely because segcheck's job is to
// compare those claims against the bytes the segments actually contain.
package manifest

import (
	"fmt"
	"net/url"
	"time"
)

// Kind of manifest.
const (
	KindHLS  = "hls"
	KindDASH = "dash"
)

// StreamKind is the media type of a rendition.
type StreamKind string

const (
	Video StreamKind = "video"
	Audio StreamKind = "audio"
	Text  StreamKind = "text"
)

// Rendition is one selectable version of the content: an HLS variant or
// EXT-X-MEDIA entry, a DASH Representation.
type Rendition struct {
	Name     string     `json:"name"`
	URI      string     `json:"uri,omitempty"`
	Kind     StreamKind `json:"kind"`
	Language string     `json:"language,omitempty"`
	GroupID  string     `json:"group_id,omitempty"`
	// Bandwidth is the declared peak bitrate in bits per second (HLS
	// BANDWIDTH, DASH Representation@bandwidth).
	Bandwidth int `json:"bandwidth,omitempty"`
	// AvgBandwidth is HLS AVERAGE-BANDWIDTH, 0 when absent.
	AvgBandwidth int     `json:"avg_bandwidth,omitempty"`
	Width        int     `json:"width,omitempty"`
	Height       int     `json:"height,omitempty"`
	Codecs       string  `json:"codecs,omitempty"`
	FrameRate    float64 `json:"frame_rate,omitempty"`
	// AudioGroup is the EXT-X-STREAM-INF AUDIO attribute, linking a video
	// variant to its audio group.
	AudioGroup string `json:"audio_group,omitempty"`
	// Segments is populated for DASH, where the MPD describes every
	// representation's segments inline. For HLS it stays empty until the
	// variant's media playlist is fetched.
	Segments []Segment `json:"segments,omitempty"`
	// InitURI is the initialisation segment (DASH SegmentTemplate@initialization).
	InitURI string `json:"init_uri,omitempty"`
	// InitRange and IndexRange are set for a DASH SegmentBase representation,
	// where the whole rendition is one file: the initialisation segment and the
	// `sidx` are byte ranges of it rather than separate resources.
	//
	// IndexRange is what makes such a rendition checkable at all. Nothing in the
	// MPD says where its subsegments begin — only the index does — so the
	// analysis has to fetch this range and read it before it can sample anything.
	InitRange  *ByteRange `json:"init_range,omitempty"`
	IndexRange *ByteRange `json:"index_range,omitempty"`
	// SingleFile marks a DASH representation that is one file with its index
	// inside it. With SegmentBase@indexRange the location is stated and
	// IndexRange is set; under the on-demand profile only a BaseURL is given and
	// the index has to be found by reading the head of the file. Both are one
	// file, so both are addressed by byte range.
	SingleFile bool `json:"single_file,omitempty"`
	// Unsupported explains why a rendition could not be expanded into segments
	// (DASH SegmentBase, for instance). Empty when it was.
	Unsupported string `json:"unsupported,omitempty"`
}

// Segment is one media segment as the manifest describes it.
type Segment struct {
	URI string `json:"uri"`
	// Duration is the declared duration in seconds (EXTINF, SegmentTimeline @d).
	Duration float64 `json:"duration"`
	// Discontinuity marks an intentional timeline break (EXT-X-DISCONTINUITY).
	// A PTS jump here is expected; one without it is a defect.
	Discontinuity bool `json:"discontinuity,omitempty"`
	// InitURI is the initialisation segment in force for this segment
	// (EXT-X-MAP), needed to parse fMP4 at all.
	InitURI string `json:"init_uri,omitempty"`
	// InitRange restricts the initialisation segment to a sub-range of its
	// resource (EXT-X-MAP BYTERANGE). Apple's own reference streams do this:
	// the init is the first few hundred bytes of the same file that holds every
	// segment, so ignoring it means downloading the entire asset as the "init".
	InitRange *ByteRange `json:"init_range,omitempty"`
	// ByteRange restricts the segment to part of its resource.
	ByteRange *ByteRange `json:"byte_range,omitempty"`
	// Sequence is the HLS media sequence number, or the DASH segment number.
	Sequence int `json:"sequence"`
	// PDT is the wall-clock mapping (EXT-X-PROGRAM-DATE-TIME).
	PDT    time.Time `json:"pdt,omitempty"`
	HasPDT bool      `json:"has_pdt,omitempty"`
	// DeclaredStart is the segment's start on the media timeline in seconds,
	// when the manifest states it outright (DASH SegmentTimeline @t). HLS does
	// not carry this, so HasDeclaredStart is false there.
	DeclaredStart    float64 `json:"declared_start,omitempty"`
	HasDeclaredStart bool    `json:"has_declared_start,omitempty"`
	// KeyMethod and KeyURI are the encryption in force (EXT-X-KEY / ContentProtection).
	KeyMethod string `json:"key_method,omitempty"`
	KeyURI    string `json:"key_uri,omitempty"`
}

// ByteRange is an HLS EXT-X-BYTERANGE.
type ByteRange struct {
	Length int64 `json:"length"`
	Offset int64 `json:"offset"`
}

// Header renders the range as an HTTP Range header value.
func (b ByteRange) Header() string {
	return fmt.Sprintf("bytes=%d-%d", b.Offset, b.Offset+b.Length-1)
}

// Playlist is a parsed manifest.
type Playlist struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
	// Master is true for an HLS master playlist and for every MPD (an MPD
	// always describes its representations).
	Master bool `json:"master"`
	// Live is true for a sliding-window HLS playlist (no EXT-X-ENDLIST) or a
	// dynamic MPD.
	Live           bool        `json:"live"`
	TargetDuration float64     `json:"target_duration,omitempty"`
	MediaSequence  int         `json:"media_sequence,omitempty"`
	Renditions     []Rendition `json:"renditions,omitempty"`
	Segments       []Segment   `json:"segments,omitempty"`
}

// VideoRenditions returns the renditions that carry video, in declared-bitrate
// order — the bitrate ladder.
func (p Playlist) VideoRenditions() []Rendition {
	var out []Rendition
	for _, r := range p.Renditions {
		if r.Kind == Video {
			out = append(out, r)
		}
	}
	return out
}

// Resolve turns a possibly relative manifest reference into an absolute URL.
func Resolve(base *url.URL, ref string) string {
	if base == nil {
		return ref
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return base.ResolveReference(u).String()
}

// Detect returns the manifest kind implied by a URL, content type and body.
func Detect(rawurl, contentType string, body []byte) string {
	if u, err := url.Parse(rawurl); err == nil {
		switch pathExt(u.Path) {
		case ".mpd":
			return KindDASH
		case ".m3u8", ".m3u":
			return KindHLS
		}
	}
	switch {
	case containsFold(contentType, "dash+xml"):
		return KindDASH
	case containsFold(contentType, "mpegurl"):
		return KindHLS
	}
	if containsFold(string(trimSpaceBytes(body, 512)), "<mpd") {
		return KindDASH
	}
	return KindHLS
}
