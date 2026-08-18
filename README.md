<p align="center">
  <img src="docs/assets/logo.svg" alt="segcheck" width="96" height="96">
</p>

<h1 align="center">segcheck</h1>

<p align="center"><strong>Check what your HLS/DASH segments <em>actually contain</em> — not just what the manifest claims.</strong></p>

<p align="center"><a href="https://allan-nava.github.io/segcheck/">allan-nava.github.io/segcheck</a></p>

<p align="center">
  <a href="https://github.com/Allan-Nava/segcheck/releases"><img alt="Latest release" src="https://img.shields.io/github/v/tag/Allan-Nava/segcheck?label=release&sort=semver&color=10b981"></a>
  <a href="https://github.com/Allan-Nava/segcheck/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/Allan-Nava/segcheck/actions/workflows/ci.yml/badge.svg"></a>
  <a href="LICENSE"><img alt="License: PolyForm Noncommercial 1.0.0" src="https://img.shields.io/badge/license-PolyForm%20Noncommercial%201.0.0-f59e0b"></a>
  <img alt="Go" src="https://img.shields.io/github/go-mod/go-version/Allan-Nava/segcheck?color=10b981">
  <img alt="Zero dependencies" src="https://img.shields.io/badge/dependencies-0-10b981">
</p>

---

**segcheck downloads media segments, parses their bytes, and compares the media against the manifest.** Point it at a master playlist or an MPD and it reports the defects a manifest cannot express: segments that do not join up, a 1080p rung that codes 720p, declared durations that drift from the real ones, renditions that are not on a shared timeline.

```
$ segcheck check https://cdn.example/master.m3u8

🔴 BAD    continuity  1080p seg 412    gap of +512ms: previous segment ends at 824.512s, this one starts at
                                       825.024s, with no EXT-X-DISCONTINUITY
                     ↳ the player has nothing to show for this interval: expect a stall or a skip
🔴 BAD    resolution  1080p            manifest declares 1920x1080, the bitstream codes 1280x720
                     ↳ the rendition is not the resolution the ladder promises
🟡 WARN   bitrate     720p seg 38      segment peaks at 3.10 Mbps but BANDWIDTH declares 2.40 Mbps (+29%)
                     ↳ BANDWIDTH must be an upper bound: under-declaring makes players choose a rendition
                       their connection cannot sustain
🟢 OK     alignment   ladder           renditions aligned at 6 shared segment indexes (tolerance 100ms)

21 checks: 17 OK, 1 WARN, 3 BAD, 0 ERROR — 18 segments, 24.5 MiB in 4.1s
```

## Why this exists

Every HLS/DASH checker reads the manifest. The manifest is a set of *claims*, and the interesting failures are the ones where the claims and the media disagree:

| The manifest says | segcheck reads the segments and answers |
|---|---|
| `#EXTINF:6.000` | Is the media really 6.000s, or 5.184s and drifting? |
| `RESOLUTION=1920x1080` | What does the H.264 SPS / the `avc1` sample entry actually code? |
| Segment 41 follows segment 40 | Does 41's first timestamp equal 40's last one, or is there a 512ms hole? |
| `BANDWIDTH=2400000` | What is the measured peak segment bitrate? |
| Four renditions | Do their segment boundaries land on the same timeline, so ABR switching is seamless? |
| `<S t="360000" d="180000"/>` | Does the fragment's `tfdt` agree with the `@t` the MPD promised? |

None of those questions can be answered without downloading the bytes.

**No ffmpeg, no ffprobe, no cgo, no dependencies.** The MPEG-TS, ISO-BMFF/CMAF, H.264 SPS and ADTS parsers are all in-tree, standard library only. `segcheck` is one static binary that runs from your laptop, a cron job, or CI.

## Install

```bash
# Homebrew (macOS only — Homebrew on Linux does not support casks)
brew install --cask allan-nava/tap/segcheck

# Go — every platform, including Linux Homebrew users
go install github.com/Allan-Nava/segcheck/cmd/segcheck@latest

# Docker (linux/amd64 and linux/arm64)
docker run --rm ghcr.io/allan-nava/segcheck:latest check https://cdn.example/master.m3u8

# Or grab a binary from the releases page
```

