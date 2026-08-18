# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`--watch DUR` observes the live edge** (SC-25). A packager that stopped publishing an
  hour ago serves a flawless playlist: every segment downloads, parses and lines up, and a
  single-shot check has nothing to compare it against. `--watch` re-reads the manifest for
  as long as it is asked to, on the interval the manifest itself implies —
  `TARGETDURATION` in HLS, `minimumUpdatePeriod` in DASH — and reports, per rendition,
  whether the edge advanced at all, how long it ever stood still, and how that compares
  with the interval. `--stall-tolerance` (default 3) is how many intervals of silence are
  a stall rather than jitter. Watching a VOD playlist is not a defect and is reported as
  an OK finding saying there is no live edge, not as a problem in the stream. Only
  manifests are re-fetched: the segments are downloaded once, at the start.
- **A dynamic MPD is checked against its own clock** (SC-52). A DASH live edge is not
  listed, it is computed: `availabilityStartTime` plus arithmetic against "now", and which
  "now" is the whole question. A machine thirty seconds fast asks for segments the packager
  has not made yet and gets 404s that read as a CDN fault, which is a day of investigating
  the wrong system. segcheck now resolves the `UTCTiming` sources the MPD names, in the
  MPD's own fallback order — `http-head` (the `Date` header), `http-iso` and `http-xsdate`
  (the body), and `direct` — corrects for half the round trip, re-expands the segment list
  against that answer, and uses it for the rest of the run. An NTP source is named and
  skipped rather than silently ignored: a zero-dependency binary does not speak NTP, and
  saying so beats falling back to the clock the element exists to distrust.
  The new `availability` check reports the skew between this machine and the MPD's source;
  an unbroken run of missing segments at the live edge, which is the signature of a
  packager behind the availability clock rather than of a CDN losing objects; and — from
  one small ranged probe of the segment the MPD says does not exist yet — a packager
  *ahead* of its own window, where every spec-following player waits for media that is
  already there. An MPD naming no source gets an OK finding saying the verdict is only as
  good as this machine's time.
- `Playlist.AvailabilityStart`, `UTCTiming`, `TimeShiftBufferDepth`, `PresentationDelay`
  and `Rendition.NextSegment` are parsed from the MPD. `NextSegment` is deliberately kept
  out of the segment list: sampling it would report a 404 the MPD itself predicted.
- **The run duration no longer follows the stream's clock.** Adopting a UTCTiming answer
  shifted the clock the elapsed time was measured against, so a run against a packager
  thirty seconds behind reported `-529ms`. Found against a live DASH-IF reference stream.
- **`EXT-X-PROGRAM-DATE-TIME` is compared against the media** (SC-51). It is the only
  thing in an HLS playlist that claims a time in the real world, players seek by it, DVR
  windows are addressed by it and ad decisions are timed against it — and until now it was
  parsed and believed. The new `pdt` check reports a wall clock that goes backwards (two
  moments in the stream then answer to the same time), one that advances at a different
  rate from the media it is stamped on, and — the one the check exists for — rungs of a
  ladder that map the same media to different wall clocks, which makes one seek land in
  two different places depending on which rung the player happened to be on. The
  cross-rendition comparison is on the *offset* between wall clock and media, and only at
  segment indexes where the media already lines up: where it does not, `alignment` owns
  the finding and repeating it would double-count one bug. A jump at a declared
  discontinuity is the packager doing what the specification requires, and is not
  reported.
- **A playlist's wall clock is now carried forward** the way a player carries it. A real
  playlist states `EXT-X-PROGRAM-DATE-TIME` once, at the top, and leaves a client to add
  the declared durations from there; a check that only looked at tagged segments looked at
  one segment per playlist and at *none at all* when sampling the live edge — which is
  where it was found doing nothing, against a real Unified Streaming live ladder. Derived
  times are marked `PDTDerived`, and the carry stops at a discontinuity with no fresh tag:
  past that point the timeline has restarted and the old anchor says nothing, so segcheck
  would be comparing the media against a number it invented. The ad-break check gains the
  same reach, since it anchors `EXT-X-DATERANGE` onto the media through the same tag.
- **`--parts N` checks the low-latency path** (SC-39). Low-latency HLS describes the same
  media twice: as segments, and at a finer grain as the `EXT-X-PART`s published before each
  segment exists. A packager muxes the two separately, so they can disagree — and when they
  do, a viewer on the low-latency path gets different media from one fetching whole
  segments. Nothing that reads only the manifest can see it, and nothing that fetches only
  the segments can either. The new `parts` check fetches a segment's parts and reports:
  parts that are not contiguous with each other, parts that do not cover the segment they
  make up, a part declared `INDEPENDENT=YES` whose media does not open on a sync sample
  (a player invited to join there has nothing to decode), a part longer than the measured
  `PART-TARGET`, and a part that will not fetch or parse. A `GAP=YES` part is the packager
  declaring the hole and is never reported. Default `--parts 1`; `--parts 0` switches it off,
  and a stream with no parts never gains a row.
- `EXT-X-PART-INF`, `EXT-X-SERVER-CONTROL`, `EXT-X-PART` and `EXT-X-PRELOAD-HINT` are parsed
  (`Playlist.PartTarget`, `PartHoldBack`, `CanBlockReload`, `PendingParts`, `PreloadHint`,
  `Segment.Parts`). A part's `BYTERANGE` follows the same "continue from the previous range"
  rule as `EXT-X-BYTERANGE`, on its own chain — every part of a segment is usually a range
  of the same growing file. The parts still pending at the end of a playlist belong to the
  segment being published right now, and are kept apart from the completed ones so that
  segment's media is not counted twice. The preload hint is parsed and deliberately never
  fetched: it is designed to block until the media exists, and a checker that blocked on one
  would hang rather than report.
- `manifest.Playlist.UpdatePeriod` carries DASH `@minimumUpdatePeriod`, which is the MPD
  stating how often it will change and therefore how often `--watch` has to look.

### Fixed

- **A live HLS ladder was reported as VOD.** `EXT-X-ENDLIST` is a media-playlist tag, so
  a master playlist carries no liveness signal at all and every live HLS stream parsed as
  VOD until its variants were loaded. The report said "HLS VOD" about a live stream, and
  `--watch` dismissed a live ladder as having no edge to watch. Liveness is now taken from
  the variants, and the manifest line is written after they load. Found by pointing the
  new `--watch` at a public live stream, not by a unit test.
- **One rendition had two names in one report.** `rendLabel` took the sampled rendition,
  so the watch loop — which re-reads the manifest and never samples — spelled an audio rung
  `150kbps` where every other check called it `audio 150kbps`. It takes the manifest
  rendition now, and there is one vocabulary again.
- **A stalled DASH live edge was measured and then declared unjudgeable.** A DASH
  representation has no `TARGETDURATION` and most MPDs state no `@minimumUpdatePeriod`,
  but every MPD states how long its segments are — which is the manifest saying how often
  a new one should appear. `--watch` now judges against that, and reserves "no interval to
  compare against" for a manifest that really states none.
