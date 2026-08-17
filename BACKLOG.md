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

## M4 — Everything that is not the video track <!-- ms: target=v0.3.0 phase=now -->

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

- [ ] **SC-20 — SCTE-35 / `EXT-X-DATERANGE`**: ad-break signalling present in the
  manifest and consistent with the segment boundaries. The check operators
  actually want before a live event.
  <!-- sc: prio=high size=L labels=check,parser -->
- [x] **SC-37 — CEA-608/708 captions**: captions declared in the manifest
  (`CLOSED-CAPTIONS`, DASH `Accessibility`) against caption data actually carried in
  the bitstream — the ATSC A/53 SEI in H.264 and HEVC, in MPEG-TS and in fMP4, and a
  CMAF `c608`/`c708` caption track beside the video, which is how Apple's own
  reference stream delivers it. "The captions are declared but the encoder stopped
  emitting them" is invisible to every manifest checker.
  <!-- sc: prio=high size=L labels=check,parser ver=0.3.0 -->
- [ ] **SC-91 — Attribute a CMAF caption track's field**: a `c608` track states the
  standard, not which CEA-608 field or CEA-708 service it carries, so a channel
  declared against one can be neither confirmed nor disproved. Locating the track's
  samples — `trun` data offsets against the `mdat` — and reading their `cdat`/`cdt2`
  boxes would make the comparison as exact as the SEI path already is.
  <!-- sc: prio=med size=M labels=parser -->
- [ ] **SC-38 — Subtitle renditions**: fetch WebVTT and TTML/IMSC segments,
  check they parse, that cue times are inside the segment window, and that the
  WebVTT `X-TIMESTAMP-MAP` lines up with the media timeline instead of drifting
  away from it. <!-- sc: prio=med size=L labels=check,parser -->
- [ ] **SC-21 — MP3 packed audio**: frame-size tables so the duration can be
  measured, instead of recognising the container and stopping.
  <!-- sc: prio=low size=S labels=parser -->
- [ ] **SC-22 — Encrypted-segment support with a key**: `--key` / `--key-file`
  for AES-128 so the content checks can run on protected streams. The key never
  goes on the command line as a literal.
  <!-- sc: prio=med size=M labels=cli,parser -->

## M5 — Live and delivery <!-- ms: target=v0.4.0 phase=next -->

A live edge and a CDN are the two things a single-shot check cannot see. These
items are what turn segcheck from "check this stream once" into "check this
stream the way a viewer receives it".

- [ ] **SC-25 — Live-edge watch**: `--watch` re-reads the playlist at
  `TARGETDURATION` and reports new-segment latency, stalls, and a live edge that
  stops advancing. <!-- sc: prio=high size=L labels=cli,check -->
- [ ] **SC-39 — LL-HLS parts**: `EXT-X-PART`, `EXT-X-PRELOAD-HINT` and
  `EXT-X-SERVER-CONTROL` — fetch the parts, check part durations against
  `PART-TARGET`, and check that the parts of a segment reconstruct that
  segment's timeline. Low-latency ladders are where continuity defects are born.
  <!-- sc: prio=high size=XL labels=check,parser -->
- [ ] **SC-40 — Multi-period DASH**: continuity across a period boundary, where
  the presentation-time offset resets and an encoder change lands. Today each
  period is checked as if the others did not exist.
  <!-- sc: prio=med size=L labels=check,parser -->
- [ ] **SC-23 — Cache behaviour**: report `X-Cache`/`Age`/`CF-Cache-Status` per
  segment and flag segments served `MISS` that should be warm; a live edge that
  is always MISS is a real origin-load problem.
  <!-- sc: prio=med size=M labels=delivery,check -->
- [ ] **SC-24 — Multi-POP comparison**: run the same check through several
  resolvers or `--header Host:` overrides and report renditions that differ
  between POPs. <!-- sc: prio=med size=L labels=delivery,cli -->
- [ ] **SC-26 — Byte-range support probe**: whether the origin honours `Range`
  at all, reported once per host rather than per segment.
  <!-- sc: prio=low size=S labels=delivery,check -->

