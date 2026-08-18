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
	"strings"
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
	// IFrame is a trick-play rung: EXT-X-I-FRAME-STREAM-INF, whose entries are
	// byte ranges holding one keyframe each rather than segments of media. It is
	// a kind of its own precisely so nothing that reads a segment as an extent
	// ever sees one — a single picture where a check expects two seconds of
	// media reports as a hole in every timeline check there is.
	IFrame StreamKind = "iframe"
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
	// VideoRange is the dynamic range the manifest claims: HLS VIDEO-RANGE, or
	// the name DASH's CICP transfer code implies. Empty means the manifest said
	// nothing, which is a different claim from SDR — a player defaults to SDR,
	// but a checker can only be wrong about what was actually stated.
	VideoRange string `json:"video_range,omitempty"`
	// Primaries, Transfer and Matrix are the CICP code points a DASH
	// AdaptationSet states in a Supplemental or Essential property. They are the
	// same numbers the bitstream carries, so no translation is needed to compare
	// them. Zero means unstated: 0 is reserved in all three registries.
	Primaries int `json:"primaries,omitempty"`
	Transfer  int `json:"transfer,omitempty"`
	Matrix    int `json:"matrix,omitempty"`
	// SampleRate and Channels are what the manifest claims about an audio
	// rendition: DASH @audioSamplingRate and AudioChannelConfiguration@value,
	// HLS EXT-X-MEDIA CHANNELS (which states no rate at all). Zero means the
	// manifest did not say, and an unstated claim is not a wrong one.
	SampleRate int `json:"sample_rate,omitempty"`
	Channels   int `json:"channels,omitempty"`
	// Captions are the closed captions the manifest claims this rendition's video
	// carries: HLS EXT-X-MEDIA TYPE=CLOSED-CAPTIONS entries in the group the
	// variant names, DASH Accessibility descriptors on the AdaptationSet. They are
	// claims about the bitstream, not renditions to fetch — there is nothing at
	// the other end of a CLOSED-CAPTIONS entry.
	Captions []Caption `json:"captions,omitempty"`
	// CaptionsNone records HLS CLOSED-CAPTIONS=NONE, which is a positive claim
	// that the video carries no captions. Absent the attribute entirely the
	// manifest says nothing either way, which is not the same claim.
	CaptionsNone bool `json:"captions_none,omitempty"`
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
	// KeyMethod is the protection declared for the whole rendition (DASH
	// ContentProtection). It exists because a single-file representation's segments do
	// not: they are synthesised from the index after the manifest is parsed, and
	// without this the protection had nowhere to come from — which produced
	// "encrypted but the manifest declares no key" on manifests that declare it.
	KeyMethod string `json:"key_method,omitempty"`
	// KeyScheme is the common encryption scheme the manifest declares — cenc,
	// cbcs, cens, cbc1 — from the DASH mp4protection ContentProtection's @value.
	// Empty means the manifest states none, which is not the same as cenc: cbcs
	// content served as cenc plays nowhere, and the two differ by a box field
	// rather than by anything visible, so MPDs get copied between them.
	KeyScheme string `json:"key_scheme,omitempty"`
	// DRMSystems are the systems the manifest promises can unlock this
	// rendition, as bare lower-case UUIDs so they compare directly with what a
	// pssh box carries: DASH urn:uuid ContentProtection entries, HLS KEYFORMAT.
	DRMSystems []string `json:"drm_systems,omitempty"`
	// DRMInManifest are the systems whose key-acquisition data the manifest
	// carries itself, in a DASH cenc:pssh element. DASH-IF's own guidance puts it
	// there and every real multi-DRM vector does, so an initialisation segment
	// legitimately carries no pssh at all — and a check that demanded one
	// reported Axinom's reference stream as missing both its systems.
	DRMInManifest []string `json:"drm_in_manifest,omitempty"`
	// NextSegment is the segment immediately past a dynamic MPD's live edge: the
	// one @availabilityStartTime arithmetic says does not exist yet. It is kept
	// out of Segments deliberately — sampling it would report a 404 the MPD
	// itself predicted — and exists so the availability claim can be probed in
	// the direction a listed segment cannot answer. If the origin serves it, the
	// packager is ahead of the clock the MPD is computed against.
	NextSegment *Segment `json:"next_segment,omitempty"`
	// OldestSegment is the oldest segment @timeShiftBufferDepth claims is still
	// there. Like NextSegment it is kept out of Segments — the expansion keeps
	// the tail, where the live edge is — and exists so the DVR promise can be
	// collected on purpose rather than only by a viewer scrubbing back, which is
	// to say in a complaint rather than in monitoring.
	OldestSegment *Segment `json:"oldest_segment,omitempty"`
	// PeriodID, PeriodIndex, PeriodStart and PeriodDuration place a DASH
	// rendition in its Period. A multi-period MPD is several presentations
	// spliced into one, and the splice is exactly where an encoder change lands —
	// a new period is how an ad break, a programme junction or a re-encode is
	// expressed. Each period restarts its own media timeline, so nothing about a
	// segment says which period it came from and a cross-period comparison
	// without this is between two things that were never on the same clock.
	//
	// PeriodIndex is 0 and the rest are zero for a single-period MPD and for HLS,
	// which is what every check that predates this already assumes.
	PeriodID       string  `json:"period_id,omitempty"`
	PeriodIndex    int     `json:"period_index,omitempty"`
	PeriodStart    float64 `json:"period_start,omitempty"`
	PeriodDuration float64 `json:"period_duration,omitempty"`
	// PresentationTimeOffset is what a segment's own timeline has to have
	// subtracted from it to land on the presentation timeline, in seconds. A
	// packager that forgets it after a period restart puts every segment of that
	// period a whole period-start away from where a seek expects it.
	PresentationTimeOffset float64 `json:"presentation_time_offset,omitempty"`
	// Unsupported explains why a rendition could not be expanded into segments
	// (DASH SegmentBase, for instance). Empty when it was.
	Unsupported string `json:"unsupported,omitempty"`
}

