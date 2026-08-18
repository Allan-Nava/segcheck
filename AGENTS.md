# AGENTS.md — segcheck

Working rules for this repository, for any agent or tool. This file is
canonical: [CLAUDE.md](CLAUDE.md) is a denser operating brief over the same
rules, plus the repo map, the known traps and the backlog tooling. When the two
disagree, this file wins and CLAUDE.md gets fixed.

## What this tool is

`segcheck` downloads HLS/DASH media segments and compares the **media** against
the **manifest's claims**. One static Go binary, zero dependencies — every
container, bitstream and cipher it reads is handled in-tree with the standard
library. Every feature has to earn its place against that
sentence. A check that only reads the manifest belongs in
[checkfleet](https://github.com/Allan-Nava/checkfleet), not here.

## Hard rules

- **Zero dependencies.** `go.mod` has no `require` block and `go.sum` stays
  empty; CI enforces both. No ffmpeg, no ffprobe, no cgo, no shelling out. When
  a format needs parsing, it gets parsed in-tree with the standard library. This
  is what makes the binary static, small, auditable and CI-friendly — it is the
  product's main differentiator, not an aesthetic preference.
- **Exit 0 whenever the check ran.** Findings are output, not failure. Only
  `--exit-on` produces a non-zero exit. A crash or a usage error exits non-zero.
- **Worst findings first**, always, in every renderer. The first line is the
  thing the operator must look at.
- **Never invent a measurement.** If the timescale is unknown, the duration is
  unknown — return `(0, false)` and let the check stay quiet. A confident wrong
  number is worse than no number.
- **A limit of this tool is not a defect in the stream.** Unsupported containers,
  encrypted segments and unexpandable representations get an honest OK-level or
  ERROR finding that says *segcheck* could not look, never a BAD that sends
  someone hunting a phantom.
- **No secrets on the command line.** Credentials go in `--header` values read
  from the environment by the caller, never into a flag that lands in shell
  history or a CI log.

## Testing

- **Test first, always — TDD is not optional here.** The test that plants the
  defect is written, and watched fail, before the code that finds it. Red,
  green, refactor. A test written after the implementation tends to assert what
  the code happens to do rather than what the stream means, and that is exactly
  how a check ends up green against media it was supposed to flag. This applies
  to parsers (write the `mediatest` builder and the failing assertion first), to
  checks, to the CLI, and to tooling — SC-45 is in the backlog ahead of SC-43
  for that reason.
- **No binary fixtures.** `internal/media/mediatest` builds every shape this tool
  reads — MPEG-TS, fMP4/CMAF, ADTS and MPEG audio, WebVTT and TTML, caption SEI and
  `c608` tracks, SCTE-35 sections and `emsg` boxes, AES-128 ciphertext — with
  timestamps, durations and resolutions known by construction. That is also how the
  bitstream readers are validated: `mediatest.SPS` is the writer, `media.ParseSPS` the
  reader, and the round trip catches bit-level mistakes.
- **A round trip cannot catch a shared misreading.** A builder and a reader written
  from the same misunderstanding of a layout agree with each other perfectly. Where an
  outside authority exists, assert against it: RFC 3602's vectors for AES-CBC, bytes
  decoded by hand from the specification for an SCTE-35 `segmentation_descriptor`.
- **Every check needs two tests**: one that plants the defect and asserts it is
  found *and correctly attributed*, and the clean-stream test
  (`TestRun_CleanStreamHasNoProblems`) which asserts nothing above OK. A checker
  that cries wolf on healthy media is worse than no checker.
- **Assert on the finding's content**, not just its presence: the target names
  the right segment, the message names the defect, `Value` carries the
  measurement. That is what stops a check from being right by accident.
- `go test -race ./...` must pass: the segment fan-out writes into a shared
  slice.
- `scripts/coverage.sh check` must pass: coverage is a ratchet against
  `scripts/coverage.floor`, and `update` refuses to lower it.
- **Run the binary against real streams — this is where the design gets corrected.**
  Automated as the smoke suite:

  ```
  go build -o /tmp/segcheck ./cmd/segcheck
  SEGCHECK_BIN=/tmp/segcheck go test -tags smoke -run TestSmokeReferenceStreams ./internal/analyze/ -v
  ```

  The assertion is **not** "nothing above OK". Apple's advanced example legitimately
  over-declares BANDWIDTH and ships an inverted ladder, so each stream carries a
  baseline of the checks allowed to exceed OK, plus a list of checks that must not fall
  silent. A finding outside the baseline is a regression; so is a check that stops
  speaking, and that second half is what catches a parser which quietly stopped
  reading. Every false positive this project has shipped was found here rather than by
  a unit test — and going looking for a real encrypted stream is what found three more.

## Releases

Every release is a tagged `vX.Y.Z` with a new `CHANGELOG.md` section (Keep a
Changelog). `minor` for new checks, parsers or flags; `patch` for fixes. Never
`git push` — that is the maintainer's call.

## Backlog

`BACKLOG.md` is the single source of truth for what is planned, with stable
`SC-n` ids. New ideas go there rather than into scattered TODO comments, and
commits reference the id. Ids never change and never get reused: an item is
retired by being marked done, not by being deleted.

Each item carries a trailing `<!-- sc: prio=… size=… labels=… -->` comment
(invisible when rendered) and each milestone a `<!-- ms: target=… phase=… -->`.
`ROADMAP.md` is **generated** from all of that — never edit it by hand:

```sh
scripts/backlog.sh lint      # ids, metadata, milestones
scripts/backlog.sh roadmap   # regenerate ROADMAP.md — commit the result
scripts/backlog.sh next      # what to pick up
```

CI runs `lint` and `check` (roadmap freshness), so a backlog edit that skips the
regeneration fails the build.

## Site and brand

`docs/` is the GitHub Pages site (<https://allan-nava.github.io/segcheck/>),
deployed by `.github/workflows/pages.yml`. It is **one self-contained static
page**: no Jekyll, no theme gem, no build step, and no external request at
render time — the zero-dependency rule applies to the site too. Inline the CSS
and the JS, and keep every asset in `docs/assets/`.

The page states what the tool does; when a check, a flag or a default changes,
`docs/index.html` moves in the same commit as the `README.md` row and the
`--help` text. A site that describes a check that behaves differently is a bug
report waiting to be filed.

Brand assets are hand-written SVG in `docs/assets/` — `logo.svg` (the mark),
`favicon.svg`, `logo-wordmark.svg` (horizontal lockup) and `og-card.svg`, with
PNG renders alongside for consumers that cannot take vectors. The mark is the
rendition ladder with one missing segment flagged in amber and the verdict
beside it; the plate and the emerald/slate palette are shared with checkfleet
on purpose. Edit the SVG and re-render the PNG — never the other way round.