- The Homebrew cask's `postflight` used `exit_status == 0`, which fails `brew style`'s
  `Style/NumericPredicate` and so fails the tap's own audit. It is `.exit_status.zero?`
  now. The cask is generated from `.goreleaser.yaml`, so correcting it in the tap by hand
  lasts only until the next release overwrites it. Ten further offences in the tap's audit
  are goreleaser's own stanza ordering, on `segcheck.rb` and `checkfleet.rb` alike, and are
  SC-98 — fixable only in the tap.

## [0.3.0] - 2026-08-18

The release M4 was for: everything in a stream that is not the video track. Four new
checks — `audio`, `captions`, `adbreak` and `subtitles` — readers for MPEG audio, WebVTT
and TTML, the ATSC A/53 caption SEI, SCTE-35 splice sections and `emsg` boxes, AES-128
decryption so the content checks run on a protected stream at all, and the machinery to
locate a fragment's samples so what is inside them can be read.

The through-line is that a number a manifest states and a number a container states are
often both right about different things. AC-3 writes 2 into a field describing a 5.1
programme; `CHANNELS="16/JOC"` counts a rendered Atmos presentation over a 5.1 bed;
HE-AAC codes at half the rate it plays and HE-AAC v2 codes a mono core it renders as
stereo; a TTML document in an `stpp` sample counts on the presentation timeline, not from
the fragment carrying it; `X-TIMESTAMP-MAP` anchors WebVTT in HLS and does not exist in
DASH. Every one of those produced a BAD on a public reference stream before it was
understood, and every one of them was found by running the binary rather than by a unit
test.

The other through-line is the difference between *no* and *unknown*. Partial encryption is
the sharpest case: with AES-128 nothing parses and every check honestly says it could not
look, but with SAMPLE-AES or CENC the container parses, the timing checks pass, and the
bitstream readers succeed and find nothing — so a caption scan over ciphertext reported
"scanned, no captions" and turned a manifest correctly declaring CC1 into an accessibility
failure. Samples nobody could read, cues nobody could place and a key that does not
decrypt are now all reported as limits of this tool rather than as defects in the stream.

Thirteen distinct false findings against correct media were fixed in the course of it, ten
of them found only by pointing the binary at a real stream rather than by any test.

### Added

- **`audio` check — what the audio actually is, against what the manifest says
  it is** (SC-18). A player configures its decoder and its output device from the
  manifest before it has fetched a byte of media, so a rendition that plays at a
  rate or a channel layout other than the one declared is a pitch shift or a
  silent surround channel — and nothing manifest-only can see it. The check
  reports a BAD when the media contradicts HLS `CHANNELS`, DASH
  `@audioSamplingRate` or `AudioChannelConfiguration`, and when the format changes
  part-way through a rendition, which no manifest states at all because a manifest
  states one value for the whole thing.
- **The `audio` check compares the codec too** (SC-90). An `mp4a` track declared
  `ec-3` is silence on a device with no E-AC-3 decoder that would have played the
  AAC happily, because `CODECS` is what a player checks before it commits. A
  `CODECS` value naming two audio codecs — which no single rendition can honour —
  or none at all states nothing to compare, and is not treated as a claim.
- **`captions` check — the caption data that is really in the bitstream** (SC-37).
  A manifest declares CC1, the encoder stops emitting it, and nothing in the
  manifest changes: no manifest-level checker will ever notice, and in several
  countries the obligation is legal rather than editorial. The check reads the
  captions from every place they are actually carried — the ATSC A/53 SEI in H.264
  and HEVC, in MPEG-TS and in fMP4's length-prefixed samples, and a CMAF
  `c608`/`c708` caption track beside the video, which is how Apple's own reference
  stream delivers CEA-608 — and compares it against HLS `CLOSED-CAPTIONS` and DASH
  `Accessibility`.

  What it reports is bounded by what can be known. CC1 and CC3 share CEA-608
  field 1, and separating them needs the line-21 control codes decoded, so only an
  *empty* field is a defect: a channel declared over a populated one is not
  reported either way. CEA-708 names its services in the DTVCC packet layer, so a
  declared service that is genuinely absent is a defect the reader can be sure of.
  A caption track states its standard and no more (SC-91). And a bitstream nobody
  could walk gets an ERROR saying the coverage has a hole, never a BAD.

  `CLOSED-CAPTIONS=NONE` over a bitstream that carries captions is a WARN: a player
  believes the manifest, so the toggle is never offered. An absent attribute is not
  the same claim, and is not treated as one.
- **`adbreak` check — can a player actually cut to the break?** (SC-20). Whether
  the break is *signalled* is a manifest question; whether it can be *taken* is not.
  A splice point that does not land on a segment boundary is a break nobody can cut
  to: the ad server fires and the transition lands mid-picture, or the switch never
  happens — and the manifest describes it perfectly either way.

  SCTE-35 is read where it really lives: a `splice_info_section` on an MPEG-TS PID
  of stream type 0x86, carried as a private section rather than a PES, and a DASH
  `emsg` box, whose binary scheme carries the same section verbatim. `pts_adjustment`
  is added as the standard requires, so a section a downstream splicer shifted is
  placed where it actually happens. Verified against livesim2's live SCTE-35 stream.

  On the manifest side: `EXT-X-DATERANGE` with an `SCTE35-OUT`/`IN`/`CMD` attribute,
  `EXT-X-CUE-OUT`/`CUE-IN` in both duration forms packagers write, and DASH
  `EventStream` under either SCTE scheme. A `DATERANGE` without an SCTE35 attribute
  is a chapter or a programme boundary, not a break, and is not treated as one.

  Two things are deliberately not flagged. A splice that states no time —
  `splice_immediate`, or a `splice_time` with its flag clear — means "now" and has
  nothing to be compared against; judging it against the zero value would call every
  one of them perfectly aligned. And inband signalling with nothing in the manifest
  is reported, not flagged: server-side insertion downstream is a legitimate design,
  and segcheck cannot tell it from a packager that forgot to translate the cue.
- **`subtitles` check, and readers for WebVTT and TTML/IMSC** (SC-38). A subtitle
  rendition rarely fails by being malformed. It fails by being perfectly valid and
  pointing somewhere else: the cues parse, the segments are the right size, the
  manifest is impeccable, and the subtitles are hours from the picture because
  `X-TIMESTAMP-MAP` was written from the wrong clock. The manifest says nothing about
  where the cues are, so nothing that reads only the manifest can tell.

  The comparison is *overlap*, not containment — a cue continuing across a boundary
  appears in both segments and overhangs one of them at each end, so demanding
  containment would flag correct media. And it is anchored to the media timeline the
  video states rather than to accumulated `EXTINF`: the cues count on the media
  clock, which begins wherever the video's first timestamp is, and Apple's advanced
  example starts its video ten seconds in.

  Without `X-TIMESTAMP-MAP` there is nothing to anchor the cue clock to, and that is
  a WARN saying so rather than a guess in either direction. A rendition whose every
  readable segment is empty is a WARN with the count — a gap in the dialogue is
  legitimate, so it is not proof. Subtitle renditions are sampled with a new
  `--subtitles N` (default 1); they were previously not sampled at all.
