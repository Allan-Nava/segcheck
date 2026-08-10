# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and
this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
