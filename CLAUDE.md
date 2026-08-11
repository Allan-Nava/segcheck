# CLAUDE.md — segcheck

`segcheck` (`github.com/Allan-Nava/segcheck`) downloads HLS/DASH media segments
and compares the **media** against the **manifest's claims**. One static Go
binary, zero dependencies: MPEG-TS, fMP4/CMAF, H.264 SPS and ADTS are all parsed
in-tree with the standard library. `cmd/segcheck` is the CLI, `internal/manifest`
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
7. **Real streams before the tag**: Apple's fMP4 and MPEG-TS reference streams
   plus a public DASH manifest must all come back with **zero findings above
   OK**. Every false positive found so far was found this way, not by the unit
   tests. (Automating this is SC-36.)
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
  asserted against; start here before touching a parser
- `internal/analyze/checks.go` — every check lives here; `analyze.go` does the
  manifest load, rendition pick, segment fan-out
- `docs/index.html` — the GitHub Pages site
  (<https://allan-nava.github.io/segcheck/>), one self-contained static page
  with inline CSS/JS and no external request at render time; brand assets are
  hand-written SVG in `docs/assets/`, PNGs are renders of them. A check, flag or
  default that changes moves the page in the same commit as the README row
- `.goreleaser.yaml` — release build; the Homebrew tap step is present but
  disabled until the secret exists (SC-32)
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
