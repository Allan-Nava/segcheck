# Backlog — segcheck

Single source of truth for what is planned. Items keep a stable `SC-n` id so
commits, the CHANGELOG and issues can reference them. New ideas go here rather
than into scattered TODO comments.

[ROADMAP.md](ROADMAP.md) is a **generated** view of this file, grouped by
milestone. Do not edit it by hand — run `scripts/backlog.sh roadmap` after
touching this file, or CI will fail.

## How to write an item

```
## M3 — Title of the milestone <!-- ms: target=v0.2.0 phase=now -->

- [ ] **SC-15 — Short name**: what it is, why it earns its place, what it
  needs to touch. <!-- sc: prio=high size=L labels=parser,video -->
```

- The **id never changes**. Adding an item means taking the next free number,
  never reusing a retired one. Moving an item to a different milestone is fine;
  renumbering it is not.
- `- [ ]` is open, `- [x]` is shipped, and a shipped item carries the release it
  went out in: `ver=0.1.0`.
- Metadata lives in a trailing `<!-- sc: ... -->` comment (invisible when
  rendered, trivially parseable). Keys: `prio` (`high|med|low`), `size`
  (`S|M|L|XL`), `labels` (comma-separated, from the vocabulary below), `ver`
  (shipped items only).
- Milestone metadata is a trailing `<!-- ms: ... -->` on the heading. Keys:
  `target` (the release it aims at, or `ongoing`) and `phase`
  (`shipped|now|next|later|ongoing`).
- Labels: `parser`, `check`, `output`, `cli`, `delivery`, `integration`,
  `tests`, `docs`, `release`, `project`.

`scripts/backlog.sh lint` enforces all of the above; `scripts/backlog.sh next`
prints what to pick up.

## M1 — Core: read the segments <!-- ms: target=v0.1.0 phase=shipped -->

- [x] **SC-1 — MPEG-TS parser**: PAT/PMT, PES presentation timestamps,
  continuity-counter breaks, scrambling flag, PSI reassembly across packets,
  resync after lost sync. Zero-dep, in-tree.
  <!-- sc: prio=high size=XL labels=parser ver=0.1.0 -->
- [x] **SC-2 — fMP4/CMAF parser**: `moov` (timescale, handler, codec, coded size
  from the sample entry), `moof`/`mfhd`/`tfhd`/`tfdt`/`trun` for the timeline,
  `encv`/`enca`/`pssh` for protection.
  <!-- sc: prio=high size=XL labels=parser ver=0.1.0 -->
- [x] **SC-3 — H.264 SPS**: coded resolution out of the bitstream, with
  Exp-Golomb, emulation-prevention unescaping, scaling lists and the
  frame-cropping arithmetic. This is what makes "declares 1080p, codes 720p"
  detectable in MPEG-TS. <!-- sc: prio=high size=L labels=parser ver=0.1.0 -->
- [x] **SC-4 — HLS parser**: master and media playlists, attribute lists with
  quoted commas, `EXT-X-MAP` (including `BYTERANGE`), `EXT-X-BYTERANGE` with
  implicit offsets, `EXT-X-KEY`, `EXT-X-DISCONTINUITY`,
  `EXT-X-PROGRAM-DATE-TIME`, audio-only variant classification.
  <!-- sc: prio=high size=L labels=parser ver=0.1.0 -->
- [x] **SC-5 — DASH parser**: `SegmentTemplate` with `$Number$`/`$Time$`/`%0Nd`,
  `SegmentTimeline` with `@r`, `SegmentList`, `BaseURL` chains, `xs:duration`,
  live-edge derivation from `availabilityStartTime`.
  <!-- sc: prio=high size=L labels=parser ver=0.1.0 -->
- [x] **SC-6 — Packed audio**: ADTS AAC frame counting plus the ID3
  `com.apple.streaming.transportStreamTimestamp` PRIV tag, which is the only
  timeline an audio-only rendition has.
  <!-- sc: prio=med size=M labels=parser ver=0.1.0 -->

## M2 — The checks <!-- ms: target=v0.1.0 phase=shipped -->

- [x] **SC-7 — `continuity`**: undeclared gaps and overlaps between consecutive
  segments, PTS wraparound handled, declared discontinuities honoured; MPEG-TS
  packet loss. <!-- sc: prio=high size=L labels=check ver=0.1.0 -->
- [x] **SC-8 — `duration`**: declared against real, per segment and accumulated,
  plus `TARGETDURATION` compliance.
  <!-- sc: prio=high size=M labels=check ver=0.1.0 -->
- [x] **SC-9 — `resolution`**: coded resolution against declared `RESOLUTION`.
  <!-- sc: prio=high size=M labels=check ver=0.1.0 -->
- [x] **SC-10 — `bitrate`**: measured peak and average against `BANDWIDTH`, both
  under- and over-declaration.
  <!-- sc: prio=high size=M labels=check ver=0.1.0 -->
- [x] **SC-11 — `alignment`**: segment boundaries across renditions on a shared
  timeline. <!-- sc: prio=high size=M labels=check ver=0.1.0 -->
- [x] **SC-12 — `timeline`**: DASH `SegmentTimeline` `@t` against the fragment
  `tfdt`. <!-- sc: prio=med size=M labels=check ver=0.1.0 -->
- [x] **SC-13 — `tracks` / `container` / `encryption` / `ladder` / `init`**:
  track presence and stability, codec agreement, container sanity,
  declared-versus-observed protection, ladder shape.
  <!-- sc: prio=high size=L labels=check ver=0.1.0 -->
- [x] **SC-14 — Output**: terminal (worst first, colour on a TTY only), JSON,
  ops-style markdown; `--exit-on`.
  <!-- sc: prio=high size=M labels=output ver=0.1.0 -->

## M3 — Codec and timing depth <!-- ms: target=v0.2.0 phase=shipped -->

The rungs segcheck currently reads least well. Every item here removes a silent
skip — a place where the tool says nothing because it cannot look, not because
the stream is healthy.

- [x] **SC-15 — HEVC/H.265 SPS**: coded resolution for HEVC rungs, which used to
  report a codec and no resolution so the `resolution` check skipped them in
  silence — a silence indistinguishable from a pass. `internal/media/hevc.go`
  reads the parameter set out of an MPEG-TS elementary stream: two-byte NAL
  header, `profile_tier_level` measured exactly (its length depends on the
  sub-layer count, and misreading it returns a plausible wrong number rather
  than failing), then the conformance window applied with the SubWidthC /
  SubHeightC unit for the chroma format. `tsStream.track()` dispatches on stream
  type rather than trying both readers, because an HEVC stream read as H.264 can
  find something SPS-shaped. fMP4 needed no reader — the visual sample entry
  already states it — but `hvc1` now has a test of its own rather than an
  assumption. Both the sub-layer tail and the end-to-end check were
  mutation-verified. <!-- sc: prio=high size=L labels=parser ver=0.2.0 -->
- [x] **SC-79 — Encrypted fMP4 reported the wrong codec and no resolution**:
  found while closing SC-78's coverage gap, and reachable on any CMAF stream with
  `cenc`/`cbcs` protection. `parseStsd` looked for `sinf`/`frma` — where an
  encrypted sample entry preserves the format the encryption replaced — from byte
  0 of the sample entry payload. Those first bytes are not a box: a
  VisualSampleEntry opens with 78 bytes of fixed fields (28 for audio), whose
  leading reserved zeros `boxesIn` reads as a box of declared size 0 that swallows
  the entry whole, so the search never found anything. The codec was therefore
  reported as `encv`/`enca`, and `checkTracks` compared the manifest's declared
  `avc1` against it and emitted a **codec-mismatch WARN on every encrypted
  rendition** — a defect reported against media that was entirely correct. The
  resolution was lost the same way from the other side: `encv` is not in the
  visual-sample-entry list, so the frame size was never read and `resolution`
  skipped the rung in silence. The search now starts after the fixed fields, and
  both the video and audio cases are asserted against sample entries built to the
  real layout. <!-- sc: prio=high size=S labels=parser,check ver=0.2.0 -->
- [x] **SC-16 — Keyframe alignment**: the `keyframe` check reports segments a
  player cannot switch into, which is the defect behind "ABR switching stutters
  even though the boundaries line up" — `alignment` passes, every duration is
  right, and every switch still breaks. Read three ways, since the containers state
  it in three places: the first coded slice's `nal_unit_type` for H.264, the whole
  IRAP range 16–21 for HEVC (recognising only `IDR_W_RADL` would call a switchable
  `CRA_NUT`-opening segment broken, and CRA is what some live encoders emit for
  every segment), and `sample_is_non_sync_sample` for fMP4 from trun's
  first-sample-flags, else its per-sample flags, else the tfhd default. The walk
  skips parameter sets, AUDs and SEI to stop at the first NAL carrying picture data;
  only the first `traf` decides.
  **The severity model was corrected by the reference streams, not by reasoning.**
  A first draft made "does not open on a keyframe" a BAD, and Apple's own bipbop
  reported three of them — its segments are byte ranges of one `main.ts`, so a
  range boundary falls on a transport packet and a segment can carry the tail of
  the previous picture before its own IDR. Players start at the IDR; the stream
  plays everywhere. So the BAD is now reserved for a segment carrying **no** random
  access point at all, and "carries one, just not first" is an OK-level note with a
  count. A second real defect surfaced the same way: `annexBNALUs` stops after 64
  units, and a 1080p picture split across dozens of slices pushes the following IDR
  past that — so bipbop's larger rungs read as having no keyframe at all. The
  keyframe walk now has its own generous cap and reports whether the cap, rather
  than the data, stopped it; hitting it, or hitting the 1 MiB elementary-stream
  capture cap, means absence was never established. Four facts, not one:
  opens / present / known / scanned, because "there is none" and "nobody looked far
  enough" must not be the same answer. Mutation-verified (ten mutations, ten
  caught), and two of the tests were themselves rewritten when that pass showed
  them passing for the wrong reason. Verified against Apple fMP4, Apple MPEG-TS and
  a public DASH manifest: zero findings above OK.
  <!-- sc: prio=high size=L labels=check,parser ver=0.2.0 -->
- [x] **SC-17 — Frame rate**: the `framerate` check measures the rate from the
  median gap between presentation timestamps — the one measure that survives
  B-frames, since the stream is not in presentation order and a mean would also be
  dragged off by any discontinuity inside the segment — and asks two things of it.
  Against the manifest: `FRAME-RATE` / `@frameRate` is what a player consults to
  decide what it can decode *before* downloading anything, so a 1080p60 rung
  declared as 30 gets chosen by a device that can only manage 30 and then stutters,
  while the manifest reads perfectly on the way down. Across the ladder: rungs at
  unrelated rates make every switch visibly uneven. The deliberate exception is an
  exact integer relation — halving the rate on the lower rungs is an ordinary way
  to save bitrate — so a ladder of 60/30/15 is left alone and one mixing 25 and 30
  is not. The 2% tolerance exists to absorb the NTSC rates: a manifest writes 29.97
  where the media runs at 30000/1001, and flagging that would fire on a large
  fraction of the world's content — Apple's own reference stream declares 23.976
  against a measured 23.98. The manifest side needed no work: `Rendition.FrameRate`
  already carried both `FRAME-RATE` and `@frameRate`. Mutation-verified (nine
  mutations, nine caught) and checked against Apple fMP4, Apple MPEG-TS and a
  public DASH manifest with zero findings above OK.
  <!-- sc: prio=high size=M labels=check ver=0.2.0 -->
