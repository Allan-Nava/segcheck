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

## M3 — Codec and timing depth <!-- ms: target=v0.2.0 phase=now -->

The rungs segcheck currently reads least well. Every item here removes a silent
skip — a place where the tool says nothing because it cannot look, not because
the stream is healthy.

- [ ] **SC-15 — HEVC/H.265 SPS**: coded resolution from `hvcC` and from an
  MPEG-TS HEVC elementary stream. Today HEVC in TS reports the codec but not the
  resolution, so the `resolution` check silently skips those rungs.
  <!-- sc: prio=high size=L labels=parser -->
- [ ] **SC-16 — Keyframe alignment**: every segment must start on an IDR/IRAP. A
  segment that opens on a non-keyframe cannot be switched into, which is the
  defect behind "ABR switching stutters even though the boundaries line up".
  Needs slice-type inspection for H.264 and `styp`/`sap` for CMAF.
  <!-- sc: prio=high size=L labels=check,parser -->
- [ ] **SC-17 — Frame rate**: measured from the timestamp deltas, against
  `FRAME-RATE` / `@frameRate`. Also catches a rung whose real frame rate differs
  from the rest of the ladder. <!-- sc: prio=high size=M labels=check -->
- [ ] **SC-19 — `sidx` and `SegmentBase`**: parse the index so single-file DASH
  representations can be sampled at all. Today they are reported as unsupported
  rather than checked. <!-- sc: prio=high size=M labels=parser -->
- [ ] **SC-42 — AV1 and VP9 coded resolution**: `av1C` / `vpcC` sample entries,
  and the OBU sequence header for AV1 in CMAF, so an AV1 ladder gets the same
  resolution check as an H.264 one.
  <!-- sc: prio=med size=L labels=parser -->
- [ ] **SC-35 — Parser fuzzing**: `go test -fuzz` targets for the TS, MP4, SPS
  and ADTS readers, with a checked-in seed corpus. The parsers eat bytes from
  the open internet; a panic on a truncated box is a crash in someone's CI.
  <!-- sc: prio=high size=M labels=tests,parser -->

## M4 — Everything that is not the video track <!-- ms: target=v0.3.0 phase=next -->

Audio, captions, subtitles, ad signalling and protected content — the parts of a
stream that break in production and that no manifest-only checker can see.

- [ ] **SC-18 — Audio sanity**: sample rate and channel count consistency across
  a rendition and against `CODECS`; silence detection is explicitly out of scope
  (that needs decoding). <!-- sc: prio=high size=M labels=check -->
- [ ] **SC-20 — SCTE-35 / `EXT-X-DATERANGE`**: ad-break signalling present in the
  manifest and consistent with the segment boundaries. The check operators
  actually want before a live event.
  <!-- sc: prio=high size=L labels=check,parser -->
- [ ] **SC-37 — CEA-608/708 captions**: captions declared in the manifest
  (`CLOSED-CAPTIONS`, DASH `Accessibility`) against caption data actually
  carried in the bitstream (H.264 SEI user data, `cdat`/`cdt2` in CMAF). "The
  captions are declared but the encoder stopped emitting them" is invisible to
  every manifest checker. <!-- sc: prio=high size=L labels=check,parser -->
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

## M5 — Live and delivery <!-- ms: target=v0.4.0 phase=later -->

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

## M8 — Container image and supply chain <!-- ms: target=v0.3.0 phase=next -->

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
  <!-- sc: prio=high size=M labels=tests,release ver=unreleased -->
- [x] **SC-43 — Dockerfile (scratch, non-root)**: multi-stage, building with
  `CGO_ENABLED=0 -trimpath` and the same `-X main.version` ldflag goreleaser
  uses, so the image and the released binary can never report different
  versions. Final stage `FROM scratch` with the binary, `USER 65532:65532`, and
  `/etc/ssl/certs/ca-certificates.crt` copied from the builder — **the CA bundle
  is the trap**: without it every `https://` manifest fails TLS inside the
  container, and the failure reads like an origin defect rather than a packaging
  one. <!-- sc: prio=high size=M labels=release,project ver=unreleased -->