- **AES-128 decryption, so the content checks can run on a protected stream**
  (SC-22). Every check in this tool reads the media, so on an encrypted stream every
  one of them is blind and the honest report was "segcheck could not look". Given the
  key it can look, and the point is that the *content* checks then run — not that
  decryption happened.

  The key is given by name, never as a value: `--key-file PATH` or `--key-env NAME`,
  accepting sixteen raw bytes or their hex spelling. A key in `argv` lands in shell
  history, in the process list and in every CI log that echoes its own invocation,
  and unlike a password it cannot be rotated without re-encrypting the content. The
  error for an unreadable key names the flag and never quotes what it read.
  `--fetch-keys` takes the key from the URI `EXT-X-KEY` names, and is off by default
  because pointing a checker at a key server is a request to a system that logs,
  rate-limits and sometimes bills.

  The initialisation vector is the trap. `EXT-X-KEY` need not state one, and when it
  does not the IV is the segment's media sequence number as a 128-bit big-endian
  value — so a decrypter defaulting to zeroes produces noise on the large share of
  streams that omit the attribute, and noise is indistinguishable from a wrong key.
  A key that does not decrypt is reported as an ERROR about the *key*, not as
  unreadable media: it points at the right thing instead of sending an operator
  hunting a defect in a healthy stream.

  Verified against a synthetic origin whose plaintext is known by construction —
  which is the only way to tell a working decrypter from one producing plausible
  noise — but *not* against a real encrypted stream, because no public AES-128 test
  stream was reachable. SC-96 tracks that, and it matters: every other reader in this
  project had a design error that only a real stream found.
- **HE-AAC v2's mono core is not a channel-count mismatch.** Parametric Stereo codes a
  mono core that the decoder renders as stereo, so an `mp4a.40.29` representation
  declaring `AudioChannelConfiguration value="1"` over stereo media is right about the
  core while the sample entry is right about the output — and which of the two each side
  states is not something a checker can adjudicate. A real DASH vector does exactly that,
  and calling it a defect was the same mistake as calling HE-AAC's half sampling rate one.
  The allowance is that profile and that pair only: 5.1 declared as mono is still wrong
  however the audio is coded.
- **Decryption and the splice descriptors are checked against an authority outside this
  repository** (SC-96). Every other assertion about them round-trips through this
  project's own builders, and a builder and a reader written from the same misreading
  agree with each other perfectly. The usual answer is a real stream; none is available —
  no public AES-128 HLS test stream is reachable across every provider that publishes one,
  and no reachable stream carries a `segmentation_descriptor` either.

  So the cipher layer is now checked against RFC 3602's published AES-CBC vectors — third
  -party plaintext and third-party ciphertext, pinning the mode, the key schedule and the
  IV chaining across block boundaries. The key-length check moved into that layer so it
  has one spelling and is reachable from both callers. And the segmentation descriptor is
  checked against bytes laid out by hand from the specification with the bit boundaries
  written down, including the byte where three flags and five reserved bits share space —
  getting that one wrong is what moves the type id.
- **Three false findings on real protected and DASH streams**, all found by going looking
  for one (SC-96):
  - **Protected single-file DASH reported "encrypted but the manifest declares no key"**
    against a manifest that declares it three times. A `SegmentBase` representation's
    segments are synthesised from its index rather than parsed, so the protection had
    nowhere to come from; it now travels on the rendition and is stamped on as they are
    built.
  - **A sidecar subtitle is one file that *is* the subtitle**, not an ISO-BMFF container
    with an index. A `text/vtt` or `application/ttml+xml` representation with only a
    `BaseURL` was sent through the on-demand index probe and reported "no segment index
    found" — an ERROR claiming the tool could not look at a file it could read whole. It
    now becomes one segment covering the period, and a real stream's 78 cues are read.
  - **`X-TIMESTAMP-MAP` is an HLS mechanism.** DASH does not use the tag and puts WebVTT
    cue times on the presentation timeline directly, so warning about its absence there
    was a warning about a tag the format does not have. The local times are now always
    kept, and whether anything anchors them is recorded separately.
- **SAMPLE-AES and CENC no longer make the bitstream checks lie** (SC-95). Full-segment
  AES-128 is the honest kind of blindness: nothing parses and every check says it could
  not look. Partial encryption is the dangerous kind — the container parses, the timing
  checks work perfectly and read as a clean bill of health, and the bitstream readers
  *succeed and find nothing*.

  Two false BADs came out of that, both against media that is entirely correct: a caption
  scan over ciphertext reported "scanned, no captions" and turned a manifest correctly
  declaring CC1 into an accessibility failure, and a keyframe walk reported "no random
  access point" for the same reason. Both now report that nobody could look.

  The CENC scheme is read from `sinf`/`schm` (`cenc`, `cbcs`, `cens`, `cbc1`) and
  SAMPLE-AES from the manifest, and the `encryption` check says which half of the tool
  ran. The distinction is drawn per container, not blanket: in fMP4 the resolution is in
  the sample entry and the sync flag in the `trun`, both in the clear, so those checks
  keep working — it is MPEG-TS where the elementary stream is the only source and a
  verdict drawn from it is worthless once the samples are encrypted.

  Verified against a real CENC stream (`media.axprod.net` v7-MultiDRM-SingleKey), which
  produces exactly the findings its clear twin does apart from the encryption notes. It
  joins the smoke suite as a sixth baseline — the first protected one.
- **A wrapped subtitle rendition gets the same drift check a text one does** (SC-97).
  SC-93 counted the cues inside a `stpp` sample; now they are placed. A document that is
  internally perfect and pointing somewhere else fails the way a WebVTT one with a bad
  `X-TIMESTAMP-MAP` does.

  The backlog item said a TTML document's times are relative to the fragment carrying it.
  They are not — ISO/IEC 14496-30 puts them on the presentation timeline, and livesim's
  DASH subtitles are written that way. The first draft added the fragment's `tfdt` on top
  and reported every one of that stream's correct segments as four seconds adrift; the
  real stream is what corrected it, for the fifth time in this project. A `wvtt` sample
  times its cue by the sample's own duration and states no span at all, so there is
  nothing to compare and the check says so rather than reporting the fragment's window as
  the cues'.
- **SCTE-35 says what kind of break it is, and the manifest's own copy is checked**
  (SC-92). A `splice_info_section` says *when*; its `segmentation_descriptor` says
  *what* — a provider advertisement, a distributor placement opportunity, a programme
  boundary — and an operator chasing an ad-insertion problem needs the second as much
  as the first. Almost every field in that descriptor is optional and shifts the ones
  after it, so the flags are read rather than assumed: a reader taking the delivery
  restrictions to be absent names whatever byte happens to sit where the type id goes.
  A type id the table does not hold is reported as its number, which is more use than
  a wrong word.

  `SCTE35-OUT`/`IN`/`CMD` carry the same section as hexadecimal, so the manifest's
  account of a break and the media's are now compared by event id rather than only
  their timings — a packager that rewrote one and not the other is what that catches.
  A value that is not a whole section decodes to nothing rather than to half a header.

  Verified against livesim2's real section for the command path. Its descriptor loop is
  empty, so the descriptor reader is asserted only against built sections; SC-96 now
  covers that gap alongside the encrypted-stream one.