- [x] **SC-19 — `sidx` and `SegmentBase`**: single-file DASH representations are
  sampled now instead of being reported unsupported — an honest answer that
  skipped every other check for the whole rendition. Three layers, because
  `ParseDASH` does no I/O: the manifest says which bytes hold the index, the media
  package reads it, and the analysis fetches. Two shapes had to be handled, and
  only the second is common in the wild. `SegmentBase@indexRange` states where the
  index is; the **on-demand profile states nothing but a `BaseURL`**, so the index
  is found by reading the head of the file — which is the shape Sony's DASH-IF
  vector uses, and the first implementation reported it as having no segment
  description at all.
  Two more things came from that stream rather than from the spec. Its index is
  **hierarchical**: a root `sidx` whose every reference points at a leaf `sidx`, so
  a reader that stops at the first level finds no media references and concludes
  the file describes nothing — `ResolveSIDX` follows the tree to a bounded depth.
  And its fragments state no sample durations at all, relying on `mvex`/`trex` in
  the init segment (SC-87), without which every duration read as zero. Once both
  were right the stream went from 3 ERROR and 0 segments to **25 checks, 25 OK**.
  The offsets are the part to get right: a reference states a size, not a position,
  so they accumulate from the end of the index box plus `first_offset`, and the
  index's own position in the file has to be added because `@indexRange` addresses
  it there. <!-- sc: prio=high size=M labels=parser ver=0.2.0 -->
- [x] **SC-87 — `trex` defaults were never read**: `mvex`/`trex` in the
  initialisation segment states the default sample duration, size and flags for a
  track, and a fragment may state none of them itself. A large share of real
  on-demand DASH is packaged that way — Sony's DASH-IF vector carries
  `default_sample_duration=1001` in `trex` and nothing in its fragments — and
  ignoring it made every sample zero ticks long. That did not fail loudly: the
  segment's stated duration became zero, so `duration` reported the media as 100%
  shorter than declared and `continuity` reported a gap before every segment,
  against a stream that is entirely correct. The defaults are now read per track
  and used as the floor, with the tfhd overriding them and a trun overriding that.
  Found while closing SC-19 but not specific to it: it affects any CMAF stream
  packaged this way. `DurationSec` also stopped reporting a computed zero as a
  measurement — timestamps that never advance measure nothing, and saying
  otherwise is the same false report by another route.
  <!-- sc: prio=high size=S labels=parser,check ver=0.2.0 -->
- [x] **SC-42 — AV1 and VP9 coded resolution**: the item assumed a parser was
  missing. Checked before writing one, and it was not: an `av01`, `vp09` or `vp08`
  visual sample entry already reported both codec and resolution end to end,
  because the resolution of every fMP4 codec comes from the sample entry rather
  than from a bitstream reader — `av1C` and `vpcC` carry profile and level, not a
  frame size. So the whole of it rested on the sample entry type being in
  `isVisualSampleEntry`, which is exactly the kind of thing that works until
  someone edits a list, and nothing asserted it.
  What shipped is therefore the test rather than the parser, on the same reasoning
  that gave `hvc1` one in SC-15: a codec missing from that list reports no
  resolution, `resolution` has nothing to compare, and the rung is skipped in a
  silence indistinguishable from a pass. The list is now stated as a contract —
  every codec the tool names must be recognised as visual and must have a codec
  name — and mutation-verified: dropping `av01` or `vp09` from it, or from the
  codec table, is caught.
  The OBU sequence header the item also asked for buys nothing here, because an
  `av01` entry is required to carry width and height; reading `av1C`/`vpcC` for
  **profile and level** is a real gap, and it belongs with SC-74 in M12 where the
  codec string is already the subject. MPEG-TS AV1 is deliberately out: there is no
  deployed stream type for it.
  <!-- sc: prio=med size=L labels=parser ver=0.2.0 -->
- [x] **SC-35 — Parser fuzzing**: six `go test -fuzz` targets — TS, MP4, SIDX,
  packed audio, the H.264/HEVC parameter sets, and `Parse` itself for the
  container detection in front of them. The seed corpus is **built from
  `mediatest` rather than checked in**, because no binary fixture enters this
  repository; the builders already produce a well-formed segment of each kind, and
  mutating those is where a fuzzer should start. `go test` runs the seeds on every
  build, so the targets double as a regression suite without anyone opting in.
  The property asserted is not only "does not panic" but "when it claims success,
  what it returns is self-consistent" — a parser that survives by reporting a
  60000x12000 frame has not survived, it has moved the failure downstream into a
  finding about media that never said any such thing. That second property is what
  found all three defects below; a panic-only target would have passed.
  A crash writes its input under `testdata/fuzz`, which is gitignored: the fix is
  to turn it into an explicit test with the bytes written out in code, which is how
  all three were handled. CI fuzzes each target for 60s.
  <!-- sc: prio=high size=M labels=tests,parser ver=0.2.0 -->
- [x] **SC-88 — Three defects the fuzzer found**: all of the same shape — a parser
  answering confidently instead of failing. **`sidx` version**: anything other than
  0 was read as version 1, so a version byte of `0x30` had the time fields read at
  the wrong width, turning a run of `0xff` into a `first_offset` of nearly 2^64 and
  every subsegment offset negative — a byte-range request starting before the file
  does. Versions other than 0 and 1 are rejected now, and the offset arithmetic is
  guarded against overflow. **Frame rate**: timestamps advancing by one tick on a
  90kHz clock yielded 90000fps, which `framerate` would have compared against the
  manifest and called the rendition wrong; a rate past 1000fps is arithmetic on
  timestamps that did not advance, not a measurement. **Resolution**: the bitstream
  readers had always refused an implausible frame size, but the container readers
  had not, so a malformed `tkhd` or sample entry could report 16688x12336 and have
  `resolution` report a mismatch against a manifest that says nothing of the kind.
  One rule for both now, and unknown beats wrong.
  <!-- sc: prio=high size=S labels=parser ver=0.2.0 -->

## M4 — Everything that is not the video track <!-- ms: target=v0.3.0 phase=shipped -->

Audio, captions, subtitles, ad signalling and protected content — the parts of a
stream that break in production and that no manifest-only checker can see.

- [x] **SC-18 — Audio sanity**: sample rate and channel count read from where each
  container states them — the `AudioSampleEntry` in fMP4, the `dac3`/`dec3` box for
  AC-3 and E-AC-3, the ADTS header in MPEG-TS and packed audio — then compared
  against HLS `CHANNELS`, DASH `@audioSamplingRate` and `AudioChannelConfiguration`,
  and against the rendition itself across segments. Silence detection is explicitly
  out of scope (that needs decoding).
  <!-- sc: prio=high size=M labels=check ver=0.3.0 -->
- [x] **SC-89 — DASH kind from the Representation**: an `AdaptationSet` may state
  no `mimeType` or `contentType` and leave it to its Representations, which
  `dash.akamaized.net/dash264/TestCasesHD/2b/qualcomm/1/MultiResMPEG2.mpd` does.
  Every rung of it was then classified as audio, so the `ladder` check reported a
  BAD "no video rendition in the manifest" on a perfectly good stream and
  `resolution`, `framerate` and `keyframe` went silent on all of it. `dashKind` now
  falls back to the first Representation's `mimeType`, `codecs` and frame size, and
  the stream is a smoke-suite baseline.
  <!-- sc: prio=high size=S labels=parser ver=0.3.0 -->
- [x] **SC-90 — Audio codec against `CODECS`**: the `audio` check compares the rate
  and the layout, and now the codec too — an `mp4a` track declared `ec-3` is
  silence on a device with no E-AC-3 decoder that would have played the AAC. A
  `CODECS` value naming two audio codecs, or none, states nothing to compare.
  <!-- sc: prio=med size=S labels=check ver=0.3.0 -->

- [x] **SC-20 — SCTE-35 / `EXT-X-DATERANGE`**: ad-break signalling read where it
  really lives — a `splice_info_section` on an MPEG-TS PID of stream type 0x86, or a
  DASH `emsg` — and compared against `EXT-X-DATERANGE`, `EXT-X-CUE-OUT`/`CUE-IN` and
  DASH `EventStream`. The question is not whether the break is signalled but whether
  a player can cut to it: a splice that does not land on a segment boundary is a
  break nobody can take.
  <!-- sc: prio=high size=L labels=check,parser ver=0.3.0 -->
- [x] **SC-92 — Splice descriptors and `EXT-X-DATERANGE` payloads**: the
  `segmentation_descriptor` is read for the segmentation type and the UPID, so a finding
  says "Provider Placement Opportunity Start" rather than "time_signal", and the
  hexadecimal section in `SCTE35-OUT`/`IN`/`CMD` is decoded so the manifest's own copy is
  compared against the inband one by event id. Verified against livesim2's real section
  for the command path; its descriptor loop is empty, so the descriptor reader itself is
  asserted only against built sections — see SC-96.
  <!-- sc: prio=med size=M labels=parser ver=0.3.0 -->
- [x] **SC-37 — CEA-608/708 captions**: captions declared in the manifest
  (`CLOSED-CAPTIONS`, DASH `Accessibility`) against caption data actually carried in
  the bitstream — the ATSC A/53 SEI in H.264 and HEVC, in MPEG-TS and in fMP4, and a
  CMAF `c608`/`c708` caption track beside the video, which is how Apple's own
  reference stream delivers it. "The captions are declared but the encoder stopped
  emitting them" is invisible to every manifest checker.
  <!-- sc: prio=high size=L labels=check,parser ver=0.3.0 -->
- [x] **SC-91 — Attribute a CMAF caption track's field**: the track's samples are
  located from the `trun` data offsets against the `mdat`, and their `cdat`/`cdt2` boxes
  say which CEA-608 field the data is on. Apple's own fMP4 reference stream now reports
  "CEA-608 field 1 (CC1/CC3)" where it used to say the channel was not attributable.
  <!-- sc: prio=med size=M labels=parser ver=0.3.0 -->
- [x] **SC-38 — Subtitle renditions**: WebVTT and TTML/IMSC segments are fetched and
  parsed, and their cue times compared against the window the manifest put them in —
  overlap rather than containment, because a cue crossing a boundary appears in both
  segments. The comparison is anchored to the media timeline the video states, not to
  accumulated `EXTINF`, which is the difference between reading Apple's advanced
  example correctly and reporting all of it ten seconds adrift.
  <!-- sc: prio=med size=L labels=check,parser ver=0.3.0 -->
- [x] **SC-93 — Cues inside an fMP4 subtitle track**: a `stpp` sample is a TTML document
  and a `wvtt` sample a sequence of cue boxes, both now read through the same sample
  location SC-91 needed. A rendition whose segments are the right size and carry nothing
  is the usual shape of a broken subtitle pipeline, and the sample count alone could not
  tell it from a working one.
  <!-- sc: prio=med size=M labels=parser ver=0.3.0 -->