## M9 — Wallclock and DVR correctness <!-- ms: target=v0.4.0 phase=next -->

A manifest does not only claim structure, it claims **time in the real world**:
this segment starts at 14:03:22 UTC, this window is sixty seconds deep, this
stream is available now. The media carries the only evidence that can arbitrate
those claims, and nothing in M1–M8 compares the two — every check so far reasons
about a timeline relative to itself.

These are the defects that survive a clean run: the stream is perfectly
continuous, every rung codes what it promises, and a seek still lands in the
wrong place, an ad splices two frames late, or a scrub back into the DVR window
404s. Shares the live-edge machinery with M5, so the two ship together.

- [ ] **SC-51 — `EXT-X-PROGRAM-DATE-TIME` against the media**: the manifest
  claims segment N starts at wallclock T while the media says it starts at PTS
  P. Check that the mapping is monotonic, that it stays consistent from segment
  to segment, and above all that it is **the same across renditions** — a ladder
  whose rungs disagree about PDT makes one seek land in two different places
  depending on which rung the player happens to be on. PDT is parsed today
  (SC-4) and compared against nothing.
  <!-- sc: prio=high size=M labels=check -->
- [ ] **SC-52 — DASH `availabilityStartTime` and `UTCTiming`**: is the segment
  the MPD says is available right now actually fetchable, and is the live edge
  where the clock says it is? Clock skew between packager and client is the
  classic cause of 404s at the live edge that get investigated as CDN faults for
  a day. Requires honouring the `UTCTiming` element rather than assuming the
  local clock, which is itself the thing under test.
  <!-- sc: prio=high size=M labels=check,parser -->
- [ ] **SC-53 — The DVR window is real**: the oldest segment inside
  `timeShiftBufferDepth` — or inside the media playlist's own span — must still
  fetch and still parse. A DVR window that lies is a seek that fails, and it
  only ever shows up when a viewer scrubs back, which is to say in a complaint
  rather than in monitoring. <!-- sc: prio=high size=M labels=check,delivery -->
- [ ] **SC-54 — Discontinuity integrity**: `EXT-X-DISCONTINUITY-SEQUENCE` and
  DASH period boundaries against the timeline resets the media actually
  contains. A declared discontinuity with no reset in the media and a reset with
  nothing declared are opposite bugs that present as the same stall, and the
  `continuity` check currently trusts the declaration.
  <!-- sc: prio=med size=M labels=check -->
- [ ] **SC-55 — Live-edge drift**: over a `--watch` window (SC-25) the edge must
  advance at 1× real time. Report an edge that drifts against the wallclock,
  stalls, or moves backwards — the three shapes of a packager losing its clock,
  none of which a single-shot check can see.
  <!-- sc: prio=med size=M labels=check,cli -->

## M6 — Integration <!-- ms: target=v0.5.0 phase=later -->

- [ ] **SC-27 — Prometheus/OTLP output**: the same findings as metrics, so a
  cron run feeds existing dashboards.
  <!-- sc: prio=med size=M labels=output,integration -->
- [ ] **SC-28 — Slack output** (Block Kit), webhook from the environment, never
  on the command line. <!-- sc: prio=med size=S labels=output,integration -->
- [ ] **SC-41 — Baseline diff**: `--baseline run.json` compares this run against
  a saved one and reports what changed — a rung that lost 30% of its bitrate, a
  resolution that moved, a rendition that disappeared. Turns segcheck into a
  regression gate rather than a snapshot.
  <!-- sc: prio=med size=M labels=cli,output -->
- [ ] **SC-29 — GitHub Action**: a composite action wrapping the binary, so a
  repository can gate on a stream.
  <!-- sc: prio=med size=S labels=integration -->
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

- [ ] **SC-63 — `--profile apple|dash-if|none`**: selects which rule set runs,
  `none` by default. Lands first: a conformance rule with no way to turn it off
  turns a run that was clean yesterday into a wall of findings today, on a
  stream nobody changed. Profiles are opt-in, and each finding names the rule it
  comes from so it can be argued with. <!-- sc: prio=high size=S labels=cli -->