// Caption is one closed-caption service the manifest claims is in the video.
type Caption struct {
	// InstreamID names where in the bitstream it is: "CC1".."CC4" for the CEA-608
	// channels, "SERVICE1".."SERVICE63" for the CEA-708 services. It is the HLS
	// attribute name, and DASH Accessibility values are normalised onto it so the
	// check has one vocabulary to compare against.
	InstreamID string `json:"instream_id"`
	Language   string `json:"language,omitempty"`
	Name       string `json:"name,omitempty"`
	GroupID    string `json:"group_id,omitempty"`
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
	// PDTDerived is true when this segment carried no EXT-X-PROGRAM-DATE-TIME of
	// its own and its wall clock was computed by adding the declared durations
	// forward from the segment that did. That is exactly what a player does — a
	// playlist states the tag once and leaves the rest to arithmetic — so the
	// derived value is a real claim and not an invention. It is marked because a
	// derived time is only as good as the durations it was summed over.
	PDTDerived bool `json:"pdt_derived,omitempty"`
	// DeclaredStart is the segment's start on the media timeline in seconds,
	// when the manifest states it outright (DASH SegmentTimeline @t). HLS does
	// not carry this, so HasDeclaredStart is false there.
	DeclaredStart    float64 `json:"declared_start,omitempty"`
	HasDeclaredStart bool    `json:"has_declared_start,omitempty"`
	// PeriodOffset is how far into its DASH Period the segment begins, on the
	// presentation timeline, in seconds. It is what the manifest's own
	// arithmetic says — the index times @duration, or @t less
	// @presentationTimeOffset — and it is the value the segment's own timestamp
	// has to agree with once that offset is subtracted. HLS has no periods and
	// HasPeriodOffset is false there, as it is wherever the MPD describes its
	// segments in a way that states no position at all.
	PeriodOffset    float64 `json:"period_offset,omitempty"`
	HasPeriodOffset bool    `json:"has_period_offset,omitempty"`
	// KeyMethod and KeyURI are the encryption in force (EXT-X-KEY / ContentProtection).
	KeyMethod string `json:"key_method,omitempty"`
	KeyURI    string `json:"key_uri,omitempty"`
	// KeyIV is the initialisation vector EXT-X-KEY states, when it states one.
	// When it does not, the IV is the segment's media sequence number as a 128-bit
	// big-endian value — so the absence of this field is a specific instruction,
	// not a missing value to default to zeroes.
	KeyIV []byte `json:"key_iv,omitempty"`
	// KeyFormat is EXT-X-KEYFORMAT. Its absence means "identity": the key URI
	// resolves to sixteen raw bytes. Anything else is a DRM system whose key this
	// tool cannot fetch.
	KeyFormat string `json:"key_format,omitempty"`
	// Parts are the EXT-X-PART entries this segment was published as, in order.
	// Empty for everything but a low-latency playlist, and empty there too once
	// the packager has aged them out.
	Parts []Part `json:"parts,omitempty"`
}

