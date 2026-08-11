# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

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

[0.1.1]: https://github.com/Allan-Nava/segcheck/releases/tag/v0.1.1
[0.1.0]: https://github.com/Allan-Nava/segcheck/releases/tag/v0.1.0