- [ ] **SC-59 — Apple HLS Authoring Spec, the measurable subset**: peak segment
  bitrate within 200% of average, segment durations consistent across the
  ladder, an IDR at every segment start (SC-16), video bitrate within the tier
  its resolution implies, frame rate stable and shared across rungs (SC-17).
  Each rule reports the measured value beside the limit, because "fails rule
  3.4" without a number is unactionable.
  <!-- sc: prio=high size=L labels=check -->
- [ ] **SC-60 — I-frame playlists**: `EXT-X-I-FRAME-STREAM-INF` declares byte
  ranges that must resolve to keyframes and to nothing else, and the I-frame
  rung must span the same timeline as the video it belongs to. A trick-play
  track whose ranges land on non-keyframes is the scrub that shows a grey frame,
  and no manifest reader can see it. <!-- sc: prio=high size=M labels=check,parser -->
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

- [ ] **SC-66 — DRM systems present against declared**: enumerate the `pssh`
  boxes in the initialisation segment by system UUID — Widevine
  `edef8ba9-79d6-4ace-a3c8-27dcd51d21ed`, PlayReady
  `9a04f079-9840-4286-ab92-e65be0885f95`, FairPlay
  `94ce86fb-07ff-4f43-adb8-93d2fa968ca2` — and compare them against what the
  manifest promises (`EXT-X-KEY` `KEYFORMAT`, DASH
  `ContentProtection@schemeIdUri`). A ladder whose MPD advertises PlayReady but
  whose CMAF init carries only a Widevine `pssh` plays on Chrome and dies on
  Xbox and Edge, and the manifest reads perfectly on the way down.
  <!-- sc: prio=high size=M labels=check,parser -->
- [ ] **SC-67 — Encryption scheme**: the scheme in `schm` and the defaults in
  `tenc` — `cenc`, `cbcs`, `cens`, `cbc1` — against the scheme the manifest
  declares. `cbcs` content served as `cenc` plays nowhere, and because the two
  differ by a box field rather than by anything visible, MPDs get copied between
  them. Reported per rendition, since a ladder that mixes schemes is its own
  failure. <!-- sc: prio=high size=M labels=check,parser -->
- [ ] **SC-69 — Clear lead, and media that is not protected at all**: read the
  per-sample encryption state from `senc`/`saiz`/`saio` and report a rendition
  whose samples are in the clear while the manifest declares it protected, plus
  a clear lead longer or shorter than the one that was asked for. This is the
  most expensive defect in the milestone and the quietest: the content ships
  unprotected, every player plays it, nobody files a bug, and the first signal
  is a rights-holder audit. <!-- sc: prio=high size=L labels=check,parser -->
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

- [ ] **SC-72 — Colour description readers**: the VUI in H.264 and HEVC
  (`colour_primaries`, `transfer_characteristics`, `matrix_coefficients`,
  `video_full_range_flag`), and the `colr` box with `nclx` in fMP4, where the
  container states it and no bitstream reader is needed — the same split that
  already applies to resolution. The parser prerequisite for everything below;
  it lands with the `mediatest` writers and the round trip that catches
  bit-level mistakes, and with nothing consuming it yet. Note the trap: the VUI
  sits behind the optional parameter sets the current SPS readers skip past, so
  reaching it means parsing them rather than seeking.
  <!-- sc: prio=high size=L labels=parser -->
- [ ] **SC-73 — `VIDEO-RANGE` against the transfer function**: HLS
  `EXT-X-STREAM-INF` `VIDEO-RANGE=SDR|HLG|PQ` and the DASH
  `SupplementalProperty` / `EssentialProperty` transfer-characteristic
  descriptors, checked against what SC-72 reads: `PQ` is transfer 16, `HLG` is
  18, `SDR` is 1 or 6. `VIDEO-RANGE` is not parsed by the HLS reader at all
  today, so the attribute lands with the check. A PQ rung whose samples are
  BT.709 is tone-mapped twice by every device that believes the manifest and
  once by every device that believes the bitstream, and the two halves of the
  audience see different pictures of the same stream.
  <!-- sc: prio=high size=M labels=check,parser -->