**macOS, if you download an archive rather than using Homebrew:** the binaries are
not yet Developer ID signed or notarised, so Gatekeeper kills them on first run —
and from a terminal it does that silently, with exit 137 and no message, which
looks like a broken build rather than a signing gap. Clear the quarantine flag:

```bash
xattr -d com.apple.quarantine ./segcheck
```

The cask does this for you (SC-65). Real notarisation is SC-94.

The image is the static binary and a CA bundle: no shell, no package manager,
non-root, and the same build as the release archive for that version.
[docs/running-in-containers.md](docs/running-in-containers.md) has Compose and
Kubernetes `CronJob` recipes.

## Usage

```bash
# The whole ladder, six segments per rendition
segcheck check https://cdn.example/master.m3u8

# A live stream: sample the live edge, which is what a joining viewer gets
segcheck check https://cdn.example/live.m3u8 --from edge --segments 12

# DASH is the same command
segcheck check https://cdn.example/manifest.mpd

# A low-latency ladder: fetch two segments' worth of EXT-X-PART parts and
# check they reconstruct the segments a normal player would fetch
segcheck check https://cdn.example/ll.m3u8 --parts 2

# Run Apple's HLS Authoring Specification over the measurements — opt-in,
# because a conformance rule you cannot turn off is a wall of findings
segcheck check https://cdn.example/master.m3u8 --profile apple

# Watch the live edge for two minutes: a packager that stopped publishing
# serves a flawless playlist, and only a second look tells the two apart
segcheck check https://cdn.example/live.m3u8 --watch 2m

# Keep it cheap on a big ladder: top and bottom rungs plus a spread between
segcheck check https://cdn.example/master.m3u8 --renditions 3 --segments 4

# A report to paste into an incident doc
segcheck check https://cdn.example/master.m3u8 --output markdown > report.md

# Gate CI on the result
segcheck check https://cdn.example/master.m3u8 --exit-on bad

# An AES-128 stream: without the key every content check is blind
segcheck check https://cdn.example/master.m3u8 --key-env SEGCHECK_KEY
segcheck check https://cdn.example/master.m3u8 --key-file /run/secrets/content.key

# Or let segcheck fetch it from the URI EXT-X-KEY names — off by default,
# because pointing a checker at a key server is a request to a system that
# logs, rate-limits and sometimes bills
segcheck check https://cdn.example/master.m3u8 --fetch-keys
```

**The content key is never a flag value.** `--key-env` names an environment
variable and `--key-file` a path; a key in `argv` lands in shell history, in the
process list and in every CI log that echoes its own invocation, and unlike a
password it cannot be rotated without re-encrypting the content. The same rule
governs credentials, which go in `--header` values the caller reads from the
environment.

**Exit status is 0 whenever the check ran**, findings or not — a check that ran *is* a success. Use `--exit-on warn|bad|error` when you want a non-zero exit for CI.

## What it checks