- [x] **SC-97 — Place a wrapped subtitle cue on the timeline**: a `stpp` sample's TTML
  states its times on the presentation timeline — *not* relative to the fragment carrying
  it, which is what the backlog item assumed and what livesim's real stream corrected —
  so a CMAF rendition now gets the same drift check a WebVTT one does. A `wvtt` sample
  times its cue by the sample's duration and states no span, which leaves nothing to
  compare and is reported as such.
  <!-- sc: prio=med size=M labels=check ver=0.3.0 -->
- [x] **SC-21 — MP3 packed audio**: the frame-size tables for every version and layer,
  so a packed MP3 rendition's duration can be measured instead of the container being
  recognised and skipped. A header stating a reserved or free-format field is refused
  rather than guessed at: a length computed from one walks into the next frame and
  counts a plausible wrong number of them.
  <!-- sc: prio=low size=S labels=parser ver=0.3.0 -->
- [x] **SC-22 — Encrypted-segment support with a key**: `--key-file` / `--key-env`
  for AES-128 so the content checks can run on protected streams, and `--fetch-keys`
  to take the key from the URI `EXT-X-KEY` names. The key is given by name, never as
  a literal. The IV defaults to the media sequence number as the specification
  requires, which is the difference between reading a stream that omits the attribute
  and decrypting all of it to noise.
  <!-- sc: prio=med size=M labels=cli,parser ver=0.3.0 -->
- [x] **SC-95 — SAMPLE-AES and CENC**: the CENC scheme is read from `sinf`/`schm` and
  `METHOD=SAMPLE-AES` from the manifest, and both stop the bitstream readers reporting an
  absence they could not have seen — a caption scan over ciphertext succeeded and found
  nothing, which turned a manifest correctly declaring CC1 into a BAD. The `encryption`
  check now says which half of the tool ran. Verified against a real CENC stream whose
  clear twin produces the same findings minus the encryption notes.
  <!-- sc: prio=med size=L labels=parser ver=0.3.0 -->
- [x] **SC-96 — Verify decryption and the splice descriptors against an outside
  authority**: no public AES-128 HLS test stream is reachable — twenty candidates across
  every provider that publishes one — and no reachable stream carries a
  `segmentation_descriptor` either, livesim2's loop being empty. So the gap the item was
  about is closed the other way: the cipher layer is checked against RFC 3602's published
  AES-CBC vectors, third-party plaintext and third-party ciphertext, which pin the mode,
  the key schedule and the IV chaining that a round trip against this repository's own
  encryptor cannot; and the segmentation descriptor is checked against bytes laid out by
  hand from the specification, field by field, so the reader has to agree with the
  standard rather than with its twin builder. Looking for the stream is what found the
  three false findings fixed alongside it.
  <!-- sc: prio=med size=S labels=tests ver=0.3.0 -->

## M5 — Live and delivery <!-- ms: target=v0.4.0 phase=shipped -->

A live edge and a CDN are the two things a single-shot check cannot see. These
items are what turn segcheck from "check this stream once" into "check this
stream the way a viewer receives it".

- [x] **SC-25 — Live-edge watch**: `--watch` re-reads the manifest at the interval
  it implies — `TARGETDURATION` in HLS, `minimumUpdatePeriod` in DASH — and reports,
  per rendition, new-segment latency, stalls and a live edge that stops advancing.
  The edge's identity is the newest segment's URI, not a sequence number: DASH
  renumbers a `SegmentTimeline` every time the window slides, and only the URI
  changes when — and only when — something new is published.
  <!-- sc: prio=high size=L labels=cli,check ver=0.4.0 -->
- [x] **SC-39 — LL-HLS parts**: `EXT-X-PART`, `EXT-X-PART-INF`, `EXT-X-SERVER-CONTROL`
  and `EXT-X-PRELOAD-HINT` are parsed, and `--parts N` fetches a segment's parts and
  compares them with the segment they make up: contiguity, coverage at both edges,
  `INDEPENDENT=YES` against the real sync sample, and measured length against
  `PART-TARGET`. The point is that the parts are not slices of the segment — a
  packager muxes both — so the two descriptions of the same media can disagree, and
  then the low-latency and the normal path deliver different content. The preload
  hint is parsed and never fetched: it blocks until the media exists by design.
  Unit-tested only; no public LL-HLS reference stream was reachable to run it
  against, which is SC-99.
  <!-- sc: prio=high size=XL labels=check,parser ver=0.4.0 -->
- [x] **SC-40 — Multi-period DASH**: the `period` check reads a multi-period MPD as
  one presentation. Every rendition carries the Period it came from and every segment
  how far into that Period it starts, so the two things a boundary hides are visible:
  a Period whose media does not land where `@presentationTimeOffset` maps it — the
  seek-only defect that plays perfectly from the start — and a resolution or codec
  that changes across the join. The ladder-wide comparisons were scoped to a Period
  at the same time: `ladder` and `alignment` had been reading consecutive Periods as
  competing rungs and reported every well-formed multi-period MPD as full of
  duplicates and four seconds misaligned. A Period's first segment may begin before
  the Period does — the grid does not divide by a boundary and the player trims the
  head — and audio is left out of the placement reading entirely, because an AAC grid
  straddles almost every boundary there is: nomor's own DASH-IF vector puts 1.96198s
  segments against a 250s Period. Verified against DASH-IF livesim2's `periods_60`
  (both `@duration` and `SegmentTimeline`) and nomor's 5_1a and 6 test cases.
  <!-- sc: prio=med size=L labels=check,parser ver=0.4.0 -->
- [x] **SC-23 — Cache behaviour**: the `cache` check reads `X-Cache`,
  `CF-Cache-Status`, `Akamai-Cache-Status`, `X-Cache-Hits` and `Age` — every vendor
  spells it differently, and one vocabulary would call a warm edge cold — and reports
  a live edge served entirely from the origin as a BAD, the same shape on demand as a
  WARN, and any segment whose `Cache-Control` says `no-store`, `no-cache` or `private`
  as a BAD whatever the CDN does. `max-age=0` is not one of those: it is what live
  playlists use. An origin stating no cache status is reported as unreadable, not as a
  miss. Verified against Apple's CDN (hits with ages), Akamai's DASH vectors and a
  Unified Streaming origin (both silent).
  <!-- sc: prio=med size=M labels=delivery,check ver=0.4.0 -->
- [x] **SC-24 — Multi-POP comparison**: `--pop ADDR`, repeatable, connects to an
  address the URL does not resolve to while still sending the original `Host` and TLS
  server name, and re-fetches every sampled segment through it. The comparison is of
  the same URLs rather than a second full run — re-running against another edge would
  sample a live playlist at a different moment and report the clock as a difference.
  Reported per edge: segments it did not serve, segments whose bytes differ, and an
  edge that could not be reached, which is a coverage hole rather than a verdict.
  Verified against two real Akamai edges of `dash.akamaized.net`.
  <!-- sc: prio=med size=L labels=delivery,cli ver=0.4.0 -->
- [x] **SC-26 — Byte-range support probe**: the `byterange` check reports whether a host
  honours `Range`, once per host. `fetch` was already saying it once per byte-range
  *segment* — the same host-level sentence four times on a four-segment sample, and said
  underneath the media findings the misconfiguration causes rather than above them. A
  stream addressed in ranges (HLS `EXT-X-BYTERANGE`, DASH `SegmentBase` with an
  `indexRange`) does not degrade when support is off, it fails wearing a media defect's
  clothes: the fixture built for this produced twelve continuity-counter breaks, three
  overlapping segments and a duration 300% long, eight findings about content for one
  setting on one host. Severity belongs to the stream, not the origin — BAD when the
  stream needs ranges, WARN when `Accept-Ranges: bytes` advertises what the origin will
  not do (a claim against a fact, which is the whole brief applied to delivery), OK when
  the stream addresses whole resources and only the seek gets dearer. A probe that never
  completed is an ERROR naming the hole, never a verdict — an origin reconfigured on the
  strength of a dropped connection was fine. It costs nothing on a stream already using
  ranges, because the sample asked the question: Apple's MPEG-TS reference and Sony's
  single-file DASH are measured from their own 326744- and 285730-byte asks, with no
  extra request. `byterange` is in every smoke stream's must-not-fall-silent list.
  <!-- sc: prio=low size=S labels=delivery,check ver=0.4.0 -->

## M9 — Wallclock and DVR correctness <!-- ms: target=v0.4.0 phase=shipped -->

A manifest does not only claim structure, it claims **time in the real world**:
this segment starts at 14:03:22 UTC, this window is sixty seconds deep, this
stream is available now. The media carries the only evidence that can arbitrate
those claims, and nothing in M1–M8 compares the two — every check so far reasons
about a timeline relative to itself.

These are the defects that survive a clean run: the stream is perfectly
continuous, every rung codes what it promises, and a seek still lands in the
wrong place, an ad splices two frames late, or a scrub back into the DVR window
404s. Shares the live-edge machinery with M5, so the two ship together.

- [x] **SC-51 — `EXT-X-PROGRAM-DATE-TIME` against the media**: the `pdt` check
  reports a wall clock that goes backwards, one that advances at a different rate
  from the media it is stamped on, and rungs of a ladder that map the same media to
  different wall clocks — the last being why the item existed: a seek then lands in
  two different places depending on which rung the player is on. The
  cross-rendition comparison is on the offset between wall clock and media, and
  only where the media already lines up, so `alignment` is not double-counted.
  Real streams corrected the design: a playlist states the tag once at the top and
  leaves a client to add the durations forward, so the check saw one segment per
  playlist and none at all when sampling the live edge. The parser carries the
  clock forward now (`PDTDerived`), stopping at a discontinuity with no fresh tag.
  <!-- sc: prio=high size=M labels=check ver=0.4.0 -->
- [x] **SC-52 — DASH `availabilityStartTime` and `UTCTiming`**: the `UTCTiming`
  sources an MPD names are resolved in its own fallback order (`http-head`,
  `http-iso`, `http-xsdate`, `direct`; NTP is named and skipped rather than
  silently ignored), half the round trip is corrected for, and the segment list is
  re-expanded against that answer so the whole run uses the clock the stream
  nominates rather than this machine's. The `availability` check then reports the
  skew, an unbroken run of missing segments at the edge — the signature of a
  packager behind the availability clock rather than of a CDN losing objects — and,
  from one ranged probe of the segment the MPD says does not exist yet, a packager
  ahead of its own window, where every player waits for media that is already
  there. Verified against DASH-IF livesim2's `utc_head` and `http-xsdate` endpoints,
  which is where the negative run duration was found.
  <!-- sc: prio=high size=M labels=check,parser ver=0.4.0 -->
