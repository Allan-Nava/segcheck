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

## M9 — Wallclock and DVR correctness <!-- ms: target=v0.4.0 phase=later -->

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
- [ ] **SC-36 — Real-stream smoke suite**: a script with the reference streams
  from AGENTS.md (Apple fMP4, Apple MPEG-TS, a public DASH manifest) that runs
  the built binary against each and fails on anything above OK. Every false
  positive so far was found this way and not by the unit tests, so the step
  should not stay manual. <!-- sc: prio=high size=S labels=tests,release -->
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
- [ ] **SC-64 — `scripts/backlog.sh` has no tests**: the test-first rule covers
  tooling and this is the one place it was not applied — which is how it shipped
  a generator that split a table row in three the first time an item title
  contained a `|` (SC-63). A shell test fixture with a small BACKLOG, asserting
  the generated ROADMAP for pipes, em dashes, done-versus-open ordering, and the
  lint failures (duplicate id, gap in the sequence, bad metadata) that are
  currently only ever exercised by hand.
  <!-- sc: prio=med size=S labels=tests,project -->
- [ ] **SC-48 — Coverage ratchet**: CI already prints total coverage; make it
  fail when a commit lowers it. Test-first only holds if something notices when
  it did not happen — a check merged without its test should show up in the
  build, not in a review three weeks later.
  <!-- sc: prio=med size=S labels=tests -->
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