| Check | What it compares | Worst status |
|---|---|---|
| `manifest` | The manifest parses, and what shape it is | BAD |
| `fetch` | Every sampled segment is reachable; `Range` requests are honoured | ERROR |
| `init` | The `EXT-X-MAP` / DASH initialisation segment is available | ERROR |
| `container` | The bytes are the media they claim to be — an origin error page served with a 200 lands here | BAD |
| `continuity` | Each segment starts where the previous one ended; MPEG-TS continuity-counter breaks (packet loss) | BAD |
| `duration` | Declared `EXTINF` / `@d` against the real media duration, per segment and accumulated; `TARGETDURATION` compliance | BAD |
| `timeline` | A DASH `SegmentTimeline` `@t` against the fragment's `tfdt` | BAD |
| `bitrate` | Measured peak and average against the declared `BANDWIDTH`, in both directions | WARN |
| `resolution` | The coded resolution in the bitstream against the declared `RESOLUTION` | BAD |
| `keyframe` | Every segment carries a random access point — an IDR, an HEVC IRAP, an fMP4 sync sample — so it can be switched into at all | BAD |
| `framerate` | The measured frame rate against the declared `FRAME-RATE` / `@frameRate`, and rungs whose rate is unrelated to the rest of the ladder | WARN |
| `audio` | The sampling rate, channel layout and codec the media actually carries against `CHANNELS` / `@audioSamplingRate` / `AudioChannelConfiguration` / `CODECS`, and any of them changing part-way through a rendition | BAD |
| `captions` | CEA-608/708 caption data actually in the video bitstream — an SEI message or a CMAF `c608`/`c708` track — against `CLOSED-CAPTIONS` / DASH `Accessibility` | BAD |
| `adbreak` | SCTE-35 splice points in the media — a TS signalling PID or an `emsg` — against `EXT-X-DATERANGE`/`EXT-X-CUE-OUT`/DASH `EventStream`, and whether either lands on a segment boundary at all | BAD |
| `subtitles` | WebVTT and TTML segments actually parse, and their cue times overlap the segment the manifest put them in — the `X-TIMESTAMP-MAP` drift no manifest checker can see | BAD |
| `tracks` | Expected video/audio present, codecs match `CODECS`, track layout stable across segments | BAD |
| `alignment` | Segment boundaries across renditions, so ABR switching does not glitch | BAD |
| `encryption` | Declared protection against what the segments carry, whether a supplied key actually decrypts them, and — for SAMPLE-AES and CENC, which protect the samples and not the container — which half of the tool could run at all | BAD |
| `ladder` | Duplicate rungs, inverted rungs, dangling `AUDIO` groups, missing `CODECS` | BAD |
| `iframe` | `EXT-X-I-FRAME-STREAM-INF` trick-play ranges fetched and read: each must resolve to a keyframe and to nothing else, and the rung must sit on the same timeline as the video it belongs to | BAD |
| `profile` | With `--profile apple`, the measurable subset of Apple's HLS Authoring Specification: peak-to-average bit rate, consistent segment durations, an IDR at every segment start, average bit rate against the tier the resolution implies, and a frame rate constant within a rung and shared across the ladder. Every finding names its rule and puts the measured value beside the limit | WARN |
| `dvr` | The oldest segment the DVR window still promises — DASH `timeShiftBufferDepth`, or an HLS playlist's own span — is fetched and parsed. It is the only promise nobody collects on purpose | BAD |
| `availability` | A dynamic MPD's live edge is computed, not listed: the `UTCTiming` source it names is honoured, the skew against this machine's clock is reported, and the computed edge is probed against what the origin actually has in both directions | BAD |
| `pdt` | `EXT-X-PROGRAM-DATE-TIME` against the media: that it never goes backwards, that it advances at the media's rate, and that every rung of the ladder maps the same media to the same wall clock | BAD |
| `parts` | Low-latency `EXT-X-PART` parts fetched and compared with the segment they make up: contiguity, coverage, `INDEPENDENT=YES` against the real sync sample, and measured length against `PART-TARGET` | BAD |
| `watch` | With `--watch`, whether the live edge actually advances: new-segment latency, a stall, and a packager that stopped publishing | BAD |

A dynamic MPD is checked against its own clock, not this machine's. `availabilityStartTime` makes the live edge a computed claim rather than a listed one, so segcheck resolves the `UTCTiming` source the MPD names — `http-head`, `http-iso`, `http-xsdate` and `direct` — re-expands the segment list against that answer, and reports the skew. Without it a laptop thirty seconds fast asks for segments the packager has not published and reports the resulting 404s as a broken origin. NTP sources are named and skipped rather than silently ignored.

A trick-play rung is checked as media rather than skipped. `EXT-X-I-FRAME-STREAM-INF` declares byte ranges the packager computed, and nothing in the manifest says whether they landed on keyframes; when they did not, the scrub preview shows a grey frame and it gets reported as a player bug. The rung is deliberately kept out of every other check — one picture where two seconds of media are expected is a hole in the timeline, a duration mismatch and a bitrate ten times the declared, which is the same trap subtitle renditions sprang.

Low-latency HLS is read as well as delivered: `EXT-X-PART-INF`, `EXT-X-SERVER-CONTROL`, `EXT-X-PART` and `EXT-X-PRELOAD-HINT`. The parts of a segment are fetched and compared with the segment itself, because a packager muxes the two separately and they can disagree — and when they do, a viewer on the low-latency path gets different media from one fetching whole segments. The preload hint is parsed and never fetched: it is designed to block until the media exists, and a checker that blocked on one would hang instead of reporting.