- [x] **SC-53 — The DVR window is real**: the `dvr` check fetches *and parses* the
  oldest segment `timeShiftBufferDepth` — or an HLS playlist's own span — still
  promises. Parsing matters as much as fetching: a CDN serving an error page for
  media it has aged out fails a scrub exactly as completely as a 404 does, and
  alerts on nothing. One segment per run on the top rung, since retention is a
  policy the whole ladder shares. `Rendition.OldestSegment` is computed rather than
  listed, because the live expansion keeps the tail, and clamped to the first
  segment so a window deeper than the stream is old never asks for media that never
  existed. Verified against livesim2 (60s) and a Unified Streaming live ladder
  (601s). Measuring how deep the window *really* is when it fails needs bisection
  over segments the manifest does not list, and is SC-101.
  <!-- sc: prio=high size=M labels=check,delivery ver=0.4.0 -->
- [x] **SC-54 — Discontinuity integrity**: the `discontinuity` check reads
  `EXT-X-DISCONTINUITY` as the instruction it is rather than the description
  `continuity` treats it as. A tag over a timeline that runs straight through it is a
  decoder flush the viewer pays for and nobody asked for; RFC 8216 §4.3.2.3 keeps it
  honest, because the tag signals a change of encoding as well as of timestamps, so a
  continuous timeline over media that really changed codec, track layout or coded size
  stays quiet. `EXT-X-DISCONTINUITY-SEQUENCE` is parsed and carried onto every segment,
  so two rungs that put the same measured moment at different numbers — the same media
  on two different timelines, which stalls a switch — are reported. The DASH half of
  this item is SC-40: a Period boundary expresses its reset through
  `@presentationTimeOffset`, and that is where it is checked. Shipped without a
  real-stream pass; see SC-103.
  <!-- sc: prio=med size=M labels=check ver=0.4.0 -->
- [x] **SC-55 — Live-edge drift**: `--watch` now measures the media published against
  the wall clock over the whole window, not only whether the edge moved. A packager
  publishing two seconds every three advances at every single poll and still loses a
  second of ground per three, so the live latency grows without bound until the
  viewer's buffer is gone — a rebuffer nothing in the stream explains, and a shape no
  pair of polls can show. Falling behind is a BAD, running ahead a WARN, because a
  packager catching up after a stall looks the same as one whose clock is fast and only
  the second keeps going. An edge that moves backwards is the third shape and the one a
  stall check reads as health — the newest segment changed, which is what a working edge
  does — and it is a packager that restarted or a POP answering with an older playlist.
  Verified against Unified Streaming's and DASH-IF livesim2's live edges, both of which
  measure 1x and stay quiet.
  <!-- sc: prio=med size=M labels=check,cli ver=0.4.0 -->

## M6 — Integration <!-- ms: target=v0.5.0 phase=later -->

- [x] **SC-27 — Prometheus/OTLP output**: `--output prometheus` renders the text
  exposition format and `--output otlp` an OTLP/HTTP `ExportMetricsServiceRequest`, both
  from one shared metric set so the two can never drift. The design is in what they leave
  out: a finding's `Target` is `720p seg 38`, a different value every run on a live
  stream, so a `target` label would mint a new series every tick and never retire one and
  a minutely cron would bury the operator's Prometheus within a week. Neither format
  carries a target and a test asserts no target reaches the output. The aggregate is what
  ships — count per check per status, worst severity per check, worst overall, and the run
  itself — because it answers the two questions a dashboard is for and the detail behind
  an alert is one `--output json` away. Every check present states all four statuses
  including zeros, so an alert is `> 0` rather than a question about a series that does
  not exist; a silent check disappears entirely, so catching *that* needs `absent()` and
  the `# HELP` says so. ERROR is 3 and outranks BAD at 2, the project's own order. OTLP
  puts the stream on the resource, not on every point, and quotes `timeUnixNano` because
  the value outruns an exact JSON number. `OTLP` returns no error where `JSON` does, and
  the reason is real rather than convenient: `JSON` marshals findings, whose `*float64`
  Value a future check could set to NaN, and this payload carries no finding Value at all.
  A round trip against our own writer could not catch a shared misreading of the
  exposition format, so it was checked against the reference `prometheus_client` parser —
  a real run plus bodies with a quote, a backslash, a newline and non-ASCII in the URL,
  all accepted and round-tripped byte for byte. That check stays out of CI: a Python
  dependency is not worth it and the zero-dependency rule covers the tooling too.
  <!-- sc: prio=med size=M labels=output,integration ver=0.4.0 -->
- [x] **SC-28 — Slack output**: `--output slack` renders a Block Kit message, worst
  finding first, and posts it when `SEGCHECK_SLACK_WEBHOOK` is set — unset, the payload
  goes to stdout to be inspected or piped. The webhook is only ever read from the
  environment, the rule `--key-env` already exists for, and it appears in no error
  message: not the configuration one, and not the transport one either, where
  `*url.Error` would otherwise print the whole URL. A failed delivery exits non-zero,
  because a report that never arrived is segcheck failing at what it was told to do
  rather than a finding about the stream. Three of Slack's limits are enforced rather
  than discovered, since exceeding any is a bare 400 that names no block: the
  150-character `plain_text` header (cut from the left, so the tail that names the
  stream survives), 3000 characters per text field, 50 blocks. Bounded to fifteen
  findings, always stating how many were left out. `&`, `<` and `>` are escaped in that
  order — Slack's mrkdwn reserves all three and a `container` finding quotes `<html>`
  when an origin serves an error page with a 200, so unescaped it arrives mangled in
  exactly the situation that produced it. Go's HTML escaping is off so the bytes on the
  wire are the bytes Slack renders; with it on, a payload that forgot to escape would
  still look escaped and the distinction would be untestable.
  <!-- sc: prio=med size=S labels=output,integration ver=0.5.0 -->
- [x] **SC-41 — Baseline diff**: `--baseline run.json` compares against a saved
  `--output json` run. The diff is ordinary findings on a `baseline` check, so it sorts
  worst-first, renders in every format and `--exit-on` gates on it without knowing it
  came from a comparison. The design is all in what is stable between two runs: a
  rendition is, one of its segments is not, so a check is compared *per rendition* with
  its segment findings folded in. Pairing exact targets loses the very regression the
  gate exists for — a breaking stream replaces the rendition-level OK with a
  segment-level BAD, and the pair then looks deleted rather than worse; found by a CLI
  test after the unit tests were green, the second time pairing on prose has cost this
  project a bug. A measurement must move 10% and match units, and a baseline of zero is
  an absolute move because anything over nothing is not a percentage. A message is
  compared only where there is no measurement, which is how a moved resolution is caught
  when both runs agree with their own manifest. A check that fell silent is an ERROR, not
  a BAD. `finding.Result.Renditions` is the new seam — a Target is prose and nothing else
  tells a rung from its segments — and `output.ParseJSON` reads a report back through the
  same shape that wrote it so the two cannot drift. Two runs of Akamai's bbb_30fps
  produce no diff; sampling one rung instead of three reports two renditions gone and
  `alignment` silent, which needs two rungs to compare.
  <!-- sc: prio=med size=M labels=cli,output ver=0.5.0 -->
- [x] **SC-29 — GitHub Action**: `action.yml`, composite rather than Docker — a Docker
  action only runs on Linux runners and pays for an image pull every job, while segcheck
  is one static binary with no dependencies, so the download *is* the install. It
  resolves `latest` through the releases redirect (no token, no API quota), verifies the
  archive against the published `checksums.txt` before running it, writes the report into
  the job summary and leaves it in a file the next step reads through the `report`
  output. One `args` passthrough rather than an input per flag: a second copy of the
  CLI's surface here would go stale the first time a flag changed, and a passthrough
  cannot. Gating is segcheck's own `--exit-on`, not something the action invents. Inputs
  reach the shell through the environment and are never interpolated into a `run:` body,
  because a manifest URL comes from outside and `${{ }}` in a script is an injection.
  Linux and macOS; Windows is refused with a message rather than half-supported, its
  archive being a zip that no job exercises. A CI job runs the action on every push to
  main — against the last release rather than the tree, which is what a repository using
  it actually gets — and asserts that `--exit-on bad` really fails a step, because an
  action nothing runs has already rotted.
  <!-- sc: prio=med size=S labels=integration ver=0.5.0 -->
