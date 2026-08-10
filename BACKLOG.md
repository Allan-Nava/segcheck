# Backlog — segcheck

Single source of truth for what is planned. Items keep a stable `SC-n` id so
commits and the CHANGELOG can reference them.

## M1 — Core: read the segments (v0.1) ✅

- [x] **SC-1 — MPEG-TS parser**: PAT/PMT, PES presentation timestamps, continuity-counter breaks, scrambling flag, PSI reassembly across packets, resync after lost sync. Zero-dep, in-tree. _(v0.1.0)_
- [x] **SC-2 — fMP4/CMAF parser**: `moov` (timescale, handler, codec, coded size from the sample entry), `moof`/`mfhd`/`tfhd`/`tfdt`/`trun` for the timeline, `encv`/`enca`/`pssh` for protection. _(v0.1.0)_
- [x] **SC-3 — H.264 SPS**: coded resolution out of the bitstream, with Exp-Golomb, emulation-prevention unescaping, scaling lists and the frame-cropping arithmetic. This is what makes "declares 1080p, codes 720p" detectable in MPEG-TS. _(v0.1.0)_
- [x] **SC-4 — HLS parser**: master and media playlists, attribute lists with quoted commas, `EXT-X-MAP` (including `BYTERANGE`), `EXT-X-BYTERANGE` with implicit offsets, `EXT-X-KEY`, `EXT-X-DISCONTINUITY`, `EXT-X-PROGRAM-DATE-TIME`, audio-only variant classification. _(v0.1.0)_
- [x] **SC-5 — DASH parser**: `SegmentTemplate` with `$Number$`/`$Time$`/`%0Nd`, `SegmentTimeline` with `@r`, `SegmentList`, `BaseURL` chains, `xs:duration`, live-edge derivation from `availabilityStartTime`. _(v0.1.0)_
- [x] **SC-6 — Packed audio**: ADTS AAC frame counting plus the ID3 `com.apple.streaming.transportStreamTimestamp` PRIV tag, which is the only timeline an audio-only rendition has. _(v0.1.0)_

## M2 — The checks (v0.1) ✅

- [x] **SC-7 — `continuity`**: undeclared gaps and overlaps between consecutive segments, PTS wraparound handled, declared discontinuities honoured; MPEG-TS packet loss. _(v0.1.0)_
- [x] **SC-8 — `duration`**: declared against real, per segment and accumulated, plus `TARGETDURATION` compliance. _(v0.1.0)_
- [x] **SC-9 — `resolution`**: coded resolution against declared `RESOLUTION`. _(v0.1.0)_
- [x] **SC-10 — `bitrate`**: measured peak and average against `BANDWIDTH`, both under- and over-declaration. _(v0.1.0)_
- [x] **SC-11 — `alignment`**: segment boundaries across renditions on a shared timeline. _(v0.1.0)_
- [x] **SC-12 — `timeline`**: DASH `SegmentTimeline` `@t` against the fragment `tfdt`. _(v0.1.0)_
- [x] **SC-13 — `tracks` / `container` / `encryption` / `ladder` / `init`**: track presence and stability, codec agreement, container sanity, declared-versus-observed protection, ladder shape. _(v0.1.0)_
- [x] **SC-14 — Output**: terminal (worst first, colour on a TTY only), JSON, ops-style markdown; `--exit-on`. _(v0.1.0)_

## M3 — Segment internals, phase two

- [ ] **SC-15 — HEVC/H.265 SPS**: coded resolution from `hvcC` and from an MPEG-TS HEVC elementary stream. Today HEVC in TS reports the codec but not the resolution, so the `resolution` check silently skips those rungs.
- [ ] **SC-16 — Keyframe alignment**: every segment must start on an IDR/IRAP. A segment that opens on a non-keyframe cannot be switched into, which is the defect behind "ABR switching stutters even though the boundaries line up". Needs slice-type inspection for H.264 and `styp`/`sap` for CMAF.
- [ ] **SC-17 — Frame rate**: measured from the timestamp deltas, against `FRAME-RATE` / `@frameRate`. Also catches a rung whose real frame rate differs from the rest of the ladder.
- [ ] **SC-18 — Audio sanity**: sample rate and channel count consistency across a rendition and against `CODECS`; silence detection is explicitly out of scope (that needs decoding).
- [ ] **SC-19 — `sidx` and `SegmentBase`**: parse the index so single-file DASH representations can be sampled at all. Today they are reported as unsupported rather than checked.
- [ ] **SC-20 — SCTE-35 / `EXT-X-DATERANGE`**: ad-break signalling present in the manifest and consistent with the segment boundaries. The check operators actually want before a live event.
- [ ] **SC-21 — MP3 packed audio**: frame-size tables so the duration can be measured, instead of recognising the container and stopping.
- [ ] **SC-22 — Encrypted-segment support with a key**: `--key` / `--key-file` for AES-128 so the content checks can run on protected streams.

## M4 — Delivery and CDN behaviour

- [ ] **SC-23 — Cache behaviour**: report `X-Cache`/`Age`/`CF-Cache-Status` per segment and flag segments served `MISS` that should be warm; a live edge that is always MISS is a real origin-load problem.
- [ ] **SC-24 — Multi-POP comparison**: run the same check through several resolvers or `--header Host:` overrides and report renditions that differ between POPs.
- [ ] **SC-25 — Live-edge watch**: `--watch` re-reads the playlist at `TARGETDURATION` and reports new-segment latency, stalls, and a live edge that stops advancing.
- [ ] **SC-26 — Byte-range support probe**: whether the origin honours `Range` at all, reported once per host rather than per segment.

## M5 — Integration

- [ ] **SC-27 — Prometheus/OTLP output**: the same findings as metrics, so a cron run feeds existing dashboards.
- [ ] **SC-28 — Slack output** (Block Kit), webhook from the environment, never on the command line.
- [ ] **SC-29 — GitHub Action**: a composite action wrapping the binary, so a repository can gate on a stream.
- [ ] **SC-30 — checkfleet module**: expose these analyses as a `stream-deep` module in [checkfleet](https://github.com/Allan-Nava/checkfleet), sharing this parser rather than reimplementing it.
- [ ] **SC-31 — Config file**: `segcheck.yml` with named targets and per-target thresholds, for checking several streams in one run.

## Release

- [ ] **SC-32 — goreleaser + Homebrew tap**: wire the tap upload once the release secret exists (the config is in place with the tap step disabled).
- [ ] **SC-33 — Docs site**: GitHub Pages with a page per check and CI recipes, in the shape of the checkfleet site.