- [ ] **SC-74 — Codec string profile and level** (includes the `av1C`/`vpcC`
  configuration boxes, moved here from SC-42: they carry profile and level, not a
  resolution, so they belong with the codec string rather than with the frame
  size): parse the whole string rather
  than its first component — `avc1.PPCCLL` against `profile_idc`,
  `constraint_set` flags and `level_idc` in the SPS; `hvc1.P.C.LX.B` against the
  profile-tier-level `skipHEVCProfileTierLevel` currently walks past;
  `av01.P.LL.BB` against the AV1 sequence header. Report both directions, since
  they fail differently: a level declared below the media's is a decoder that
  rejects the stream up front, a profile declared above it silently excludes
  devices that could have played it. The comparison must stay honest about
  strings it cannot decompose — an unparseable codec string is an OK-level "not
  verifiable", never a mismatch. <!-- sc: prio=high size=L labels=check,parser -->
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

- [ ] **SC-81 — Audio configuration boxes**: `esds`/`AudioSpecificConfig` for
  `mp4a` (audio object type, sampling frequency index, channel configuration,
  and the SBR/PS extension that changes both), plus `dac3`, `dec3`, `dOps` and
  `dfLa`. The parser prerequisite for the rest of the milestone: it gives an
  fMP4 audio track the same footing video already has, where the container
  states its configuration and no bitstream reader is needed. Lands with the
  `mediatest` writers and nothing consuming it yet.
  <!-- sc: prio=high size=L labels=parser -->
- [ ] **SC-82 — `CHANNELS` against the real channel count**: HLS
  `EXT-X-MEDIA CHANNELS` and DASH `AudioChannelConfiguration@value`, neither of
  which is parsed today, against what SC-81 reads. Distinct from SC-18, which
  compares renditions with each other and with `CODECS`: this is the manifest's
  own channel claim, and it is the one a player acts on before it ever decodes a
  frame. A stereo track advertised as 5.1 makes a receiver select a surround
  output and upmix into it, so the defect is audible on exactly the systems that
  were the reason for shipping surround. <!-- sc: prio=high size=M labels=check,parser -->
- [ ] **SC-83 — Audio codec string against the configuration**: the audio
  counterpart of SC-74 — `mp4a.40.2` against an audio object type that is really
  5 or 29, `ec-3` declared over an `ac-3` sample entry, `mp4a.40.5` over content
  with no SBR at all. Declaring plain AAC-LC over HE-AAC is the classic of the
  set: devices that trust the string decode the base layer only and play the
  whole ladder at half the intended bandwidth's worth of top end, which sounds
  like a bad encode rather than a manifest error. Unparseable strings report
  OK-level "not verifiable", never a mismatch.
  <!-- sc: prio=high size=M labels=check -->
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
- [ ] **SC-65 — The published cask actually installs**: two questions SC-32
  could not answer before a cask existed. (1) The binary is unsigned and
  unnotarized — if Gatekeeper quarantines it, `brew install --cask` succeeds and
  the first run dies with "the developer cannot be verified", which reads as a
  broken build rather than a signing gap. Fix is a `hooks.post.install` running
  `xattr -dr com.apple.quarantine`, or real notarization. (2) Homebrew casks are
  macOS-only, so the migration off the deprecated `brews` silently removed the
  Linux Homebrew path; decide whether that audience is worth carrying a formula
  alongside the cask, or whether the README pointing them at `go install` is
  enough. Close it by installing the first published cask on a clean macOS.
  <!-- sc: prio=med size=S labels=release,docs -->
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
- [ ] **SC-64 — `scripts/backlog.sh` has no tests**: the test-first rule covers
  tooling and this is the one place it was not applied — which is how it shipped
  a generator that split a table row in three the first time an item title
  contained a `|` (SC-63). A shell test fixture with a small BACKLOG, asserting
  the generated ROADMAP for pipes, em dashes, done-versus-open ordering, and the
  lint failures (duplicate id, gap in the sequence, bad metadata) that are
  currently only ever exercised by hand.
  <!-- sc: prio=med size=S labels=tests,project -->
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
