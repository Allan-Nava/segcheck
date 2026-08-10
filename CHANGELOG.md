# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
- **`docs/running-in-containers.md`** (SC-46): Compose and Kubernetes `CronJob`
  recipes, including why `--exit-on` must stay off under a scheduler and what a
  schedule costs in CDN egress.
- SBOM generation and keyless cosign signing are configured (SC-47) but
  **unverified** — neither can run without a tagged release, so the item stays
  open until `cosign verify` succeeds against a published image.

### Changed

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

[0.1.0]: https://github.com/Allan-Nava/segcheck/releases/tag/v0.1.0