- [x] **SC-44 — Multi-arch image on GHCR**: `linux/amd64` and `linux/arm64` from
  one `dockers_v2` entry (the older `dockers` + `docker_manifests` pair is
  deprecated and fails `goreleaser check`), packaging the binaries goreleaser
  already built rather than rebuilding them, tagged `vX.Y.Z` plus a `latest`
  that stays put on a prerelease. `Dockerfile.release` copies from
  `${TARGETPLATFORM}/segcheck` because that is where the binary is staged.
  Verified by a full `--snapshot` run; the push itself is first exercised at the
  next tag, and the release workflow re-runs SC-45's test against the published
  image. <!-- sc: prio=high size=M labels=release ver=unreleased -->
- [ ] **SC-47 — SBOM and signed artefacts**: goreleaser `sboms` and keyless
  cosign signing for the checksums and the image manifest are configured, and
  the release workflow installs syft and cosign with the `id-token: write`
  permission the keyless flow needs. **Not verified**: neither step can run
  without a real tag, and syft is not installed locally. Close this once a
  tagged release has produced an SBOM and a verifiable signature —
  `cosign verify` against the published image is the acceptance test.
  <!-- sc: prio=med size=M labels=release -->
- [x] **SC-46 — Scheduled-run recipes**: `docs/running-in-containers.md` — a
  Compose service and a Kubernetes `CronJob`, with `backoffLimit`, a locked-down
  `securityContext`, a pinned tag, the egress cost of a schedule, and why
  `--exit-on` must stay off in a `CronJob` (a non-zero exit would make the
  scheduler retry a run that worked).
  <!-- sc: prio=med size=S labels=docs,delivery ver=unreleased -->

## M7 — Project and release <!-- ms: target=ongoing phase=ongoing -->

- [x] **SC-34 — Backlog and roadmap tooling**: `scripts/backlog.sh` lints this
  file and regenerates [ROADMAP.md](ROADMAP.md) from it, with a CI gate that
  fails on a stale roadmap or a malformed item. Zero-dep, POSIX sh and awk.
  <!-- sc: prio=med size=M labels=project ver=unreleased -->
- [ ] **SC-36 — Real-stream smoke suite**: a script with the reference streams
  from AGENTS.md (Apple fMP4, Apple MPEG-TS, a public DASH manifest) that runs
  the built binary against each and fails on anything above OK. Every false
  positive so far was found this way and not by the unit tests, so the step
  should not stay manual. <!-- sc: prio=high size=S labels=tests,release -->
- [ ] **SC-48 — Coverage ratchet**: CI already prints total coverage; make it
  fail when a commit lowers it. Test-first only holds if something notices when
  it did not happen — a check merged without its test should show up in the
  build, not in a review three weeks later.
  <!-- sc: prio=med size=S labels=tests -->
- [ ] **SC-32 — goreleaser + Homebrew tap**: wire the tap upload once the
  release secret exists (the config is in place with the tap step disabled).
  <!-- sc: prio=med size=S labels=release -->
- [x] **SC-33 — Docs site**: GitHub Pages served from `docs/`, in the shape of
  the checkfleet site — one self-contained static page (hero and sample output,
  the manifest-claims table, the check matrix with worst status, install, flags,
  CI recipes, sampling cost, limits, roadmap) plus the deploy workflow. No
  Jekyll, no theme gem, no external request at render time, in keeping with the
  zero-dependency rule. <!-- sc: prio=low size=M labels=docs ver=unreleased -->
- [x] **SC-50 — Brand assets**: logo, favicon, horizontal wordmark and OG card,
  all hand-written SVG in `docs/assets/` with PNG renders for the consumers that
  cannot take vectors. The mark is the rendition ladder with one segment the
  media does not have, flagged, and the verdict beside it — the product's one
  sentence in a 32px square. Same plate and palette as checkfleet, so the two
  read as siblings. <!-- sc: prio=low size=S labels=docs ver=unreleased -->
- [ ] **SC-49 — Per-check reference pages**: one page per check behind the
  matrix — what it measures, how the threshold is applied, what a finding means
  for a viewer and what to do about it. The single page carries the summary
  well; it cannot carry that depth for thirteen checks.
  <!-- sc: prio=low size=M labels=docs -->