- **A fragment's samples are located, so what is inside them can be read** (SC-91,
  SC-93). A track's samples are in no header: the `tfhd` names a base — in practice the
  enclosing `moof` — and each `trun` states an offset from it followed by the sizes of
  the samples that begin there. The data offset is signed, because a fragment may place
  its samples before the box that describes them.

  Two readers were waiting on that. A CMAF `c608` caption track's `cdat` and `cdt2`
  boxes say which CEA-608 field the data is on, so Apple's own fMP4 reference stream now
  reports "CEA-608 field 1 (CC1/CC3)" where it said the channel was not attributable
  (SC-91). And a `stpp` sample is a TTML document while a `wvtt` sample is a sequence of
  cue boxes — with `vtte` saying nothing is displayed rather than being a cue — so a
  CMAF subtitle rendition's cues are counted rather than guessed at from the sample
  count (SC-93). A rendition whose segments are the right size and carry nothing is the
  usual shape of a broken subtitle pipeline, and the sample count alone could not tell
  it from a working one.

  Samples nobody could read are still distinguished from samples holding nothing: they
  lead to opposite verdicts, and the check reports the first as a limit of this tool
  rather than as a rendition that says nothing. SC-97 tracks timing a wrapped cue as
  well as counting it.
- **Packed MP3 renditions are measured, not skipped** (SC-21). Recognising the format
  and stopping was honest, but it left the duration check with nothing to compare
  against, so a rendition declaring six seconds a segment and shipping four went
  unreported. A frame's length follows from its version, layer, bitrate index and
  sampling rate together — and MPEG-2 Layer III halves the samples per frame, which a
  reader that missed it would report at twice the real duration. A header stating a
  reserved or free-format field is refused rather than guessed at: a length computed
  from one walks into the next frame.
- **Audio format read from where each container actually states it** (SC-18): the
  `AudioSampleEntry` in fMP4, the ADTS header in MPEG-TS and in packed audio, and
  the `dac3`/`dec3` box for AC-3 and E-AC-3. Muxed audio inside a video variant is
  read too, which is how most transport-stream ladders are delivered.

### Fixed

- **The Homebrew cask installed a binary macOS would not run** (SC-65). Homebrew
  Cask stamps `com.apple.quarantine` on what it stages, and the darwin builds carry
  the ad-hoc signature the Go linker emits rather than a Developer ID one, so
  Gatekeeper rejected them. `brew install --cask` reported success and then
  `segcheck --version` died on SIGKILL — exit 137, no dialog, no message, nothing.
  Every macOS install since the tap went live in 0.1.1 was affected, and the silence
  is the worst part: it reads as a broken build, not a signing gap. The cask now
  strips the attribute in a `postflight`. Two details it depends on:
  `/usr/bin/xattr` by absolute path, because a Python `xattr` earlier on `PATH`
  rejects `-r`, and `-dr` rather than `-d`, because only `-dr` exits 0 when the
  attribute is already absent. Signing and notarising properly — which also fixes
  the archives on the releases page, still unsigned — is SC-94.
- Three things real reference streams caught that the unit tests could not, all of
  the same shape — a number that is not the number it looks like:
  - **AC-3 and E-AC-3 misstate their own channel count.** The `AudioSampleEntry`
    `channelcount` field reads 2 on Apple's 5.1 reference track; the layout is in
    the `dac3`/`dec3` box. Trusting the field reported every surround AC-3
    rendition as stereo. With dependent substreams present the count now stays
    unknown rather than reporting a 7.1 programme as 5.1.
  - **`CHANNELS="16/JOC"` is not sixteen channels.** Once a spatial-coding
    identifier follows the count, the count describes a rendered presentation, not
    the coded bed — Dolby Atmos ships 16/JOC over 5.1 — so it is no longer treated
    as a comparable claim.
  - **HE-AAC codes at half the rate it plays.** SBR rebuilds the top octave, so a
    `mp4a.40.5` track whose sample entry says 24 kHz outputs the 48 kHz DASH
    declares. Exactly a doubling is now expected for the SBR profiles, and only
    for them.
- **A DASH `AdaptationSet` may leave `mimeType` to its Representations** (SC-89),
  and the DASH-IF `MultiResMPEG2` test case does. Every one of its four video rungs
  read as audio, so `ladder` reported a BAD "no video rendition in the manifest" on
  a perfectly good stream and `resolution`, `framerate` and `keyframe` went silent
  on the whole of it. `dashKind` now falls back to the first Representation's
  `mimeType`, `codecs` and frame size, and the stream joins the smoke suite as a
  fifth baseline so the silence cannot come back unnoticed.
- **`EXT-X-KEY`'s URI was stored unresolved**, so it was unfetchable from anywhere
  but the playlist's own directory. Every other URI in the parser was already
  resolved against the playlist.
- **`continuity`, `duration` and `keyframe` spoke nonsense about subtitles.** A
  subtitle track's timestamps are the span its cues cover, not the extent of the
  segment: cues do not fill a segment, and one crossing a boundary appears in both.
  Read as a segment extent they produced six BAD gap-and-overlap findings and a
  26% duration mismatch on Apple's own reference stream. All three now leave text
  renditions to the check that reads their timestamps for what they are.
- **The CI fuzz targets are discovered, not listed.** The hardcoded list meant a new
  parser was never fuzzed and nothing said so — the same silent coverage hole this
  project treats as worse than a failure. WebVTT and TTML, being text straight off
  the network, got targets of their own in the same commit.
- **A splice information PID is signalling, not media.** Many packagers include it
  only in the segments that carry a cue, so the `tracks` check counted its
  appearance as a mid-rendition track change and warned about a decoder reset on
  every ad break in an otherwise healthy stream. `id3` was in the same position.
- **A panic on a truncated `stsd`**, found by the fuzzer: the sample-entry walk
  sliced past the end of a box shorter than its own header. The audio hook added in
  SC-18 had the same unguarded slice.
- A data race in `TestSampleAll_ByteRangeSegmentsSendTheRangeHeader`, which
  appended to a shared slice from concurrent HTTP handlers and failed under
  `-race` about one run in ten.

## [0.2.0] - 2026-08-17

The release M3 was for: the rungs segcheck read least well. Two new checks —
`keyframe` and `framerate` — two new readers, HEVC parameter sets and the `sidx`
of a single-file DASH representation, and the tooling that keeps the rest honest.

The through-line is silence. A check that cannot look reports nothing, and nothing
reads exactly like a clean bill of health: an HEVC rung had no resolution to
compare, a single-file DASH representation was marked unsupported and skipped
whole, an encrypted track reported its codec as `encv`. Each of those was a rung
the tool was quietly not checking.

The other through-line is that **the reference streams, not the unit tests, found
every design error here** — five of them. A keyframe rule that flagged Apple's own
bipbop; a NAL walk that stopped before the keyframe on 1080p; a DASH profile that
states no `SegmentBase` at all; a hierarchical index whose top level references
only other indexes; `trex` defaults without which every duration read as zero.
Unit tests assert what their author thought of. So that step is now a test of its
own (SC-36), the parsers are fuzzed (SC-35), and coverage ratchets rather than
merely being printed (SC-48).

### Added

- **AV1, VP9 and VP8 are asserted, not assumed** (SC-42). The item assumed a
  parser was missing; checking first showed it was not. Every fMP4 codec takes its
  resolution from the visual sample entry rather than from a bitstream reader —
  `av1C` and `vpcC` carry profile and level, not a frame size — so AV1 and VP9
  already worked end to end. What was missing was the test: the whole of it rested
  on the sample entry type appearing in one list, and a codec absent from that list
  reports no resolution, leaving the rung skipped in a silence indistinguishable
  from a pass. That list is now a stated contract, mutation-verified. Reading
  `av1C`/`vpcC` for profile and level is a real gap and has moved to SC-74, where
  the codec string already is.

