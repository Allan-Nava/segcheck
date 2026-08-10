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
# Homebrew
brew install Allan-Nava/tap/segcheck

# Go
go install github.com/Allan-Nava/segcheck/cmd/segcheck@latest

# Docker (linux/amd64 and linux/arm64)
docker run --rm ghcr.io/allan-nava/segcheck:latest check https://cdn.example/master.m3u8

# Or grab a binary from the releases page
```

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

# Keep it cheap on a big ladder: top and bottom rungs plus a spread between
segcheck check https://cdn.example/master.m3u8 --renditions 3 --segments 4

# A report to paste into an incident doc
segcheck check https://cdn.example/master.m3u8 --output markdown > report.md

# Gate CI on the result
segcheck check https://cdn.example/master.m3u8 --exit-on bad
```

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
| `tracks` | Expected video/audio present, codecs match `CODECS`, track layout stable across segments | BAD |
| `alignment` | Segment boundaries across renditions, so ABR switching does not glitch | BAD |
| `encryption` | Declared protection against what the segments carry | BAD |
| `ladder` | Duplicate rungs, inverted rungs, dangling `AUDIO` groups, missing `CODECS` | BAD |

Containers understood: **MPEG-TS** (PAT/PMT, PES timestamps, continuity counters, H.264 SPS for the real resolution), **fragmented MP4 / CMAF** (`moov` for timescale, codec and coded size; `tfdt`/`trun` for the timeline), and **packed audio** (ADTS AAC with the ID3 `transportStreamTimestamp` that gives audio-only renditions a timeline).

## When *not* to use it

- **You want continuous monitoring with history and alerting.** Use Prometheus and friends; segcheck is a check you run, not a service. Pair it with [checkfleet](https://github.com/Allan-Nava/checkfleet) if you want its findings alongside the rest of your fleet.
- **You need per-frame QoE or visual quality metrics** (VMAF, PSNR, artefact detection). segcheck reads structure and timing, never decoded pixels.
- **Your segments are full-segment AES-128 encrypted and you cannot supply the key.** Those bytes are opaque by design; segcheck says so and skips the content checks rather than pretending.
- **You want to validate against every clause of RFC 8216.** segcheck checks what it can verify against the media, not the whole spec.

## Sampling, and what it costs

segcheck downloads real media, so it is worth knowing what it will pull. Per run: `renditions × segments` segments, plus one initialisation segment per rendition. Defaults (6 segments, all renditions) against a five-rung 1080p ladder is roughly 100–200 MB. Trim it with `--renditions` and `--segments`; a capped `--renditions` always keeps the top and bottom rungs, because that is where ladder defects concentrate.

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

In flight for **v0.2.0**: HEVC coded resolution, keyframe alignment, frame rate,
`sidx`/`SegmentBase`, AV1/VP9, parser fuzzing. For **v0.3.0**: audio, captions,
subtitles and SCTE-35, plus a `FROM scratch` container image with multi-arch
GHCR publication and signed artefacts. For **v0.4.0**: the live edge and CDN
behaviour, and wallclock correctness — `EXT-X-PROGRAM-DATE-TIME` and the DVR
window checked against the media rather than taken at their word.

## License

[PolyForm Noncommercial 1.0.0](LICENSE) — free for noncommercial use. For commercial use, open an issue.