// Part is one EXT-X-PART: a fraction of a segment, published before the segment
// itself exists. Low-latency HLS is built out of them, and they are a second,
// finer description of media the playlist already describes as segments — which
// is exactly why they are worth reading. Two descriptions of the same media can
// disagree.
type Part struct {
	URI      string  `json:"uri"`
	Duration float64 `json:"duration"`
	// ByteRange restricts the part to a range of its resource, which is how most
	// packagers publish parts: every part of a segment is a range of the same
	// growing file.
	ByteRange *ByteRange `json:"byte_range,omitempty"`
	// Independent is INDEPENDENT=YES: the part is claimed to begin with an
	// independent frame, so a player may start playing at it. It is the one claim
	// a part makes about the bitstream, and the media can arbitrate it.
	Independent bool `json:"independent,omitempty"`
	// Gap is GAP=YES: the packager is declaring this part deliberately absent.
	// Fetching it is pointless and reporting it missing is reporting the manifest
	// back at itself.
	Gap bool `json:"gap,omitempty"`
	// Sequence is the media sequence number of the segment this part belongs to,
	// and Index its position within that segment, both so a finding can name it:
	// a part has no number of its own.
	Sequence int `json:"sequence"`
	Index    int `json:"index"`
}

// PreloadHint is EXT-X-PRELOAD-HINT: a resource the server promises is coming
// and will hold a request open for. segcheck does not fetch it — a preload hint
// is designed to block until the media exists, and a checker that blocked on one
// would hang rather than report — but a playlist that hints at something it has
// already published is making a player fetch the same bytes twice.
type PreloadHint struct {
	Type            string `json:"type"` // PART or MAP
	URI             string `json:"uri"`
	ByteRangeStart  int64  `json:"byte_range_start,omitempty"`
	ByteRangeLength int64  `json:"byte_range_length,omitempty"`
}

// UTCTiming is one time source a dynamic MPD names for a client to synchronise
// against before doing any availability arithmetic.
type UTCTiming struct {
	// Scheme is the schemeIdUri: urn:mpeg:dash:utc:http-head:2014,
	// http-iso:2014, http-xsdate:2014, direct:2014, ntp:2014 and so on. Which
	// one it is decides how Value is read, and segcheck can honour the HTTP ones
	// and the direct one — an NTP source is a protocol this tool does not speak,
	// and saying so is better than silently falling back to the local clock the
	// element exists to distrust.
	Scheme string `json:"scheme"`
	Value  string `json:"value,omitempty"`
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
	Live           bool    `json:"live"`
	TargetDuration float64 `json:"target_duration,omitempty"`
	// PartTarget is EXT-X-PART-INF PART-TARGET: the longest any EXT-X-PART in
	// this playlist may be. Zero when the playlist publishes no parts.
	PartTarget float64 `json:"part_target,omitempty"`
	// PartHoldBack and CanBlockReload are EXT-X-SERVER-CONTROL: how far back
	// from the live edge a low-latency player should stay, and whether the
	// server will hold a request open for media that does not exist yet.
	PartHoldBack   float64 `json:"part_hold_back,omitempty"`
	CanBlockReload bool    `json:"can_block_reload,omitempty"`
	// PendingParts are the parts of the segment currently being published: the
	// ones with no segment URI line after them yet. They are kept apart from
	// Segments because attaching them to the last complete segment would count
	// that segment's media twice.
	PendingParts []Part `json:"pending_parts,omitempty"`
	// PreloadHint is EXT-X-PRELOAD-HINT, when the playlist carries one.
	PreloadHint *PreloadHint `json:"preload_hint,omitempty"`
	// AvailabilityStart is DASH @availabilityStartTime: the wall clock the whole
	// presentation timeline is measured from, and therefore the only thing that
	// says which segment exists right now. HLS lists what exists instead, so it
	// makes no such claim.
	AvailabilityStart    time.Time `json:"availability_start,omitempty"`
	HasAvailabilityStart bool      `json:"has_availability_start,omitempty"`
	// UTCTiming are the time sources the MPD names, in the order it lists them —
	// its own fallback order. They exist because the client's clock is not to be
	// trusted for the availability arithmetic, which is exactly why a checker
	// that ignores them is measuring its own clock rather than the stream.
	UTCTiming []UTCTiming `json:"utc_timing,omitempty"`
	// TimeShiftBufferDepth is @timeShiftBufferDepth in seconds: how far back the
	// MPD claims a viewer can still seek. PresentationDelay is
	// @suggestedPresentationDelay, how far back from the edge it suggests they
	// start. Zero means the MPD states neither.
	TimeShiftBufferDepth float64 `json:"time_shift_buffer_depth,omitempty"`
	PresentationDelay    float64 `json:"presentation_delay,omitempty"`
	// UpdatePeriod is how often the manifest itself says it will change: DASH
	// @minimumUpdatePeriod. HLS states no such interval — a player re-reads a
	// live media playlist at TARGETDURATION — so it stays zero there.
	UpdatePeriod  float64     `json:"update_period,omitempty"`
	MediaSequence int         `json:"media_sequence,omitempty"`
	Renditions    []Rendition `json:"renditions,omitempty"`
	Segments      []Segment   `json:"segments,omitempty"`
	// AdBreaks are the SCTE-35 ad-break signals the manifest declares: HLS
	// EXT-X-DATERANGE and EXT-X-CUE-OUT/CUE-IN in a media playlist, DASH
	// EventStream events at Period level.
	AdBreaks []AdBreak `json:"ad_breaks,omitempty"`
}

