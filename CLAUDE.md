# CLAUDE.md — segcheck

`segcheck` (`github.com/Allan-Nava/segcheck`) downloads HLS/DASH media segments
and compares the **media** against the **manifest's claims**. One static Go
binary, zero dependencies: MPEG-TS, fMP4/CMAF, H.264 and HEVC parameter sets, ADTS
and MPEG audio, WebVTT and TTML, CEA-608/708 caption SEI, SCTE-35 splice sections and
AES-128 decryption are all done in-tree with the standard library. `cmd/segcheck` is the CLI, `internal/manifest`
reads HLS/DASH, `internal/media` reads segment bytes, `internal/analyze` runs the
checks, `internal/finding` is the result model, `internal/output` renders.

This file is the operating brief for agents working in the repo.
[AGENTS.md](AGENTS.md) holds the same rules for other tools — when they disagree,
AGENTS.md wins and this file gets fixed.

## Working rules (ALWAYS)

- **Every feature earns its place against one sentence**: *compare the media
  against the manifest's claims*. A check that only reads the manifest belongs in
  [checkfleet](https://github.com/Allan-Nava/checkfleet), not here.
- **Zero dependencies.** `go.mod` has no `require` block, `go.sum` stays empty,
  CI enforces both. No ffmpeg, no ffprobe, no cgo, no shelling out — including in
  the tooling (`scripts/` is POSIX sh + awk for the same reason). A new format
  gets parsed in-tree. This is the product's differentiator, not an aesthetic.
- **Exit 0 whenever the check ran.** Findings are output, not failure. Only
  `--exit-on` produces a non-zero exit; a crash or a usage error exits non-zero.
- **Worst findings first**, in every renderer. The first line is the thing the
  operator must look at.
- **Never invent a measurement.** Unknown timescale means unknown duration:
  return `(0, false)` and let the check stay quiet. A confident wrong number is
  worse than no number.
- **A limit of this tool is not a defect in the stream.** Unsupported containers,
  encrypted segments and unexpandable representations get an honest OK-level or
  ERROR finding saying *segcheck* could not look — never a BAD that sends someone
  hunting a phantom.
- **No secrets on the command line.** Credentials go in `--header` values the
  caller reads from the environment, never into a flag that lands in shell
  history or a CI log. Same for future `--key` (SC-22) and webhooks (SC-28).
- **Every idea goes in `BACKLOG.md`** with a stable `SC-n` id — never a scattered
  TODO comment. After editing it, run `scripts/backlog.sh roadmap` and commit the
  regenerated `ROADMAP.md`, or CI fails. Commits and CHANGELOG entries reference
  the id.
- **Test first, always.** The failing test lands before the implementation —
  red, green, refactor, no exceptions for "small" changes. A test written
  afterwards asserts what the code does instead of what the stream means, which
  is how a check ends up passing on media it was meant to flag. Write the
  `mediatest` builder and the failing assertion, run it, watch it fail for the
  right reason, then write the parser or the check.
- **Align everything**: a new or changed check must land in the same commit as
  its `README.md` table row, its `--help` text, its tests, the `BACKLOG.md` tick
  and the `CHANGELOG.md` line. A README that describes a check that behaves
  differently is a bug report waiting to be filed.
- **Releases**: every release is a tagged `vX.Y.Z` with a new `CHANGELOG.md`
  section (Keep a Changelog). `minor` for new checks, parsers or flags; `patch`
  for fixes. **Never `git push`** — that is the maintainer's call. No
  `Co-Authored-By` trailers.

## Pattern for adding a check (validated on the v0.1.0 set)

1. **Backlog first**: the item exists in `BACKLOG.md` with an `SC-n`, a
   milestone, `prio`, `size` and `labels`. Regenerate `ROADMAP.md`.
2. **Red first**: `internal/media/mediatest` *builds* a segment with the defect
   planted and known-by-construction timestamps, the assertion is written
   against it, and the test is **run and seen failing for the right reason**
   before any production code exists. No binary fixtures ever enter this
   repository.
3. **Parser change with a round trip**: `mediatest.SPS` is the writer and
   `media.ParseSPS` the reader — the round trip is what catches bit-level
   mistakes. New bitstream readers get the same treatment.
4. **The check emits a real `finding.Finding`**: `Target` names the exact
   segment or rendition, `Message` names the defect, `Value`/`Unit` carry the
   measurement so machine consumers never parse prose, `Hint` says what it means
   for the viewer.
5. **Two tests, minimum**: one that plants the defect and asserts it is found
   *and correctly attributed*, and `TestRun_CleanStreamHasNoProblems`, which
   asserts nothing above OK. A checker that cries wolf on healthy media is worse
   than no checker.
6. **`go test -race ./...`** — the segment fan-out writes into a shared slice.
7. **Real streams before the tag** — automated now (SC-36), but run it yourself
   while writing a check, because it is where the design gets corrected:

   ```
   go build -o /tmp/segcheck ./cmd/segcheck
   SEGCHECK_BIN=/tmp/segcheck go test -tags smoke -run TestSmokeReferenceStreams ./internal/analyze/ -v
   ```

   Seven streams: Apple's fMP4 and MPEG-TS references, a DASH `SegmentTemplate`
   manifest, a single-file on-demand one, one whose AdaptationSet states nothing,
   a CENC one and a multi-period one. Two more are served from `mediatest` over a
   loopback origin, for the two features no public stream declares — LL-HLS
   `EXT-X-PART` and `EXT-X-DISCONTINUITY`. A self-served stream cannot catch a
   shared misreading, only a check falling silent, so it never counts towards the
   guard that says a real stream was reached. The assertion is **not** "nothing
   above OK" — Apple's
   advanced example legitimately over-declares BANDWIDTH and ships an inverted
   ladder — but a per-stream baseline of the checks allowed to exceed OK, plus a
   list of checks that must not fall silent. A new finding outside the baseline is
   a regression; so is a check that stops speaking, and that is the half that
   catches a parser which quietly stopped reading. Every false positive this
   project has shipped was found here rather than by a unit test.
8. **Close the loop**: CHANGELOG entry referencing the `SC-n`, tick the backlog
   item with `ver=X.Y.Z`, regenerate the roadmap, tag. No push.

## Known traps / technical rules

- **`MinPTS`/`MaxPTS` are min and max, not first and last.** With B-frames the
  stream is not in presentation order; anything that assumes decode order is
  wrong on real content.
- **Real duration is the PTS span plus one frame.** The span from first to last
  timestamp omits how long the last frame is displayed — 40ms on a 25fps
  segment, enough to trip a 1% drift check. When the container states the
  duration (MP4 sample durations) that value wins over the derived one.
- **MPEG-TS timestamps wrap at 33 bits** (`media.PTSModulus`). Any new timing
  arithmetic has to handle the wrap or it will report a ~26.5-hour gap once a
  day on a long-running live stream.
- **`(value, false)` is the protocol for "I could not measure this".** Callers
  must check the bool, never use the zero value. A check that sees `false` stays
  silent or reports OK-level "not verifiable", never BAD.
- **`ERROR` means the check could not run**, not that the stream is broken — it
  sorts above BAD because an operator needs to know the coverage has a hole, and
  that is the only reason.
- **The TS resolution reader dispatches on stream type, never on guesswork.**
  H.264 (`0x1B`) and HEVC (`0x24`/`0x25`) put the NAL type in different bits —
  five low bits versus bits 1..6 of a two-byte header — so reading one stream
  with the other reader does not fail cleanly: it can find something SPS-shaped
  and return a plausible wrong resolution. Add a codec to `streamCodec`, and add
  its reader to the switch in `tsStream.track()` at the same time, or the rung
  goes silent (SC-15 was exactly that silence for HEVC).
- **In fMP4 the container states the resolution and no bitstream reader is
  needed** — the visual sample entry carries it. A codec missing from
  `isVisualSampleEntry` goes silent the way MPEG-TS HEVC used to, which is why
  `hvc1` has a test of its own rather than being assumed to work.
- **Clock injection**: `analyze.Options.Now` fixes the clock for live-edge maths
  and DASH `$Time$`/`availabilityStartTime` expansion. Never call `time.Now()`
  inside a check or a parser — the DASH tests pin the clock.
- **`--renditions 1` keeps only the top rung**, not top-and-bottom; from 2 up,
  `pick` spreads evenly across the sorted ladder and always includes both ends.
  Renditions are sorted by `BANDWIDTH` ascending before sampling.
- **Colour only on a TTY**, and `NO_COLOR` is honoured. JSON and markdown output
  are never coloured — they get piped into incident docs.
- **One response body is capped at 64 MiB** (`fetch.DefaultMaxBytes`) and a
  truncated body is flagged, not silently parsed as if complete.
- **Requests carry a `segcheck/<version>` User-Agent** so a check is
  distinguishable from real traffic in access logs. Do not make it configurable
  by default.
- **CI gates**: `gofmt` over `./cmd ./internal`, `go vet`, tests with coverage,
  `-race`, cross-build matrix, `govulncheck`, the zero-dependency check, and
  `scripts/backlog.sh lint` + `check`. A doc-only commit can still fail CI on a
  stale `ROADMAP.md`.

- **A number the container states is not always the number.** Three codecs lie in a
  way that reads as data: AC-3 and E-AC-3 write `2` into the `AudioSampleEntry`
  `channelcount` whatever the programme is (the layout is in `dac3`/`dec3`, and with
  dependent substreams present the count is genuinely unknowable); HLS
  `CHANNELS="16/JOC"` counts a rendered Atmos presentation, not the coded 5.1 bed;
  and HE-AAC (`mp4a.40.5`, `mp4a.40.29`) codes at half the rate it plays, so a
  sample entry saying 24 kHz against a manifest saying 48 kHz is correct — and
  HE-AAC **v2** (`mp4a.40.29`) additionally codes a mono core that Parametric Stereo
  renders as stereo, so a declared 1 against a measured 2 is also correct. Each of
  these produced a BAD on a public reference stream before it was understood.
- **Partial encryption is more dangerous than full.** With AES-128 nothing parses and
  every check says it could not look. With SAMPLE-AES or CENC the container parses,
  the timing checks pass, and the bitstream readers *succeed and find nothing* — a
  caption scan over ciphertext reported "scanned, no captions" and turned a manifest
  correctly declaring CC1 into a BAD. Anything that reads inside a sample must consult
  `bitstreamOpaque`. The distinction is per container: in fMP4 the resolution is in
  the sample entry and the sync flag in the `trun`, both in the clear.
- **`X-TIMESTAMP-MAP` is an HLS mechanism.** DASH does not use the tag and puts WebVTT
  cue times on the presentation timeline directly, so its absence is a defect in one
  format and normal in the other. `renditionData.dash` is what tells them apart.
- **A TTML document in an `stpp` sample states presentation times**, not times relative
  to the fragment carrying it (ISO/IEC 14496-30 puts them relative to the period).
  Adding the fragment's `tfdt` double-counts it and reports correct media as adrift.
- **A `trun` data offset is signed.** A fragment may place its samples before the box
  that describes them, and reading it unsigned puts them four gigabytes away.
- **An `EXT-X-KEY` with no `IV` is an instruction, not a gap**: the IV is then the
  segment's media sequence number as a 128-bit big-endian value. Defaulting to zeroes
  decrypts to noise, and noise is indistinguishable from a wrong key — which is why
  the PKCS#7 padding is verified rather than trusted.
- **A subtitle or caption rendition's timestamps are a cue span, not a segment
  extent.** `continuity`, `duration` and `keyframe` read them as an extent and
  produced six BADs and a 26% duration mismatch on Apple's own reference stream; all
  three now skip `manifest.Text`.
- **Signalling is not media.** A splice-information PID or an ID3 PID appears only in
  the segments that carry a cue, so `trackShape` excludes them — counting them made
  `tracks` warn about a decoder reset on every ad break in a healthy stream.
- **`EXT-X-DISCONTINUITY` signals a change of encoding, not only of timestamps.** RFC 8216
  §4.3.2.3 lists file format, track layout and codec alongside the timestamp sequence, so
  a tag over a perfectly continuous timeline is legitimate whenever the media on either
  side changed shape. Reading timestamps alone would report a correct splice into
  differently encoded content as a decoder flush performed for nothing.
- **A DASH Period boundary does not divide a segment grid.** A period's first segment
  legitimately begins *before* the period does — the player trims the head — and audio
  straddles almost every boundary there is: nomor's DASH-IF vector puts 1.96198s AAC
  segments against a 250s period, so its first audio segment starts 0.83s early. The
  `period` check therefore reads placement from video only, and allows a whole segment
  of head start in that direction while allowing none in the other, because media
  starting *after* its period leaves a hole nothing fills.
- **Two consecutive Periods are not two rungs of one ladder.** A ladder is what a player
  chooses between at one moment; a multi-period MPD holds several of them end to end.
  `ladder` and `alignment` compared across the boundary and reported every well-formed
  multi-period presentation as full of duplicate rungs and four seconds misaligned.
  Anything that compares renditions with each other has to group by `PeriodIndex` first,
  and anything keyed by segment index has to key by period too — periods restart their
  numbering, so segment 0 of one is not the moment segment 0 of the next is.
- **A round trip against `mediatest` cannot catch a shared misreading.** Where an
  external authority exists, use it: RFC 3602's vectors for the cipher, bytes decoded
  by hand from the specification for a `segmentation_descriptor`. Where a real stream
  exists, prefer that — five of this project's design errors were found only there.

## Backlog and roadmap (the dynamic part)

`BACKLOG.md` is the source of truth; `ROADMAP.md` is **generated** from it and
must never be hand-edited. Items carry an invisible metadata comment:

```
- [ ] **SC-15 — HEVC/H.265 SPS**: what it is and why it earns its place.
  <!-- sc: prio=high size=L labels=parser -->
```

`scripts/backlog.sh`:

| Command | What it does |
|---|---|
| `lint` | ids unique and gap-free, metadata valid, done items carry `ver=`, milestone phases sane |
| `roadmap` | regenerate `ROADMAP.md` |
| `check` | fail if `ROADMAP.md` is stale (the CI gate) |
| `stats` | one-line summary |
| `next [n]` | the n highest-priority open items, in flight first |

Both `scripts/backlog_test.sh` (the generator and the linter) and
`scripts/backlog_issues_test.sh` (the issue planner) run in CI. `BACKLOG_FILE` and
`ROADMAP_FILE` point the tool at a fixture — never run a test that omits the second,
because `roadmap` will overwrite this repository's own.

Milestones: **M1/M2** shipped in v0.1.0 · **M3** codec and timing depth (v0.2.0,
in flight) · **M4** audio, captions, subtitles, ad signalling (v0.3.0) · **M5**
live and delivery (v0.4.0) · **M6** integration (v0.5.0) · **M7** project and
release (ongoing). Ids are stable forever: retire an item by marking it done,
never by deleting it and reusing the number.

## Pointers

- [AGENTS.md](AGENTS.md) — the same rules, canonical · [BACKLOG.md](BACKLOG.md)
  — planned work · [ROADMAP.md](ROADMAP.md) — generated view ·
  [CHANGELOG.md](CHANGELOG.md) — Keep a Changelog
- `internal/media/mediatest/` — the segment builders every parser test is
  asserted against; start here before touching a parser. A round trip against them
  cannot catch a shared misreading of a layout, so where an outside authority exists
  (RFC 3602's AES-CBC vectors, spec bytes decoded by hand) assert against that too
- `internal/media/samples.go` — locating a fragment's samples from the `trun` data
  offsets. Everything that reads *inside* a sample depends on it: a `c608` caption
  track's field, a `stpp` track's cues
- `internal/analyze/checks.go` — every check lives here; `analyze.go` does the
  manifest load, rendition pick, segment fan-out
- `docs/index.html` — the GitHub Pages site
  (<https://allan-nava.github.io/segcheck/>), one self-contained static page
  with inline CSS/JS and no external request at render time; brand assets are
  hand-written SVG in `docs/assets/`, PNGs are renders of them. A check, flag or
  default that changes moves the page in the same commit as the README row
- `.goreleaser.yaml` — release build. The Homebrew cask has published to
  `Allan-Nava/homebrew-tap` since v0.1.1 (SC-32) and needs the
  `HOMEBREW_TAP_TOKEN` secret; a release after that fine-grained PAT expires
  publishes its archives and image and then fails at that step. The darwin
  binaries are ad-hoc signed, not notarised, so the cask strips
  `com.apple.quarantine` in a `postflight` (SC-65) — without it Gatekeeper kills
  the installed binary on sight, silently, with exit 137. SC-94 is the real fix
- Related: [checkfleet](https://github.com/Allan-Nava/checkfleet) — the
  manifest-level and fleet-level checker segcheck deliberately does not
  duplicate (SC-30 exposes these analyses there as a `stream-deep` module)
- License: PolyForm Noncommercial 1.0.0 — keep the header and the README badge
  accurate if it ever changes

## graphify

This project has a knowledge graph at graphify-out/ with god nodes, community structure, and cross-file relationships.

Rules:
- For codebase questions, first run `graphify query "<question>"` when graphify-out/graph.json exists. Use `graphify path "<A>" "<B>"` for relationships and `graphify explain "<concept>"` for focused concepts. These return a scoped subgraph, usually much smaller than GRAPH_REPORT.md or raw grep output.
- If graphify-out/wiki/index.md exists, use it for broad navigation instead of raw source browsing.
- Read graphify-out/GRAPH_REPORT.md only for broad architecture review or when query/path/explain do not surface enough context.
- After modifying code, run `graphify update .` to keep the graph current (AST-only, no API cost).