Containers understood: **MPEG-TS** (PAT/PMT, PES timestamps, continuity counters, H.264 and HEVC/H.265 parameter sets for the real resolution), **fragmented MP4 / CMAF** (`moov` for timescale, codec and coded size; `mvex`/`trex` defaults; `tfdt`/`trun` for the timeline; `sidx` for single-file DASH, addressed by byte range), **packed audio** (ADTS AAC and MPEG-1/2 audio, with the ID3 `transportStreamTimestamp` that gives audio-only renditions a timeline), and **WebVTT and TTML/IMSC** subtitle segments. Audio format is read where each container actually states it: the `AudioSampleEntry` in fMP4, the `dac3`/`dec3` box for AC-3 and E-AC-3 (whose `channelcount` field is not to be trusted), and the ADTS header everywhere else.

## When *not* to use it

- **You want continuous monitoring with history and alerting.** Use Prometheus and friends; segcheck is a check you run, not a service. Pair it with [checkfleet](https://github.com/Allan-Nava/checkfleet) if you want its findings alongside the rest of your fleet.
- **You need per-frame QoE or visual quality metrics** (VMAF, PSNR, artefact detection). segcheck reads structure and timing, never decoded pixels.
- **Your segments are full-segment AES-128 encrypted and you cannot supply the key.** Those bytes are opaque by design; segcheck says so and skips the content checks rather than pretending.
- **You want to validate against every clause of RFC 8216.** segcheck checks what it can verify against the media, not the whole spec.

## Sampling, and what it costs

segcheck downloads real media, so it is worth knowing what it will pull. Per run: `renditions × segments` segments, plus one initialisation segment per rendition. Defaults (6 segments, all renditions) against a five-rung 1080p ladder is roughly 100–200 MB. Trim it with `--renditions` and `--segments`; a capped `--renditions` always keeps the top and bottom rungs, because that is where ladder defects concentrate.

`--parts N` adds the parts of the N newest sampled segments per rendition — a segment's worth of bytes each, split across many small requests. `--parts 0` switches the low-latency checks off; a stream that publishes no `EXT-X-PART` never pays for them either way.

`--watch` adds manifests, not media: one request per selected rendition every re-read interval — `TARGETDURATION` in HLS, `minimumUpdatePeriod` in DASH — for as long as you asked for. The segments are downloaded once, at the start.

Requests carry a `segcheck/<version>` User-Agent so you can tell a check apart from real traffic in your access logs.

## Development

```bash
go test ./...              # unit + end-to-end tests against a synthetic origin
go test -race ./...
go build ./cmd/segcheck
```

There are no binary fixtures in this repository. `internal/media/mediatest` *builds* MPEG-TS, fMP4 and ADTS segments with known timestamps and resolutions, so every parser is tested against media whose correct answer is known by construction — and the end-to-end tests plant one defect at a time in an `httptest` origin and assert segcheck finds exactly that, and that a clean stream produces nothing above OK.

## Roadmap

[ROADMAP.md](ROADMAP.md) is the milestone view — what is in flight, what is
next, what is deliberately later. It is generated from [BACKLOG.md](BACKLOG.md),
which is the single source of truth for planned work: every item has a stable
`SC-n` id that commits and CHANGELOG entries reference.

Shipped in **v0.2.0**: HEVC coded resolution, keyframe alignment, frame rate,
`sidx`/`SegmentBase`, AV1/VP9, parser fuzzing. In flight for **v0.3.0**:
everything in a stream that is not the video track — audio layout and rate,
CEA-608/708 captions, WebVTT and TTML subtitles, SCTE-35 ad signalling, and
AES-128 so the content checks run on a protected stream at all. For **v0.4.0**:
the live edge and CDN behaviour, and wallclock correctness —
`EXT-X-PROGRAM-DATE-TIME` and the DVR window checked against the media rather
than taken at their word. For **v0.5.0**: metrics and chat outputs, and the
measurable subset of the Apple HLS Authoring Spec and DASH-IF IOP behind an
opt-in `--profile`. For **v0.6.0**: content protection in depth — which DRM
system the segments really carry, and media that is in the clear while the
manifest says it is protected, neither of which needs a key. (`cenc` versus
`cbcs` is already read, since the bitstream checks had to know which half of
the media they could see.)

## License

[PolyForm Noncommercial 1.0.0](LICENSE) — free for noncommercial use. For commercial use, open an issue.