- **The reference streams are a test now, not a habit** (SC-36). Every false
  positive this project has shipped was found by running against a real stream
  rather than by a unit test — five of them in this release alone — and the step
  was manual, which is the kind of step that gets skipped on the day it would have
  mattered. `internal/analyze/smoke_test.go`, behind a `smoke` build tag, runs the
  built binary against Apple's fMP4 and MPEG-TS references, a DASH
  `SegmentTemplate` manifest and a single-file on-demand one. The release workflow
  depends on it; CI runs it on pushes to main but not on pull requests, so a flaky
  CDN never blocks a contributor.
  It does not assert "nothing above OK": Apple's advanced example legitimately
  over-declares BANDWIDTH and ships an inverted ladder. Each stream carries a
  baseline of the checks allowed to exceed OK, with the reason, plus a list of
  checks that must not fall silent — and that second half is what catches a parser
  which quietly stopped reading, since silence reads exactly like a pass.

- **Fuzz targets for every parser** (SC-35): TS, MP4, `sidx`, packed audio, the
  H.264/HEVC parameter sets, and `Parse` itself for the container detection in
  front of them. The seed corpus is built from `mediatest` rather than checked in —
  no binary fixture enters this repository — so `go test` runs the seeds on every
  build and the targets double as a regression suite without anyone opting into
  fuzzing. CI fuzzes each target for 60s.
  The property asserted is not only "does not panic" but "when it claims success,
  what it returns is self-consistent". That second half is what mattered: all three
  defects below were found by it, and a panic-only target would have passed every
  one of them.

- **Single-file DASH is checked instead of skipped** (SC-19). A `SegmentBase` or
  on-demand representation used to come back marked unsupported — honest, and
  useless: one line saying so, and every other check skipped for the whole
  rendition. It is sampled now. `ParseDASH` does no I/O, so the work is split
  three ways: the manifest states which bytes hold the index, `internal/media`
  reads it, and the analysis fetches the range.
  Two shapes exist and only the second is common. `SegmentBase@indexRange` says
  where the index is; the on-demand profile says nothing but a `BaseURL`, so the
  index has to be found by reading the head of the file. Real streams then forced
  two further corrections: the index is often **hierarchical**, a root `sidx`
  whose every reference points at a leaf `sidx`, so a reader that stops at the top
  finds no media at all; and the initialisation segment is the bytes before the
  index, without which the fragments parse with no timescale. Sony's DASH-IF
  vector went from 3 ERROR and 0 segments sampled to 25 checks, all OK.

- **`framerate`: the rate the pictures actually run at** (SC-17). Measured from
  the median gap between presentation timestamps — the median because with
  B-frames the stream is not in presentation order, and because one discontinuity
  inside a segment would drag a mean off by however large the jump was — and asked
  two questions. Against the manifest, since `FRAME-RATE` / `@frameRate` is what a
  player consults to decide what it can decode before downloading anything: a
  1080p60 rung declared as 30 gets selected by a device that can only manage 30,
  and then stutters. And across the ladder, since rungs at unrelated rates make
  every switch visibly uneven — with an exact integer relation left alone, because
  halving the rate on the lower rungs is an ordinary way to save bitrate and
  reporting it would flag a technique in wide use. The 2% tolerance absorbs the
  NTSC rates, where a manifest writes 29.97 for media running at 30000/1001.
  Verified against Apple fMP4, Apple MPEG-TS and a public DASH manifest with zero
  findings above OK.

- **`keyframe`: every segment must carry a random access point** (SC-16). A segment
  with none cannot be switched into at all — a decoder arriving mid-stream has no
  reference picture, so the switch shows nothing until the next keyframe. This is
  the defect behind "ABR switching stutters even though the boundaries line up":
  `alignment` passes, every duration is correct, the ladder is flawless, and
  switching is broken for everyone. No manifest-level checker can see it.
  Read three ways, because the containers state it in three places: the first coded
  slice's `nal_unit_type` for H.264, the whole IRAP range 16–21 for HEVC — a reader
  recognising only `IDR_W_RADL` would call a switchable `CRA_NUT`-opening segment
  broken, and CRA is what some live encoders emit for every segment — and
  `sample_is_non_sync_sample` for fMP4, from trun's first-sample-flags, else its
  per-sample flags, else the tfhd default.
  Two things were learned from the reference streams rather than reasoned out, and
  both changed the design. A stricter first draft treated "does not *open* on a
  keyframe" as the defect, and reported Apple's bipbop three times over: its
  segments are byte ranges of one `main.ts`, so a boundary falls on a transport
  packet and a segment can carry the tail of the previous picture ahead of its own
  IDR. Players start at the IDR and it plays everywhere, so that case is now an
  OK-level note with a count, and the BAD is reserved for no keyframe at all. Then
  the larger rungs still read as having none, because the shared NAL walk stops
  after 64 units and a 1080p picture split across dozens of slices pushes the IDR
  past it. The keyframe walk now has its own cap and reports whether the cap or the
  data ended it; hitting the cap, or the 1 MiB elementary-stream capture limit,
  means absence was never established rather than proven. Verified against Apple
  fMP4, Apple MPEG-TS and a public DASH manifest with zero findings above OK.

- **HEVC/H.265 coded resolution** (SC-15): an HEVC rung in an MPEG-TS segment
  used to report its codec and no resolution, so the `resolution` check had
  nothing to compare and said nothing — a silence indistinguishable from a rung
  that passed. `internal/media/hevc.go` reads the parameter set out of the
  elementary stream: the two-byte NAL header, `profile_tier_level` measured
  exactly (its length depends on the declared sub-layer count, and a reader that
  mismeasures it returns a plausible wrong resolution instead of failing), and
  the conformance window applied with the SubWidthC/SubHeightC unit for the
  chroma format. The TS reader now dispatches on stream type rather than trying
  both parsers, because an HEVC stream read as H.264 can find something
  SPS-shaped and answer confidently. fMP4 already stated the resolution in its
  visual sample entry and needed no reader, but `hvc1` gained a test rather than
  staying an assumption.

- **Tests for the untested helpers behind the findings** (SC-71): total coverage
  70.7% → 76.2% (`internal/media` 74.1 → 84.3, `internal/manifest` 79.4 → 87.9,
  `internal/analyze` 85.5 → 89.5, `internal/finding` 83.3 → 94.4). An audit for
  functions no test ever called, prioritising the ones whose failure mode is a
  wrong finding rather than a crash. `parseTrun` was the worst at 38.8%: its
  per-sample fields are an optional bitmap and its composition-time offset is
  unsigned in a version 0 box but signed in a version 1 one, so reading version 1
  as unsigned moves a segment's start about 13 hours and reports a gap that does
  not exist. `media.Timeline` — the promise that a cross-segment check never
  compares a video start against an audio start — had no test at all.
  `declaredCodec` gained a contract test asserting every name it returns is one a
  parser actually produces, since the two tables sit in different packages and
  are compared by string. `describeCounts` is asserted stable over 200 runs
  because Go randomises the map iteration its output depends on. Also
  `firstTemplate`'s three-level DASH inheritance, `dashKind`, `dashName`,
  `streamInfKind`, both codec tables and the severity order. Verified as SC-57
  and SC-58 were: 60 mutations, 59 caught, the one survivor provably a no-op. The
  audit changed production code exactly once, in the `encv` case below; the
  `mediatest` SPS writer also gained `pic_order_cnt_type` 1 and the 4:4:4 scaling
  list count, so the reader is now asserted against the two remaining places
  where the fields before the resolution change length.

