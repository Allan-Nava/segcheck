# AGENTS.md — segcheck

Working rules for this repository, for any agent or tool. This file is
canonical: [CLAUDE.md](CLAUDE.md) is a denser operating brief over the same
rules, plus the repo map, the known traps and the backlog tooling. When the two
disagree, this file wins and CLAUDE.md gets fixed.

## What this tool is

`segcheck` downloads HLS/DASH media segments and compares the **media** against
the **manifest's claims**. Every feature has to earn its place against that
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

- **No binary fixtures.** `internal/media/mediatest` builds MPEG-TS, fMP4 and
  ADTS segments with known timestamps, durations and resolutions. A parser test
  asserts against a value that is known by construction, which is also how the
  SPS reader is validated: `mediatest.SPS` is the writer, and the round trip
  catches bit-level mistakes.
- **Every check needs two tests**: one that plants the defect and asserts it is
  found *and correctly attributed*, and the clean-stream test
  (`TestRun_CleanStreamHasNoProblems`) which asserts nothing above OK. A checker
  that cries wolf on healthy media is worse than no checker.
- **Assert on the finding's content**, not just its presence: the target names
  the right segment, the message names the defect, `Value` carries the
  measurement. That is what stops a check from being right by accident.
- `go test -race ./...` must pass: the segment fan-out writes into a shared
  slice.
- Before releasing, run the binary against real streams. Apple's fMP4 and
  MPEG-TS reference streams and a public DASH stream must all come back with
  **zero findings above OK** — every false positive found so far was found that
  way, not by the unit tests.

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