// AdBreak is one ad-break signal the manifest declares.
//
// Three different ways of saying when, because three different tags say it
// differently, and converting between them needs information the manifest may not
// carry. A check that collapsed them into one number would have to invent the
// missing conversion.
type AdBreak struct {
	// ID pairs a break's start with its end (EXT-X-DATERANGE ID, Event@id).
	ID string `json:"id,omitempty"`
	// OutOfNetwork is true for the start of a break, false for the return.
	OutOfNetwork bool `json:"out_of_network"`
	// Start is the wall-clock time of an EXT-X-DATERANGE. Placing it on the media
	// timeline needs EXT-X-PROGRAM-DATE-TIME, which a playlist need not carry.
	Start    time.Time `json:"start,omitempty"`
	HasStart bool      `json:"has_start,omitempty"`
	// MediaTime is the break's position on the media timeline in seconds, which
	// DASH states outright (Event@presentationTime over the stream's timescale).
	MediaTime    float64 `json:"media_time,omitempty"`
	HasMediaTime bool    `json:"has_media_time,omitempty"`
	// Sequence is the media sequence number of the segment an EXT-X-CUE-OUT or
	// EXT-X-CUE-IN precedes. Such a break is on a segment boundary by
	// construction, which is exactly what makes it worth distinguishing.
	Sequence    int  `json:"sequence,omitempty"`
	HasSequence bool `json:"has_sequence,omitempty"`
	// Duration and PlannedDuration are the break's length in seconds, 0 when the
	// signal does not state one.
	Duration        float64 `json:"duration,omitempty"`
	PlannedDuration float64 `json:"planned_duration,omitempty"`
	// Tag names which signal this came from, so a finding can quote what the
	// operator actually wrote.
	Tag string `json:"tag,omitempty"`
	// Section is the splice_info_section the manifest carries as hexadecimal in
	// SCTE35-OUT/IN/CMD, decoded. It is what makes the manifest's own account of the
	// break comparable with the media's rather than only their timings — a packager
	// that rewrote one and not the other is what that catches. Nil when the attribute
	// held something that is not a section.
	Section []byte `json:"section,omitempty"`
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

// The registered DRM system UUIDs, and the HLS KEYFORMAT spellings that name
// the same systems. Comparing what a manifest promises with what a pssh box
// carries needs both on one vocabulary, and HLS and DASH spell it differently.
const (
	widevineUUID  = "edef8ba9-79d6-4ace-a3c8-27dcd51d21ed"
	playReadyUUID = "9a04f079-9840-4286-ab92-e65be0885f95"
	fairPlayUUID  = "94ce86fb-07ff-4f43-adb8-93d2fa968ca2"
)

var keyFormatSystems = map[string]string{
	"com.apple.streamingkeydelivery": fairPlayUUID,
	"com.microsoft.playready":        playReadyUUID,
	"com.widevine.alpha":             widevineUUID,
}

// DRMSystemForKeyFormat is the system UUID an HLS KEYFORMAT names, or the empty
// string for one that names no DRM system at all.
//
// An absent KEYFORMAT means "identity": the URI resolves to sixteen raw bytes
// and there is no system to compare against. Returning a UUID for that would
// invent a claim the manifest never made.
func DRMSystemForKeyFormat(keyformat string) string {
	f := strings.ToLower(strings.TrimSpace(keyformat))
	if f == "" || f == "identity" {
		return ""
	}
	if strings.HasPrefix(f, "urn:uuid:") {
		return strings.TrimPrefix(f, "urn:uuid:")
	}
	return keyFormatSystems[f]
}

// Dynamic-range names, as HLS VIDEO-RANGE spells them.
const (
	RangeSDR = "SDR"
	RangeHLG = "HLG"
	RangePQ  = "PQ"
)

// VideoRangeForTransfer is the name a CICP transfer characteristic implies: 16
// is PQ, 18 is HLG, and everything else assigned is one flavour of SDR.
//
// The asymmetry is deliberate. PQ and HLG are two specific code points, so
// naming them is exact; "SDR" covers BT.709, BT.601, both BT.2020 curves, sRGB
// and the gamma variants, and pinning it to one of them would invent a
// distinction the manifest never draws.
func VideoRangeForTransfer(transfer int) string {
	switch transfer {
	case 0, 2, 3:
		return "" // reserved or unspecified: no claim
	case 16:
		return RangePQ
	case 18:
		return RangeHLG
	}
	return RangeSDR
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