- [ ] **SC-30 — checkfleet module**: expose these analyses as a `stream-deep`
  module in [checkfleet](https://github.com/Allan-Nava/checkfleet), sharing this
  parser rather than reimplementing it.
  <!-- sc: prio=med size=L labels=integration -->
- [ ] **SC-31 — Config file**: `segcheck.yml` with named targets and per-target
  thresholds, for checking several streams in one run. Parsed in-tree — a YAML
  dependency is not worth it, so the subset is deliberately small.
  <!-- sc: prio=low size=M labels=cli -->

## M10 — Authoring-spec conformance <!-- ms: target=v0.5.0 phase=later -->

The question behind most "why was our stream rejected" tickets: does this ladder
satisfy the rules the platform will actually enforce? Apple's HLS Authoring
Specification and DASH-IF IOP are largely a list of constraints on the *media* —
peak-to-average bitrate, an IDR at every segment start, timescales that agree
across an adaptation set — and by M3/M4 segcheck already measures nearly all of
them. This milestone is the verdict layer over measurements it has taken anyway,
not new parsing.

**Scope guard**: only the rules the media can arbitrate. "Provide a 192 kbps
audio rendition" or "declare a `RESOLUTION`" are manifest-only assertions and
belong in [checkfleet](https://github.com/Allan-Nava/checkfleet) — putting them
here would make segcheck a second manifest linter, which is exactly the line
`AGENTS.md` draws. Every rule implemented here must fail on media that a
manifest-only reader would call fine.

- [x] **SC-63 — `--profile apple|dash-if|none`**: selects which rule set runs,
  `none` by default and opt-in for the reason filed. `finding.Finding` gained a
  `Rule` field so a rule is quotable rather than buried in prose, rendered by all
  three renderers. Rule ids are segcheck's own — Apple renumbers its document
  between revisions and a citation against the wrong one is worse than none, so the
  requirement is quoted in the message instead. `dash-if` is accepted and says
  plainly that nothing ran (SC-62) rather than reporting a pass it never made.
  <!-- sc: prio=high size=S labels=cli ver=0.4.0 -->
- [x] **SC-59 — Apple HLS Authoring Spec, the measurable subset**: five rules under
  `--profile apple`, each quoting the requirement in words and putting the measured
  value beside the limit. Where the specification states a number — 200% peak to
  average — the rule uses it; where it does not, the band is segcheck's own and the
  finding says so rather than passing it off as Apple's. The bit-rate table is H.264
  SDR, so a rung in any other codec is measured against nothing rather than the
  wrong number. Apple's own reference stream corrected the IDR rule: "does not open
  on a keyframe" is an assertion in fMP4 and an inference in MPEG-TS, and escalating
  the inference reported bipbop as non-conformant — `media.Track.KeyframeStated`
  separates them now.
  <!-- sc: prio=high size=L labels=check ver=0.4.0 -->
- [x] **SC-60 — I-frame playlists**: the `iframe` check fetches the ranges
  `EXT-X-I-FRAME-STREAM-INF` declares and reads them — each must resolve to a
  keyframe and to nothing else, and the rung must sit on the video's own timeline.
  The rung is a `StreamKind` of its own and never enters `rends`: one picture where
  a check expects two seconds of media is a hole in the timeline, a duration
  mismatch and a bitrate ten times the declared, which is the subtitle trap again.
  Real streams forced two fixes: `EXT-X-MAP` byte ranges were ignored for the rung,
  and fMP4 trick-play fragments state nothing about sync at all, so the keyframe is
  read out of the length-prefixed samples instead — an inference, marked as one. An
  MPEG-TS range carries no PAT or PMT and stays honestly unverified.
  <!-- sc: prio=high size=M labels=check,parser ver=0.4.0 -->
- [ ] **SC-62 — DASH-IF IOP, the measurable subset**: `@codecs` against the
  sample entry the segments actually carry, timescales consistent across the
  representations of one adaptation set, `@segmentAlignment="true"` that is
  actually true, `@startWithSAP` honoured by the fragments. Four claims that are
  routinely copied between MPDs and quietly stop being true.
  <!-- sc: prio=med size=L labels=check -->
- [ ] **SC-61 — Trick-play thumbnails**: `EXT-X-IMAGE-STREAM-INF` and the DASH
  thumbnail `AdaptationSet` declare a tile grid and an interval; the JPEG or WebP
  actually served has its own dimensions and tile count. Fetch one, read the
  image header, and check the grid divides evenly into it. Broken scrub previews
  are reported as player bugs for weeks before anyone measures the sheet.
  <!-- sc: prio=low size=M labels=check,parser -->

## M11 — Content protection, in depth <!-- ms: target=v0.6.0 phase=later -->

The `encryption` check shipped in v0.1.0 answers one question — are these
segments protected when the manifest says they are — and stops there. Everything
that actually breaks a DRM launch is one level down: *which* system, *which*
scheme, *which* key, and whether the media agrees with the manifest about any of
it. A packaging mistake here does not look like a defect. It looks like a stream
that plays perfectly on the developer's Mac and black-screens on a third of the
installed base.

**The principle that scopes this milestone: none of it needs a key.** segcheck
must be able to answer "is this protected the way you said it is" against
production content, in CI, with no credential of any kind — that is what makes
these checks runnable at all. Reading `pssh`, `schm`, `tenc` and `senc` is
reading metadata, not decrypting. SC-22 (`--key`) is the separate, opt-in case
of running the *content* checks on protected media; M11 never needs it, and any
item here that would is out of scope.

- [x] **SC-66 — DRM systems present against declared**: the `drm` check enumerates
  the `pssh` boxes in the initialisation segment by system UUID, names the systems it
  recognises and reports the rest by UUID rather than guessing one, and compares them
  with what the manifest promises (`ContentProtection` urn:uuid entries, HLS
  `KEYFORMAT`, both normalised onto bare UUIDs). Axinom's multi-DRM vector corrected
  the design: DASH-IF puts the key-acquisition data in the MPD's own `cenc:pssh`, so a
  real init carries none and demanding one reported that stream as missing both its
  systems. A declared system is reported missing only when the init is demonstrably
  the acquisition path — it carries `pssh` for other systems and nothing else supplies
  this one.
  <!-- sc: prio=high size=M labels=check,parser ver=0.4.0 -->
- [x] **SC-67 — Encryption scheme**: the `scheme` check compares the `schm` scheme
  with the one the manifest declares, reports a ladder that mixes schemes, and quotes
  the `tenc` `default_KID` so a rung can be matched against a key server. It also
  checks the container against itself, which needs no manifest: a crypt-to-clear
  pattern belongs to cbcs and cens and cannot appear under cenc or cbc1. Axinom's cbcs
  vector corrected that rule — common encryption gives video a pattern and audio
  full-sample encryption, so cbcs audio states none and is right not to.
  <!-- sc: prio=high size=M labels=check,parser ver=0.4.0 -->
- [x] **SC-69 — Clear lead, and media that is not protected at all**: the `clear`
  check reads the per-sample encryption state from `saiz` and reports a rendition the
  manifest declares protected whose samples are all in the clear — the defect that
  ships, plays everywhere, alerts on nothing and surfaces in a rights-holder audit.
  The same measurement gives the clear lead, counted across the segment boundary
  because a lead does not stop where a segment does; its length is a choice nothing
  in the manifest records, so it is reported, and `--clear-lead` turns it into a
  claim to check in both directions. A truncated `saiz` reports nothing rather than a
  run of clear samples: unprotected content is the one direction this must never be
  wrong in.
  <!-- sc: prio=high size=L labels=check,parser ver=0.4.0 -->
- [ ] **SC-68 — Key rotation integrity**: where the manifest rotates keys — a
  new `EXT-X-KEY`, a DASH period or KID change — the KID carried by the segments
  must actually change at that boundary, and no segment may reference a KID that
  was never announced. Both directions are real defects and they fail
  differently: rotation declared but not applied leaves a retired key working,
  rotation applied but not declared black-screens every player at the boundary.
  Needs the segment timeline that SC-7 already builds.
  <!-- sc: prio=med size=L labels=check,parser -->
- [ ] **SC-70 — HLS `METHOD` against the payload**: `AES-128` protects a whole
  segment, `SAMPLE-AES` and `SAMPLE-AES-CTR` protect samples inside an otherwise
  parseable container, and the three are not interchangeable — a player told the
  wrong one produces noise rather than an error. Check the declared method
  against how the TS or CMAF payload is really protected, and flag a
  `METHOD=NONE` that appears mid-playlist without a matching change in the
  media. <!-- sc: prio=med size=M labels=check,parser -->

## M12 — Colour, HDR and the codec string <!-- ms: target=v0.7.0 phase=later -->

Today the `resolution` check compares pixel counts and the codec comparison stops
at the family name: `declaredCodec` turns `avc1.640028` into `"h264"` and asks
only whether the media is H.264 too. Everything after that first dot is thrown
away, and `VIDEO-RANGE` is not parsed at all. So a ladder can declare
`VIDEO-RANGE=PQ` over BT.709 samples, or `avc1.640028` over a Baseline stream,
and segcheck says the stream is fine — because by the only measure it currently
takes, it is.

These are the defects that do not look like defects. Wrong transfer
characteristics do not black-screen: they render, washed out or crushed, on the
subset of devices that trust the manifest over the bitstream, and the ticket
arrives as "the picture looks wrong on TV" months later. A level declared higher
than the media needs costs nothing; declared lower than the media needs is a
decoder that refuses the stream on exactly the hardware that reads the codec
string before it allocates.

**What scopes this milestone: the manifest makes a colour claim, and the media
answers it.** Reading the VUI, the `colr` box and the HDR SEI is reading the
media's own statement about itself, which is the comparison this tool exists to
make. Judging whether the colour volume is *right* for the content is grading,
not checking, and stays out.

- [x] **SC-72 — Colour description readers**: the VUI in H.264 and HEVC and the
  `colr` box with `nclx` in fMP4, round-tripped against `mediatest` writers with
  nothing consuming them yet. The trap the item named is real twice over: in H.264 the
  VUI hides behind an aspect ratio block that may carry an extended SAR, and in HEVC
  behind the short-term reference picture sets, whose sizes depend on one another — set
  N cannot be skipped without having counted set N-1, so the inter-prediction branch
  had to be implemented and is tested. A mismeasured walk returns a plausible wrong
  colour rather than failing, so every value is checked against the assigned ranges and
  a rejected read reports "unstated"; an unstated colour is never read as BT.709, since
  code point 0 is reserved. An ICC `colr` profile states no code points and is declined.
  <!-- sc: prio=high size=L labels=parser ver=0.4.0 -->
- [x] **SC-73 — `VIDEO-RANGE` against the transfer function**: the `videorange` check
  compares HLS `VIDEO-RANGE` — parsed for the first time — and DASH's CICP transfer
  descriptor against what SC-72 reads, in both directions. An absent `VIDEO-RANGE` is
  not an SDR one: a player defaults, a checker can only be wrong about what was stated.
  Apple's Dolby Vision example corrected the reader: most real fMP4 carries no `colr`
  box and states its colour only in the VUI of the parameter set inside `avcC`/`hvcC`,
  so looking for a `colr` and giving up found nothing on the majority of content.
  <!-- sc: prio=high size=M labels=check,parser ver=0.4.0 -->
- [x] **SC-74 — Codec string profile and level** (includes the `av1C`/`vpcC`
  configuration boxes, moved here from SC-42): the whole string, decomposed per
  grammar — `avc1.PPCCLL` in hex and its older dotted decimal form, `hvc1.P.C.LX.B`
  with its tier letter, `av01.P.LL[MH].BB` with the tier glued to the level, `vp09` —
  against `avcC`/`hvcC`/`av1C`/`vpcC` in fMP4 and the SPS or profile_tier_level in
  MPEG-TS. Both directions, reported differently: below the media is a rung no device
  asks for (BAD), above it is viewers silently excluded (WARN). An undecomposable
  string is OK-level "not verifiable". Apple's bipbop confirmed the check on its first
  run — a 1080p rung declaring level 3.1 — and fourteen of segcheck's own fixtures
  turned out to declare a profile and level their media did not carry.
  <!-- sc: prio=high size=L labels=check,parser ver=0.4.0 -->

- [ ] **SC-75 — HDR10 static metadata**: a rendition that declares PQ should
  carry mastering-display colour volume and content light level — SEI 137 and
  144 in the elementary stream, `mdcv` and `clli` in the sample entry. Missing
  metadata is not fatal, which is exactly why it ships: the picture is merely
  tone-mapped by the display's guess instead of the grade's intent, on every
  panel that would have honoured it. Reported at OK level with the measurement
  attached when present, one rung above when a PQ ladder carries none at all.
  <!-- sc: prio=med size=M labels=check,parser -->
- [ ] **SC-76 — Dolby Vision**: `dvh1`/`dvhe`/`dvav` sample entries and the
  `dvcC`/`dvvC` configuration box against HLS `SUPPLEMENTAL-CODECS` and the DASH
  `dvb:` / `ContentProtection`-adjacent DV descriptors — profile, level and the
  cross-compatibility id that decides whether a non-DV device sees a usable
  base layer at all. `dvh1` and `dvhe` already parse as visual sample entries so
  resolution works, which makes the gap quiet: the ladder looks checked. Profile
  8.4 declared with a cross-compatibility id of 0 is an HDR stream that plays as
  nothing on every device without a DV decoder.
  <!-- sc: prio=med size=L labels=check,parser -->
- [ ] **SC-77 — Colour consistency across the ladder**: one `VIDEO-RANGE` group
  whose rungs disagree — an SDR 360p rung inside a PQ ladder, a rung that
  switches matrix coefficients, a full-range flag set on one rendition only.
  ABR switches between these mid-playback and the picture shifts on the switch,
  which reads as a network problem to everyone watching. Needs SC-72 and the
  per-rendition fan-out `ladder` already walks; the check is the comparison
  between rungs rather than against the manifest, which is what makes it worth
  its own item. <!-- sc: prio=med size=M labels=check -->

## M13 — Audio, past the sanity check <!-- ms: target=v0.8.0 phase=later -->

SC-18 is the floor: sample rate and channel count consistent within a rendition
and against `CODECS`. Everything above that floor is currently invisible, and the
reason is one gap in the parser — `parseStsd` reads width and height and stops,
so an fMP4 audio track's real configuration is never read at all. Channel counts
exist only for packed ADTS, where the frame header states them. `ac-3`, `ec-3`,
`Opus` and `fLaC` are recognised by sample entry name and nothing more: their
configuration boxes are never opened. On the manifest side, `CHANNELS` and
`AudioChannelConfiguration` are not parsed by either reader.

The result is that the audio half of a ladder is checked by its name. A rendition
can declare `CHANNELS="6"` over a stereo track, `mp4a.40.2` over SBR content, or
`16/JOC` over an E-AC-3 stream carrying no object metadata, and every one of them
comes back clean.

**What scopes this milestone: metadata, never decoding.** Reading
`AudioSpecificConfig`, `dac3`, `dec3`, `dOps`, `dfLa`, `elst` and `dialnorm` is
reading what the media says about itself, which is the comparison this tool
exists to make and which stays honest at zero dependencies. Judging what the
audio *sounds* like — silence, clipping, measured loudness — needs a decoder,
which is why SC-18 already rules it out and why nothing here reintroduces it.

- [x] **SC-81 — Audio configuration boxes**: `esds` and its AudioSpecificConfig for
  `mp4a` — object type, sampling frequency index, channel configuration, and the SBR
  and PS extensions — plus `dOps`, `dfLa` and the `dac3`/`dec3` boxes already read.
  Nothing consumes them yet; they land with the `mediatest` writers and the round trip,
  and the writer states the sampling-frequency table independently of the reader so the
  round trip agrees with the standard rather than with itself. `AudioConfig` carries the
  *coded* rate and channel count and records SBR and PS, because the sample entry
  describes the rendered output and for HE-AAC those are deliberately different.
  <!-- sc: prio=high size=L labels=parser ver=0.4.0 -->
- [x] **SC-82 — `CHANNELS` against the real channel count**: the comparison itself
  shipped with SC-18; what SC-81 made possible was getting the Parametric Stereo
  exemption right. HE-AAC v2 codes one channel and renders two, so a declared 2 over a
  coded 1 is correct — but that exemption was granted on the codec string saying
  `mp4a.40.29`, which forgave a genuine mono-declared-stereo mismatch on any stream
  that merely claimed to be HE-AAC v2. The AudioSpecificConfig states PS outright, so
  where the media says, the media decides, and the codec string is the fallback only
  for media that states nothing.
  <!-- sc: prio=high size=M labels=check,parser ver=0.4.0 -->
- [x] **SC-83 — Audio codec string against the configuration**: `mp4a.40.2` declared
  over a configuration that explicitly states object type 5 or 29, and `ec-3` declared
  over an `ac-3` sample entry — a different decoder rather than a different
  configuration of one. Two public reference vectors corrected it before it shipped:
  HE-AAC is normally signalled *implicitly*, with an AAC-LC core in the configuration
  and the SBR data in the payload, so `mp4a.40.5` over object type 2 is the ordinary
  way HE-AAC is carried and the configuration alone can neither confirm nor deny it.
  segcheck says so rather than reporting it. Unparseable strings are OK-level "not
  verifiable".
  <!-- sc: prio=high size=M labels=check ver=0.4.0 -->
- [ ] **SC-84 — Loudness metadata**: `dialnorm` in the AC-3/E-AC-3 bitstream and
  the `ludt`/`loud` box where it is present, reported per rendition and compared
  across the ladder. A ladder whose rungs disagree on dialnorm steps in volume
  every time ABR switches, which viewers report as the stream being "quiet" and
  operators cannot reproduce because it only happens on a switch. Reading the
  declared value is metadata; measuring actual loudness is decoding and stays out.
  <!-- sc: prio=med size=M labels=check,parser -->
- [ ] **SC-85 — Immersive audio against the badge**: `CHANNELS="16/JOC"`, the
  DASH `SupplementalProperty` for object audio and the Dolby Atmos claim in
  general, against the JOC flag and complexity index in `dec3`. The badge is a
  product promise as much as a technical one, and when it is wrong the stream
  still plays — in 5.1, on a system that was bought for the thing the manifest
  says is there. <!-- sc: prio=med size=M labels=check,parser -->
- [ ] **SC-86 — Encoder delay and priming**: the `elst` media start offset and
  the priming samples an AAC encoder prepends, which are the number of samples a
  player must discard before the audio lines up with the video. Applied by one
  player and ignored by another, they are a lip-sync error of 20–50 ms that
  appears on some devices and not others — the hardest audio defect to get
  reported accurately, because the half of the audience that sees it cannot prove
  it. Reports the offset per rendition and flags a ladder whose rungs disagree.
  <!-- sc: prio=med size=L labels=check,parser -->

## M8 — Container image and supply chain <!-- ms: target=v0.1.1 phase=shipped -->

A distribution milestone, not a checking one: it does not widen what segcheck
looks at, it widens where it can run — a CI runner with no Go toolchain, a
Kubernetes `CronJob`, an operator who has Docker and nothing else. The
zero-dependency static binary is what makes a `FROM scratch` image honest:
nothing to install, nothing to patch, nothing to CVE-scan but our own code.
Targeted at v0.3.0 rather than parked at the end because it is orthogonal to the
checks roadmap and blocks nothing.

- [x] **SC-45 — Image smoke test, written first**: `internal/analyze/docker_test.go`
  behind the `docker` build tag, run by its own CI job. It asserts the version is
  stamped, that no shell is reachable, that the image ships a CA bundle, that it
  does not run as root, and that a containerised run finds a planted continuity
  gap in a live origin while still exiting 0. Written and watched fail before
  SC-43 existed; the trust-store assertion was then checked against a
  deliberately bundle-less image to confirm it can fail.
  <!-- sc: prio=high size=M labels=tests,release ver=0.1.1 -->
- [x] **SC-43 — Dockerfile (scratch, non-root)**: multi-stage, building with
  `CGO_ENABLED=0 -trimpath` and the same `-X main.version` ldflag goreleaser
  uses, so the image and the released binary can never report different
  versions. Final stage `FROM scratch` with the binary, `USER 65532:65532`, and
  `/etc/ssl/certs/ca-certificates.crt` copied from the builder — **the CA bundle
  is the trap**: without it every `https://` manifest fails TLS inside the
  container, and the failure reads like an origin defect rather than a packaging
  one. <!-- sc: prio=high size=M labels=release,project ver=0.1.1 -->
- [x] **SC-44 — Multi-arch image on GHCR**: `linux/amd64` and `linux/arm64` from
  one `dockers_v2` entry (the older `dockers` + `docker_manifests` pair is
  deprecated and fails `goreleaser check`), packaging the binaries goreleaser
  already built rather than rebuilding them, tagged `vX.Y.Z` plus a `latest`
  that stays put on a prerelease. `Dockerfile.release` copies from
  `${TARGETPLATFORM}/segcheck` because that is where the binary is staged.
  Verified by a full `--snapshot` run; the push itself is first exercised at the
  next tag, and the release workflow re-runs SC-45's test against the published
  image. <!-- sc: prio=high size=M labels=release ver=0.1.1 -->
- [x] **SC-56 — `:edge` image from `main`**: `.github/workflows/docker.yml`
  publishes `ghcr.io/allan-nava/segcheck:edge` and `:sha-<commit>` on every push
  to `main`, so the state of main is runnable without waiting for a tag and a
  packaging regression surfaces on the commit that caused it. SC-45's contract
  test gates the push — a broken image never reaches the registry. Releases stay
  release.yml's job, where goreleaser packages the exact binaries it built for
  the archives. The `Dockerfile` build stage is pinned to `$BUILDPLATFORM` with
  `GOOS`/`GOARCH` from `TARGETOS`/`TARGETARCH`, so multi-arch cross-compiles
  instead of dragging a `go build` through QEMU.
  <!-- sc: prio=high size=S labels=release ver=0.1.1 -->
- [x] **SC-46 — Scheduled-run recipes**: `docs/running-in-containers.md` — a
  Compose service and a Kubernetes `CronJob`, with `backoffLimit`, a locked-down
  `securityContext`, a pinned tag, the egress cost of a schedule, and why
  `--exit-on` must stay off in a `CronJob` (a non-zero exit would make the
  scheduler retry a run that worked).
  <!-- sc: prio=med size=S labels=docs,delivery ver=0.1.1 -->

## M7 — Project and release <!-- ms: target=ongoing phase=ongoing -->

- [x] **SC-105 — SEO for the published page**: the crawler-facing half of the
  docs site, generated from the page rather than maintained beside it —
  `sitemap.xml`, `robots.txt` and an `llms.txt` for the crawlers that read prose
  — plus `robots`, `theme-color`, `og:locale` and `og:image:alt` meta and the
  fields a search result is built from in the existing JSON-LD.
  `scripts/seo.sh check` gates it in CI *and* before the Pages deploy, because
  every failure here is silent: a canonical that drifted, a JSON-LD block
  truncated by an edit, a sitemap naming a URL that is not there. That last one
  is not hypothetical — the first sitemap this item shipped listed
  `running-in-containers.html`, and Pages serves `docs/` as committed, so the
  `.md` answers 200 and the `.html` 404s. The gate now refuses any `<loc>` with
  no file behind it.
  The reason it earns a place in M7 rather than being hygiene: crawlers read
  `robots.txt` only from the host root, and `allan-nava.github.io`'s is generated
  from a daily sync over the project sitemaps that answer **200**. A site that
  ships none is simply absent — segcheck was one of 28.
  `<lastmod>` is the last commit that touched the page, not the day the generator
  ran: the clock version moved the date on a page that had not changed and left it
  stale on one that had, and a lastmod caught lying once is discounted for the
  whole site. Not mtime inside a work tree — a checkout stamps every file with the
  moment it ran — and where no history exists the check says so and skips rather
  than gating the build on how it was cloned.
  <!-- sc: prio=med size=S labels=project,docs ver=unreleased -->
- [x] **SC-34 — Backlog and roadmap tooling**: `scripts/backlog.sh` lints this
  file and regenerates [ROADMAP.md](ROADMAP.md) from it, with a CI gate that
  fails on a stale roadmap or a malformed item. Zero-dep, POSIX sh and awk.
  <!-- sc: prio=med size=M labels=project ver=0.1.1 -->
- [x] **SC-36 — Real-stream smoke suite**: `internal/analyze/smoke_test.go`
  behind a `smoke` build tag, running the **built binary** against Apple's fMP4
  and MPEG-TS references, a DASH `SegmentTemplate` manifest and a single-file
  on-demand one — so what is under test is the thing that ships, `--output json`
  and the exit-code contract included. The release workflow depends on it, so a
  tag cannot go out without it, and CI runs it on every push to main but not on
  pull requests: a contributor should not see a red build because a CDN was
  briefly unreachable.
  The item asked for "fails on anything above OK", and that rule does not survive
  contact with the streams — Apple's advanced example legitimately over-declares
  BANDWIDTH by about 2x and ships rungs where more bandwidth buys fewer pixels, and
  a correct segcheck reports both. So each stream carries a **baseline** of the
  checks allowed to exceed OK, with the reason written down; anything outside it is
  a regression. The second half matters more: a list of checks that must produce
  something at all, because the failure mode of this tool is a parser that quietly
  stops reading, and silence reads exactly like a clean bill of health. Verified by
  breaking things on purpose — reverting the `trex` fix is caught (as `continuity`
  going silent), and so is a resolution reader that always refuses.
  <!-- sc: prio=high size=S labels=tests,release ver=0.2.0 -->
- [x] **SC-57 — `internal/fetch` tests** (was 0.0% of statements, now 94.1%).
  The truncation boundary is table-driven across under / exactly-at / one-over /
  far-over the cap, because `>` versus `>=` there is the difference between an
  honest ERROR and a confident wrong answer — verified by mutating the
  comparison and watching the exactly-at-the-cap case fail. Also covers the
  `segcheck/<version>` User-Agent, custom headers, `Range` sent only when asked,
  a 404 whose body and status still reach the caller, the timeout, context
  cancellation, `ContentType` parameter stripping and `CacheStatus` precedence.
  <!-- sc: prio=high size=M labels=tests ver=0.1.1 -->
- [x] **SC-58 — `cmd/segcheck` tests** (was 0.0% of statements, now 90.1%).
  `main` is now `os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))`, and the flag
  set moved from `ExitOnError` to `ContinueOnError` — a flag package that calls
  `os.Exit` itself takes the exit code out of `run`'s hands. The invariant the
  README leads with is asserted directly: a stream full of BAD findings exits 0,
  `--exit-on bad` exits 1 on it and 0 on a clean stream, `--exit-on error` does
  not trip on a BAD one. Ten usage errors are asserted to exit 2 *and* to say
  what is wrong. `parseInterspersed` is covered by comparing flags before and
  after the URL — verified by mutation, since a regression there silently
  ignores every flag after the URL while the run still succeeds.
  <!-- sc: prio=high size=M labels=tests,cli ver=0.1.1 -->
- [x] **SC-47 — SBOM and signed artefacts**: v0.1.1 published an SBOM beside
  every archive plus `checksums.txt.sig`/`.pem`, and signed the image manifest.
  Both halves of the acceptance test pass against the published release:
  `cosign verify-blob` on the checksums returns `Verified OK`, and
  `cosign verify` on `ghcr.io/allan-nava/segcheck:0.1.1` validates the claims
  against the transparency log with the certificate identity resolving to
  `release.yml@refs/tags/v0.1.1`. Keyless throughout — nobody holds a private
  key. <!-- sc: prio=med size=M labels=release ver=0.1.1 -->
- [x] **SC-65 — The published cask actually installs**: it did not. Installing the
  published 0.2.0 cask on macOS 14 arm64 answered both questions. (1) Homebrew Cask
  stamps `com.apple.quarantine` on the staged binary and Gatekeeper rejects the
  ad-hoc signature the Go linker emits — `spctl -a` returns *rejected* — but the
  failure is worse than the dialog this item predicted: from a terminal there is no
  dialog at all, just SIGKILL, exit 137, and not one byte of output. `brew install
  --cask` reports success either way, so the tool reads as a broken build and the
  person who hits it reports the wrong bug. A `hooks.post.install` stripping the
  attribute fixes it, verified by installing a cask carrying the hook against the
  real published archives: quarantine absent after install, `--version` exits 0,
  and a check against Apple's fMP4 reference returns 15 OK. Two details the fix
  turns on, both found by getting them wrong first — `/usr/bin/xattr` by absolute
  path, because a Homebrew or conda Python `xattr` earlier on PATH does not accept
  `-r`; and `-dr` rather than `-d`, because only `-dr` exits 0 when the attribute is
  already gone, and a postflight that fails on a clean install is worse than the
  problem. Notarisation is the real fix and is now SC-94. (2) No formula alongside
  the cask: `go install`, the archives and the image already cover Linux, and a
  second packaging path exists to be the one that goes stale. Said in the README
  rather than left implied. <!-- sc: prio=med size=S labels=release,docs ver=0.3.0 -->
- [x] **SC-80 — GitHub issues generated from the backlog**: the plan was visible
  only to someone reading `BACKLOG.md` or `ROADMAP.md` in the repository, so a
  contributor looking at the issue tab saw an empty project. `backlog.sh issues`
  now syncs them, one way — backlog to issues, never the reverse, because the
  `SC-n` id is what commits and the CHANGELOG reference and a title edited on
  GitHub must not be able to move it. Deciding what to do is kept apart from
  doing it: `issues` prints a plan and touches nothing, `--apply` executes it.
  That split is what makes the interesting half testable, and
  `scripts/backlog_issues_test.sh` asserts it against a fixture backlog with no
  network call and nothing created on a public repository — every state told
  apart (create, reopen, close, leave alone, and *never* open an issue for work
  already shipped), idempotence proved on a settled backlog because a sync that
  is not idempotent opens a duplicate on every push, the milestone filter, the
  body's contents, and a malformed backlog stopping the sync rather than planning
  against half of it and closing issues for items it merely failed to read. The
  script creates the label vocabulary and the milestones it needs, so a fresh
  clone or a fork does not fail on the first apply with an unknown label. The
  workflow runs on a push that touches `BACKLOG.md` or the script — nothing else
  can change the plan — under a `concurrency` group, because two runs racing would
  both see "no issue for SC-n" and open it twice. Does not close SC-64: `lint` and
  `roadmap` are still untested. <!-- sc: prio=med size=M labels=project,tests ver=0.2.0 -->
- [x] **SC-64 — `scripts/backlog.sh` has no tests**: `scripts/backlog_test.sh` covers the
  generator and the linter against a fixture backlog — 23 assertions, POSIX sh and awk,
  wired into the `backlog` CI job beside the issue planner's. The roadmap side asserts
  what SC-63 broke: a `|` inside an item title is escaped, and every row of every table
  has the same number of cells, which is the invariant a split row violates and nothing
  else does. Also the em dash round trip, priority ordering inside a milestone, and that
  `check` fails on a stale roadmap — a CI gate that stopped failing would have gone
  unnoticed indefinitely. The linter side asserts each rule fails for its own reason:
  duplicate id, gap in the sequence, unknown prio, unknown size, no metadata comment, a
  done item with no `ver=`, an open item inside a shipped milestone — and that the
  well-formed fixture passes, without which every one of those is about a linter that
  rejects everything. Writing the test first found the seam it needed: `roadmap` wrote
  to a hard-coded path and the first run overwrote this repository's own ROADMAP.md, so
  `ROADMAP_FILE` joins `BACKLOG_FILE` as an override.
  <!-- sc: prio=med size=S labels=tests,project ver=0.4.0 -->
- [x] **SC-71 — The untested helpers behind the findings** (total was 70.7% of
  statements, now 76.2%; `internal/media` 74.1 → 84.3, `internal/manifest`
  79.4 → 87.9, `internal/analyze` 85.5 → 89.5, `internal/finding` 83.3 → 94.4).
  An audit for functions that no test ever called, taking the ones whose failure
  mode is a *wrong finding* rather than a crash. `parseTrun` was the worst of
  them at 38.8%: its per-sample fields are an optional bitmap, and the
  composition-time offset is unsigned in a version 0 box and signed in a version
  1 one — read the version 1 case as unsigned and a small negative offset moves
  the segment's start about 13 hours forward, reporting a gap that is not there.
  Every stride is now pinned by one box carrying all four optional fields, so
  dropping any of them makes the offset come out of the preceding word.
  `media.Timeline` had no test at all despite being the promise that a
  cross-segment check never compares a video start against an audio start.
  `declaredCodec` gained a contract test asserting every name it can return is a
  name some parser actually produces — the two tables live in different packages
  and compare by string, so drift there reports a codec mismatch on every stream
  of that codec. `describeCounts` is asserted stable over 200 runs because its
  keys come out of a map and Go randomises that iteration, which would make two
  runs of the same stream render differently. Also `firstTemplate`'s three-level
  DASH inheritance (a dropped `@timescale` makes every duration unmeasurable, and
  the merge must not write through to the AdaptationSet template every sibling
  representation shares), `dashKind`/`dashName`/`streamInfKind`, both codec
  tables, and the severity order itself. Verified the way SC-57 and SC-58 were:
  60 mutations applied to the functions under test, 59 caught. The one survivor
  is provably a no-op — `applyBaseURLs`' blank-BaseURL guard, since
  `ResolveReference("")` returns the base either way. Two of the tests were
  themselves fixed by that pass, having asserted values a broken implementation
  would also have produced.
  <!-- sc: prio=high size=M labels=tests ver=0.2.0 -->
- [x] **SC-78 — Coverage to the practical ceiling, and a gate that holds it**
  (99.64% of statements, from a true baseline of 90.94%). Two measurement bugs
  came first, and the reported numbers before this were all wrong: `go test
  -cover ./...` gives each package credit only for its own tests, so
  `internal/media/mediatest` read 0.0% despite every parser test running through
  it, and `go test -coverpkg=./...` then emits one copy of each block per test
  binary which `go tool cover -func` **sums instead of merging** — a block
  covered by one binary of seven reads as 1/7. `scripts/coverage.sh` merges by
  block position in awk, and `-count=1` is mandatory because a cached package
  result carries the line numbers its source had when cached, mixing two versions
  of a file into one profile. CI now fails below 99%. Filling the gap covered
  every remaining branch of `ParseTS` (mid-segment resync, null and
  adaptation-only packets, a scrambled payload, a stream seen before its PMT),
  the ISO-BMFF box plumbing (64-bit sizes, size-0 boxes running to EOF, version 1
  `tkhd`/`mdhd`/`tfdt`, every `tfhd`/`trun` flag combination), the H.264 chroma
  formats and both variable-length blocks before the resolution, ADTS header
  variants and the ID3 frame walk, DASH `SegmentList` and open-ended `@r`,
  HLS `EXT-X-MEDIA` types and byte ranges with implicit offsets, and every
  measurement guard in the checks — the `(value, false)` paths where a check must
  stay silent. `main` is covered by re-executing the test binary as a
  subprocess, which is the only way to assert that an exit code reaches the
  shell. **Nine statements remain uncovered and are unreachable by construction**,
  not untested: `pick`'s index clamp and duplicate guard (with `len > max` the
  step is strictly greater than one, so indices strictly increase and never reach
  `len`), the HEVC `sps_max_sub_layers_minus1 > 7` check (a three-bit field
  cannot exceed 7), `ParseMP4`'s no-track error when fragments are present (the
  loop over a non-empty map always appends), `ParseTS`'s `payloadStart >=
  len(pkt)` (4 against 188) and `packets == 0` (`tsSyncOffset` guarantees an
  iteration), the JSON render error in `run` (no check produces a non-finite
  `Value`), and `useColor`'s `os.Stdout.Stat()` failure. Each is a guard worth
  keeping against a future refactor; removing them to reach a round number would
  trade real safety for a metric.
  <!-- sc: prio=high size=L labels=tests,project ver=0.2.0 -->
- [x] **SC-48 — Coverage ratchet**: `scripts/coverage.sh check` compares the
  measurement against the figure recorded in `scripts/coverage.floor` and fails
  when a commit lowers it, which is what test-first needs to hold — a check
  merged without its test now shows up in the build rather than in a review three
  weeks later. SC-78 delivered only half of this and the distinction matters: a
  hard-coded floor of 99% would have let coverage slide from 99.64% to 99.01%
  without a word. The floor is a committed file rather than a number in the
  workflow, so raising it is a reviewable diff, exactly as `ROADMAP.md` is
  generated but committed. It only goes up: a drop fails, `update` refuses to
  record a figure below the current floor so a regression cannot be laundered
  into the baseline, and a gain beyond a 0.25-point tolerance also fails asking
  to be locked in — a ratchet that never tightens is just a floor, and the slack
  it leaves is room for a real loss to hide in. The tolerance exists so that
  deleting a couple of uncovered lines does not nag. Measuring needs `go test`
  but deciding does not, so `COVERAGE_ACTUAL` injects a measurement and
  `scripts/coverage_test.sh` asserts the whole decision table without running the
  suite: the drop, the one-statement drop, standing still, a gain worth
  recording, a missing floor file, a floor file with rubbish in it (which must not
  read as zero and pass everything), and `update`'s refusal to go down.
  <!-- sc: prio=med size=S labels=tests ver=0.2.0 -->
- [x] **SC-32 — Homebrew tap upload**: `skip_upload` is gone, so the next tag
  writes `Casks/segcheck.rb` into `Allan-Nava/homebrew-tap`. The credential is a
  fine-grained PAT scoped to that one repository with *Contents: read and
  write*, stored as the `HOMEBREW_TAP_TOKEN` secret — the workflow's own
  `GITHUB_TOKEN` is scoped here and cannot write to another repository. Verified
  before enabling: the token reports `push: true` on the tap, the secret is
  present on the repo, and a `--snapshot` run still generates the cask without
  publishing. The push itself is first exercised at the next tag. Fine-grained
  PATs expire, so a release after that date will publish its archives and image
  and then fail at this step. <!-- sc: prio=med size=S labels=release ver=0.1.1 -->
- [x] **SC-33 — Docs site**: GitHub Pages served from `docs/`, in the shape of
  the checkfleet site — one self-contained static page (hero and sample output,
  the manifest-claims table, the check matrix with worst status, install, flags,
  CI recipes, sampling cost, limits, roadmap) plus the deploy workflow. No
  Jekyll, no theme gem, no external request at render time, in keeping with the
  zero-dependency rule. <!-- sc: prio=low size=M labels=docs ver=0.1.1 -->
- [x] **SC-50 — Brand assets**: logo, favicon, horizontal wordmark and OG card,
  all hand-written SVG in `docs/assets/` with PNG renders for the consumers that
  cannot take vectors. The mark is the rendition ladder with one segment the
  media does not have, flagged, and the verdict beside it — the product's one
  sentence in a 32px square. Same plate and palette as checkfleet, so the two
  read as siblings. <!-- sc: prio=low size=S labels=docs ver=0.1.1 -->
- [ ] **SC-49 — Per-check reference pages**: one page per check behind the
  matrix — what it measures, how the threshold is applied, what a finding means
  for a viewer and what to do about it. The single page carries the summary
  well; it cannot carry that depth for thirteen checks.
  <!-- sc: prio=low size=M labels=docs -->
- [ ] **SC-94 — Developer ID signing and notarisation**: SC-65 strips
  `com.apple.quarantine` in the cask's postflight, which makes Homebrew work by
  telling Gatekeeper to look away rather than by giving it something to verify.
  The tarball on the releases page is untouched by that: it is still an
  ad-hoc-signed binary macOS kills on sight, so anyone who downloads an archive
  instead of using Homebrew meets exactly the exit-137 silence SC-65 closed — and
  the archive is the only macOS path for someone who does not want a tap. The fix
  is a Developer ID Application certificate, `codesign --options runtime` over the
  darwin builds, `notarytool submit` on the archives, and then deleting the
  postflight. It needs a paid Apple Developer account and two more release
  secrets, which is why it is not part of SC-65.
  <!-- sc: prio=med size=M labels=release -->
- [ ] **SC-98 — The generated cask fails the tap's style audit**: goreleaser's template
  emits `on_intel` before `on_arm` and a blank line between the `on_macos` and `on_linux`
  groups — four `Cask/StanzaOrder` and one `Cask/StanzaGrouping` per cask. It hits
  `segcheck.rb` and `checkfleet.rb` identically, both being goreleaser-written, so
  `Allan-Nava/homebrew-tap`'s `cask-ci.yml` style step fails after every release of either,
  and nothing in `.goreleaser.yaml` reaches it. The tap already has the convention —
  `--except-cops Cask/Desc,Layout/EmptyLinesAroundBlockBody`, commented as generated-file
  offences that get rewritten every release — so adding
  `Cask/StanzaOrder,Cask/StanzaGrouping` to that list is the same argument and one line.
  The cop that caught a real defect, `Style/NumericPredicate` on our own postflight, stays
  out of the list and keeps running: exclude what polices a machine's formatting, keep what
  polices behaviour. The alternative, `brew style --fix` committed back by a tap workflow,
  buys a canonical file at the cost of a workflow writing to its own repo and is undone by
  the next release anyway. Belongs in the tap; filed here because segcheck's release is
  what surfaced it.
  <!-- sc: prio=low size=S labels=release -->
- [x] **SC-101 — A failed DVR window reports the claim, not the truth**: when the oldest
  segment `timeShiftBufferDepth` promises is missing, the `dvr` finding now also says how
  much of the window the origin really holds — the number an operator changes a retention
  setting with, and the one thing the manifest cannot supply, the manifest being what
  just turned out to be wrong. `Rendition.WindowProbes` carries sixteen segments spanning
  the window, which only `internal/manifest` can produce because a dynamic MPD lists no
  intermediate segment and the template has to be evaluated at indices nobody asked for;
  HLS lists everything it has, so its playlist is its own ladder. The boundary is
  monotone, so four requests bisect it, and they are only ever spent on a stream already
  known to be broken. The figure is reported as a lower bound — the bisection lands on a
  probe point and the real boundary is somewhere before it — because understating is the
  only safe direction: retention set from an overstated figure shortens a window that was
  already too short.
  <!-- sc: prio=med size=M labels=check,delivery ver=0.4.0 -->
- [x] **SC-100 — Two rungs at one resolution get one name**: names are made unique once
  the whole ladder is parsed, because whether one collides is a property of the ladder
  and not of the variant. Only the rungs that actually collide grow a suffix — the
  bitrate, which is what differs and what an operator picks between them by — so `720p`
  stays `720p` on every ladder that never had the problem. Two rungs identical in both
  respects are a defect `ladder` reports and still have to be nameable, so they fall
  through to an index. HLS and DASH share the pass. Verified against the Unified
  Streaming live demo this was filed from: its two `container 720p` rows are now
  `720p 1316kbps` and `720p 658kbps`.
  <!-- sc: prio=med size=S labels=output ver=0.4.0 -->
- [x] **SC-99 — No low-latency reference stream in the smoke suite**: the search was run
  again and came back empty — Apple's `ll-hls-test.apple.com` vectors, Unified
  Streaming's low-latency endpoints and THEO's demo are all gone or unreachable — so the
  second option is what shipped: `local-ll-hls` serves two plain segments and two the
  packager is still publishing parts for, from `mediatest` over a loopback origin,
  through the built binary like every other entry. The limit is stated rather than
  papered over: a stream this repository builds itself cannot catch a shared misreading,
  because the writer and the reader agree by construction, so it never counts towards the
  guard that says a real stream was reached. What it does catch is the half that has
  actually gone wrong here — `parts` falling silent, which was verified by stubbing the
  check out and watching the suite go red. A real endpoint is still worth having: SC-104.
  <!-- sc: prio=med size=M labels=tests ver=0.4.0 -->
- [x] **SC-102 — A Period behind an `xlink:href` swallows the periods after it**: the
  wider defect underneath it turned out to be simpler and to fire on a stream with no
  `xlink` in it at all. `@start` is optional — ISO/IEC 23009-1 makes an absent one the
  previous Period's start plus its duration — and segcheck defaulted the absence to zero,
  so nomor's `5b/1` DASH-IF vector, three Periods and not one `@start` between them, had
  all three placed at 0.000s. `@duration` is derived the same way now, from the next
  Period's start and from `@mediaPresentationDuration` for the last one only, because
  giving an interior Period the whole presentation's length is how a single-period
  assumption survives into a multi-period MPD. The reference is still not resolved, and
  that is now said rather than papered over: a Period behind an `xlink:href` states no
  duration here, so nothing after it is derivable, and `Rendition.PeriodStartKnown`
  carries the absence so `period` reports the hole in its coverage instead of printing a
  position nobody computed. The suite's first multi-period reference stream lands with
  it — the arithmetic SC-40 added had never had the real-stream pass.
  <!-- sc: prio=med size=M labels=parser ver=0.4.0 -->
- [x] **SC-103 — No reference stream carrying a discontinuity**: the search was widened
  before settling — the media playlists behind Apple's, mux's, JW's, Unified Streaming's
  and Red Bull's masters were all read this time, not only the masters — and every one of
  them still declares zero. So `local-discontinuity` serves four 2s segments with a real
  timeline reset under the tag, from `mediatest` over a loopback origin, through the built
  binary. `discontinuity` is in that stream's must-not-fall-silent list and stubbing the
  check out was watched turning the suite red. The reset sits at index 2 deliberately: the
  suite samples three segments from the head of a VOD playlist, so the tag and a segment
  either side of it are what gets fetched. The encoding-change exemption is **not** closed
  by this and is not claimed to be — a synthetic shape change proves only that the code
  agrees with itself, and it is a real packager's discontinuity at a real codec change
  that has to keep the check quiet. That residue is SC-104.
  <!-- sc: prio=med size=M labels=tests ver=0.4.0 -->
- [ ] **SC-104 — The loopback streams prove the check speaks, not that it is right**:
  SC-99 and SC-103 closed the silence half of two checks with origins this repository
  serves to itself, and the other half is still open by construction. `mediatest` writes
  what `internal/media` reads, so the two agree even where both are wrong, and that is
  exactly the failure the remote entries exist to catch. Two specifics are worth a real
  stream. A live LL-HLS endpoint publishing `EXT-X-PART` would settle whether `parts`
  reads a packager that muxes parts and segments separately, which is the disagreement
  the check exists for and which no fixture built from one writer can stage. A real
  packager's `EXT-X-DISCONTINUITY` at a genuine codec change would settle the
  encoding-change exemption — RFC 8216 makes the tag signal a change of file format,
  track layout or codec as well as of timestamps, so the check has to stay quiet there,
  and only media a real encoder produced can show it does. Neither was reachable in
  August 2026; this is a standing invitation to look again, not a design gap.
  <!-- sc: prio=low size=M labels=tests -->