- **Coverage to the practical ceiling, and a CI gate that holds it** (SC-78):
  99.64% of statements, up from a true baseline of 90.94%. The figures reported
  before this were wrong in two ways: `go test -cover ./...` credits a package
  only for its own tests, so `internal/media/mediatest` read 0.0% although every
  parser test runs through it, and a `-coverpkg=./...` profile carries one copy
  of each block per test binary which `go tool cover -func` sums rather than
  merges. `scripts/coverage.sh` (POSIX sh and awk, like the rest of `scripts/`)
  merges by block position and fails below 99%; `-count=1` is mandatory because a
  cached package result carries stale line numbers. Newly covered: every
  remaining `ParseTS` branch, the ISO-BMFF box plumbing including 64-bit and
  size-0 boxes and every `tfhd`/`trun` flag combination, the H.264 chroma formats
  and both variable-length blocks before the resolution, the ADTS and ID3 walks,
  DASH `SegmentList` and open-ended `@r`, HLS `EXT-X-MEDIA` and implicit byte
  ranges, and every `(value, false)` guard where a check must stay silent rather
  than report a measurement it could not take. `main` is covered by re-executing
  the test binary as a subprocess, the only way to assert an exit code reaches
  the shell. Nine statements remain uncovered because they are unreachable by
  construction — a three-bit field compared against 7, a constant 4 compared
  against 188, an index clamp whose step is always greater than one — and are
  listed with their reasons in `BACKLOG.md`; they stay as guards rather than
  being deleted to round the number up.

- **The coverage gate is a ratchet now, not a floor** (SC-48): `scripts/coverage.sh
  check` compares against the figure in `scripts/coverage.floor` and fails when a
  commit lowers it. SC-78 shipped only half of this, and the difference is the
  whole point of the item: a hard-coded 99% would have let coverage slide from
  99.64% to 99.01% without a word. The floor is committed rather than written into
  the workflow, so moving it is a reviewable diff — the same arrangement as
  `ROADMAP.md` being generated but committed. It only goes up: a drop fails,
  `coverage.sh update` refuses to record anything below the current floor so a
  regression cannot be laundered into the baseline, and a gain beyond a
  0.25-point tolerance fails asking to be locked in, because a ratchet that never
  tightens leaves slack for a real loss to hide in. Deciding is separated from
  measuring — `COVERAGE_ACTUAL` injects a figure — so `scripts/coverage_test.sh`
  asserts the whole decision table without running the suite, including a floor
  file containing rubbish, which must not read as zero and pass everything.

- **GitHub issues generated from the backlog** (SC-80): the plan was only visible
  to someone reading `BACKLOG.md` or `ROADMAP.md` in the repository — the issue
  tab was empty, and had been since the project started. `scripts/backlog.sh
  issues` now syncs it, one way only: backlog to issues, never the reverse,
  because the `SC-n` id is what commits and this file reference and a title edited
  on GitHub must not be able to move it. Planning is separate from doing —
  `issues` prints a plan and changes nothing, `--apply` executes it — which is
  what makes the decisions testable: `scripts/backlog_issues_test.sh` asserts them
  against a fixture backlog with no network call, covering every state (create,
  reopen, close, leave alone, and never open an issue for shipped work),
  idempotence on a settled backlog, the milestone filter, and a malformed backlog
  stopping the sync instead of planning against half of it. The script also
  creates the label vocabulary and milestones it needs, so a fork does not fail on
  its first apply. `.github/workflows/backlog-issues.yml` runs it on a push that
  touches the backlog or the script, under a concurrency group so two runs cannot
  both open the same issue, and CI runs the planner's tests.

- **M11 — Content protection, in depth** (SC-66 … SC-70), targeted at v0.6.0.
  The `encryption` check shipped in v0.1.0 answers whether segments are
  protected when the manifest says so, and stops there; this milestone is the
  level below, where DRM launches actually break. Which system (`pssh` per
  Widevine / PlayReady / FairPlay UUID against `KEYFORMAT` and
  `ContentProtection@schemeIdUri`), which scheme (`cenc` versus `cbcs` from
  `schm`/`tenc`), whether the media is protected at all (`senc`/`saiz`/`saio`
  per-sample state, and the clear lead's real length), whether declared key
  rotation is actually applied, and whether an HLS `METHOD` matches how the
  payload is really protected. **None of it needs a key** — reading protection
  metadata is not decrypting, which is what makes these checks runnable in CI
  against production content. `--key` (SC-22) stays the separate, opt-in case of
  running *content* checks on protected media.

- **M12 — Colour, HDR and the codec string** (SC-72 … SC-77), targeted at v0.7.0.
  Today `declaredCodec` reduces `avc1.640028` to `"h264"` and asks only whether
  the media is H.264 too — everything after the first dot is discarded — and
  `VIDEO-RANGE` is not parsed at all, so a ladder can declare `PQ` over BT.709
  samples and segcheck says it is fine, because by the only measure it takes it
  is. The milestone reads the colour description the media states about itself —
  the H.264 and HEVC VUI, the `colr` box in fMP4 — and answers the manifest's
  claims with it: `VIDEO-RANGE` against the transfer function, the codec string's
  profile and level against the SPS and the AV1 sequence header, HDR10 static
  metadata on a rung that declares PQ, Dolby Vision `dvcC` against
  `SUPPLEMENTAL-CODECS`, and colour consistency between the rungs of one ladder.
  Judging whether a grade is *right* for the content stays out — that is grading,
  not checking.

- **M13 — Audio, past the sanity check** (SC-81 … SC-86), targeted at v0.8.0.
  SC-18 is the floor — sample rate and channel count consistent within a
  rendition and against `CODECS` — and above it the audio half of a ladder is
  checked by its name: `parseStsd` reads width and height and stops, so an fMP4
  audio track's configuration is never read; `ac-3`, `ec-3`, `Opus` and `fLaC`
  are recognised by sample entry name with their configuration boxes unopened;
  and `CHANNELS` / `AudioChannelConfiguration` are parsed by neither reader. So
  `CHANNELS="6"` over a stereo track, `mp4a.40.2` over SBR content and `16/JOC`
  over a stream with no object metadata all come back clean. The milestone reads
  `AudioSpecificConfig`, `dac3`, `dec3`, `dOps`, `dfLa`, `elst` and `dialnorm`,
  and answers the manifest with them: real channel count against `CHANNELS`, the
  audio codec string against the configuration it names, loudness that steps
  between rungs, an Atmos badge with no JOC behind it, and the priming samples
  behind a lip-sync error that only half the audience sees. **Metadata, never
  decoding** — measuring what the audio sounds like needs a decoder, which SC-18
  already rules out and nothing here reintroduces.

### Fixed

- **Three parser defects found by fuzzing** (SC-88), all of the same shape — a
  parser answering confidently instead of failing.
  A `sidx` version byte other than 0 was read as version 1, so a version of `0x30`
  had the time fields read at the wrong width; a run of `0xff` then became a
  `first_offset` of nearly 2^64 and every subsegment offset came out negative — a
  byte-range request starting before the file does. Versions other than 0 and 1 are
  rejected now and the offset arithmetic is guarded against overflow.
  Timestamps advancing by a single tick on a 90kHz clock yielded 90000fps, which
  `framerate` would have compared against the manifest and used to call the
  rendition wrong. A rate past 1000fps is arithmetic on timestamps that did not
  advance, not a measurement.
  And the bitstream readers had always refused an implausible frame size while the
  container readers had not, so a malformed `tkhd` or sample entry could report a
  16688x12336 rendition and have `resolution` report a mismatch against a manifest
  that says nothing of the kind. One rule for both now: unknown beats wrong.

- **`trex` defaults were never read** (SC-87), which made every sample zero ticks
  long on a large share of real on-demand DASH. `mvex`/`trex` in the
  initialisation segment states a track's default sample duration, size and flags,
  and a fragment may state none of them itself — Sony's DASH-IF vector carries
  `default_sample_duration=1001` there and nothing in its fragments. Ignoring it
  did not fail loudly: the segment's stated duration became zero, so `duration`
  reported the media as 100% shorter than declared and `continuity` reported a gap
  before every segment, against a stream that is entirely correct. The defaults are
  read per track now and used as the floor, with the `tfhd` overriding them and a
  `trun` overriding that. This affects any CMAF stream packaged this way, not only
  the single-file ones that surfaced it. `Track.DurationSec` also stopped reporting
  a computed zero as a measurement: timestamps that never advance measure nothing,
  and saying otherwise is the same false report by another route.

- **An encrypted track reported the wrong codec and no resolution** (SC-79). When
  a sample entry is `encv` or `enca`, the original format is recovered from its
  `sinf`/`frma` child — but the search started at byte 0 of the entry, where the
  fixed `VisualSampleEntry` fields sit rather than any child box. The leading
  reserved zeros parse as a box of declared size 0, which swallows the entry
  whole, so `frma` was never found. The codec stayed `"encv"`, which the `tracks`
  check compared against the manifest's declared codec and reported as a mismatch
  on media that was correct, and the resolution was never read at all because
  `"encv"` is not a visual sample entry type. The search now starts after the
  fixed fields — 78 bytes for video, 28 for audio.

- **The documentation site had drifted from what shipped.** Install still read
  `brew install Allan-Nava/tap/segcheck` after the formula became a cask in
  v0.1.1, and there was no Docker install at all although the image ships on
  GHCR. Fixed, plus: the GitLab CI recipe now runs the published image instead
  of installing a Go toolchain, the schedule recipe says why `--exit-on` must
  stay off under a `CronJob`, and the archive block names the SBOM and cosign
  signatures every release now carries. The check matrix and the flag reference
  were audited against the README table and the `usage` const and were already
  in sync.
- The release workflow's `verify-image` job pulled
  `ghcr.io/…/segcheck:v0.1.1`, which never exists: goreleaser tags images with
  `{{ .Version }}`, the git tag *without* its leading `v`. The job now strips
  it. This failed the v0.1.1 run on a release that was otherwise complete —
  archives, SBOMs, signatures, the image and the Homebrew cask were all
  published correctly, and the published image passes the same contract test
  when it is asked for by its real name.

## [0.1.1] - 2026-08-10

How segcheck is built, tested and delivered. No new checks, parsers or flags —
the CLI behaves exactly as 0.1.0 did — so `v0.2.0` stays reserved for M3, where
the backlog has promised it.

### Added

- **Documentation site** (SC-33): <https://allan-nava.github.io/segcheck/>,
  served from `docs/` by a `Pages` workflow. One self-contained static page —
  sample output, the manifest-claims table, the thirteen checks with their worst
  status, install, the flag reference, CI recipes, what a run costs and what the
  tool deliberately does not do. No Jekyll, no theme gem and no external request
  at render time, so the site holds to the same zero-dependency rule as the
  binary. SC-49 keeps the per-check reference pages on the backlog.
- **Brand assets** (SC-50): `docs/assets/` gains the logo, favicon, horizontal
  wordmark and OG card as hand-written SVG, with PNG renders for the consumers
  that cannot take vectors. The mark is the rendition ladder with one segment
  the media does not have, flagged in amber, and the verdict beside it. The
  README leads with it.

- **Backlog and roadmap tooling** (SC-34): `scripts/backlog.sh` lints
  `BACKLOG.md` — stable `SC-n` ids with no gaps or duplicates, valid
  `prio`/`size`/`labels` metadata, shipped items carrying the release that
  shipped them — and generates `ROADMAP.md` from it, grouped by milestone with
  progress, a "next up" list and a label index. CI fails on a malformed backlog
  or a stale roadmap. POSIX sh and awk only, in keeping with the
  zero-dependency rule.
- `CLAUDE.md` is now the operating brief for agents (repo map, working rules,
  the check-authoring pattern, known traps such as the 33-bit PTS wrap and the
  `(value, false)` protocol, and pointers), with `AGENTS.md` staying canonical.

- **M10 — Authoring-spec conformance** (SC-59 … SC-63), targeted at v0.5.0: the
  measurable subset of Apple's HLS Authoring Specification and DASH-IF IOP —
  peak-to-average bitrate, an IDR at every segment start, bitrate tiers by
  resolution, `@codecs` against the sample entry, timescales consistent across
  an adaptation set, `@segmentAlignment` that is actually true — plus I-frame
  playlists and trick-play thumbnail sheets. A verdict layer over measurements
  M3 and M4 already take, gated behind an opt-in `--profile` so a conformance
  rule can never turn yesterday's clean run into today's wall of findings.
  Scoped to rules the media can arbitrate: manifest-only assertions stay in
  checkfleet.
- **M9 — Wallclock and DVR correctness** (SC-51 … SC-55), targeted at v0.4.0
  alongside the live milestone it shares machinery with: `EXT-X-PROGRAM-DATE-TIME`
  checked against the media timestamps and across renditions, DASH
  `availabilityStartTime`/`UTCTiming` against what is actually fetchable, the
  DVR window proven to be real, discontinuity declarations checked against the
  timeline resets the media contains, and live-edge drift over a `--watch`
  window. Every check up to now reasons about a timeline relative to itself;
  this milestone is where the manifest's claims about *real-world* time get
  arbitrated by the media.
- **Container image** (SC-43): `Dockerfile` builds a `FROM scratch` image
  carrying the static binary, a CA bundle and nothing else, running as
  `65532:65532`. `--build-arg VERSION` stamps the same `main.version` goreleaser
  stamps, so an image and a release archive cannot disagree about what they are.
- **Image smoke test** (SC-45): `internal/analyze/docker_test.go`, behind the
  `docker` build tag with its own CI job. It asserts the version is stamped, no
  shell is reachable, a trust store is present, the image does not run as root,
  and a containerised run finds a planted continuity gap against a live origin
  while still exiting 0. The trust-store assertion was checked against a
  deliberately bundle-less image to confirm it fails when it should — without a
  CA bundle every `https://` manifest fails TLS and reads like a broken origin.
- **Multi-arch image on GHCR** (SC-44): `linux/amd64` and `linux/arm64` from one
  goreleaser `dockers_v2` entry packaging the binaries the release already
  built, tagged `vX.Y.Z` plus a `latest` that stays put on a prerelease. The
  release workflow re-runs the smoke test against the published image.
- **`:edge` image from `main`** (SC-56): `.github/workflows/docker.yml` publishes
  `ghcr.io/allan-nava/segcheck:edge` and `:sha-<commit>` on every push to `main`,
  gated on the SC-45 contract test so a broken image never reaches the registry.
  Releases stay release.yml's job. The `Dockerfile` build stage is pinned to
  `$BUILDPLATFORM` and takes `GOOS`/`GOARCH` from `TARGETOS`/`TARGETARCH`, so a
  multi-arch build cross-compiles rather than running `go build` under QEMU —
  9 seconds instead of minutes for the foreign architecture.
- **`docs/running-in-containers.md`** (SC-46): Compose and Kubernetes `CronJob`
  recipes, a table of what each image tag means, why `--exit-on` must stay off
  under a scheduler, and what a schedule costs in CDN egress.
- SBOM generation and keyless cosign signing are configured (SC-47) but
  **unverified** — neither can run without a tagged release, so the item stays
  open until `cosign verify` succeeds against a published image.

- **Tests for the two packages that had none** (SC-57, SC-58). `internal/fetch`
  goes from 0% to 94.1% and `cmd/segcheck` from 0% to 90.1%; total coverage
  67.1% → 72.5%. The exit-code rule the README leads with — exit 0 whenever the
  check ran — is now asserted rather than assumed, as is the response-truncation
  boundary that decides whether a cut body reaches the parsers as if it were
  whole. Both suites were mutation-checked: the truncation comparison and the
  interspersed-flag parser were each broken on purpose to confirm the tests fail
  when the behaviour does.

### Changed

- **The Homebrew tap upload is live** (SC-32): `skip_upload` is gone, so the next
  tag writes `Casks/segcheck.rb` into `Allan-Nava/homebrew-tap`. The install line
  in the README is now `brew install --cask allan-nava/tap/segcheck`, with the
  macOS-only caveat stated — Homebrew on Linux does not support casks, so those
  users go through `go install` or the release archives.
- `main` is now `os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))`, with the
  `check` flag set on `flag.ContinueOnError` instead of `ExitOnError`, so exit
  codes and output are testable without spawning a process (SC-58). No change to
  the CLI's behaviour.

- `brews` → `homebrew_casks` and `dockers`/`docker_manifests` → `dockers_v2` in
  `.goreleaser.yaml`: both old forms are deprecated and fail `goreleaser check`,
  which CI now runs so a release config breaks on a pull request rather than on
  a tag.
- **Test-first is now an explicit rule** in `AGENTS.md` and `CLAUDE.md`: the
  test that plants the defect is written and watched fail before the code that
  finds it. SC-48 adds the CI coverage ratchet that makes a missing test visible
  in the build rather than in review.
- `BACKLOG.md` reorganised into eight milestones with target releases and
  phases, and extended with SC-35 … SC-48: parser fuzzing, a real-stream smoke
  suite, CEA-608/708 captions, subtitle renditions, LL-HLS parts, multi-period
  DASH, a baseline diff, AV1/VP9 coded resolution, the container-image
  milestone, and the coverage ratchet.

## [0.1.0] - 2026-08-10

First release. `segcheck check <manifest-url>` downloads media segments and
compares the media against the manifest's claims.

### Added

- **MPEG-TS parser** (SC-1): PAT/PMT with PSI reassembly across packets, PES
  presentation timestamps, continuity-counter breaks as packet-loss evidence,
  scrambling detection, and recovery after lost sync.
- **fMP4/CMAF parser** (SC-2): `moov` for timescale, handler, codec and coded
  size; `moof`/`tfhd`/`tfdt`/`trun` for the segment timeline, including
  composition offsets; `encv`/`enca`/`pssh` for protection.
- **H.264 SPS reader** (SC-3): coded resolution from the bitstream, with
  Exp-Golomb decoding, emulation-prevention unescaping, scaling-list skipping and
  the frame-cropping arithmetic that makes 1080 lines out of 1088 coded ones.
- **HLS parser** (SC-4): master and media playlists; attribute lists with quoted
  commas; `EXT-X-MAP` including its `BYTERANGE`; `EXT-X-BYTERANGE` with implicit
  offsets; `EXT-X-KEY`; `EXT-X-DISCONTINUITY`; `EXT-X-PROGRAM-DATE-TIME`;
  audio-only variants classified from `CODECS` rather than assumed to be video.
- **DASH parser** (SC-5): `SegmentTemplate` with `$Number$`, `$Time$`,
  `$RepresentationID$`, `$Bandwidth$` and `%0Nd` formats; `SegmentTimeline` with
  `@r` including `-1`; `SegmentList`; `BaseURL` chains; `xs:duration`; live-edge
  derivation from `availabilityStartTime`.
- **Packed audio** (SC-6): ADTS AAC frame counting plus the ID3
  `com.apple.streaming.transportStreamTimestamp` tag, which gives audio-only
  renditions the timeline they otherwise lack.
- **Checks** (SC-7 … SC-13): `continuity` (undeclared gaps and overlaps, PTS
  wraparound, packet loss), `duration` (per-segment and accumulated drift,
  `TARGETDURATION` compliance), `resolution`, `bitrate` (under- and
  over-declared `BANDWIDTH`), `alignment` across renditions, `timeline`
  (`SegmentTimeline` `@t` versus `tfdt`), `tracks`, `container`, `encryption`,
  `ladder` and `init`.
- **Output** (SC-14): terminal with worst findings first and colour only on a
  TTY, JSON with measurements as numbers, and an ops-style markdown report.
  `--exit-on warn|bad|error` for CI; exit 0 by default whenever the check ran.
- Sampling controls: `--segments`, `--renditions`, `--audio`, `--from
  auto|edge|start`, `--concurrency`. A capped `--renditions` always keeps the top
  and bottom rungs.
- Thresholds: `--duration-tolerance`, `--gap-tolerance`, `--bitrate-tolerance`.
- HTTP controls: `--timeout`, `--header`, `--max-bytes`, `--insecure`, and a
  `segcheck/<version>` User-Agent so checks are identifiable in access logs.

### Notes

- Zero dependencies: every parser is in-tree and uses the standard library only.
  No ffmpeg, no ffprobe, no cgo.
- Verified against Apple's fMP4 and MPEG-TS reference streams and a public DASH
  stream: all three come back clean, with durations matching to +0.00% and coded
  resolutions read from the bitstream.

[0.3.0]: https://github.com/Allan-Nava/segcheck/releases/tag/v0.3.0
[0.2.0]: https://github.com/Allan-Nava/segcheck/releases/tag/v0.2.0
[0.1.1]: https://github.com/Allan-Nava/segcheck/releases/tag/v0.1.1
[0.1.0]: https://github.com/Allan-Nava/segcheck/releases/tag/v0.1.0
