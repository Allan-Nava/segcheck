// Package baseline compares a run against a saved one, so segcheck can answer
// "did this get worse?" and not only "is this bad?".
//
// A single run is a snapshot, and a snapshot cannot tell a stream that has
// always over-declared its BANDWIDTH from one that started doing it this
// morning. Most of what an operator wants to know is a *change*: a rung that
// lost a third of its bitrate, a rendition that stopped being published, a
// check that was clean last week.
//
// What can be compared is decided by what is stable between two runs, and most
// of a Finding is not.
//
//   - A Target may name a rendition or a segment, and on a live stream the
//     segments are different every time. `720p seg 38` is never `720p seg 41`,
//     so pairing segment-scoped findings would report the entire sample as
//     vanished and a new one as appeared, on every run. Only targets the run
//     lists as renditions are treated as stable, which is why
//     finding.Result.Renditions exists.
//   - A Message restates the measurement for any finding that has one, so
//     diffing the prose would fire on every wobble. For a finding with *no*
//     measurement the message is the whole observation — `resolution` says
//     "coded 1280x720 matches the declared resolution" and nothing else — so
//     there, and only there, the message is what gets compared. That is how a
//     resolution that moved is caught in the case where neither run is a defect
//     because each agrees with its own manifest.
//   - A measurement is measured, not computed. The same rendition sampled twice
//     gives slightly different numbers, so a change has to clear a tolerance or
//     the diff reports every run and gets turned off within a day.
//
// The findings this produces are ordinary findings on a `baseline` check, so
// they sort with everything else, render in every format, and `--exit-on bad`
// gates on them without knowing they are different in kind.
package baseline

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/Allan-Nava/segcheck/internal/finding"
)

// Tolerance is how far a measurement may move before it is a change, as a
// fraction. 10% is wider than the run-to-run noise on a segment sample and far
// narrower than anything worth calling a regression: the item this was written
// for names a rung losing 30%.
const Tolerance = 0.10

// Compare reports what changed between a saved run and the current one.
//
// It returns nothing at all when nothing changed, which is the common case and
// the point: a diff that always has something to say is a diff nobody reads.
func Compare(base, cur finding.Result) []finding.Finding {
	var out []finding.Finding
	out = append(out, renditionChanges(base, cur)...)
	out = append(out, checkSilences(base, cur)...)
	out = append(out, findingChanges(base, cur)...)
	finding.SortWorstFirst(out)
	return out
}

// renditionChanges reports the ladder gaining or losing a rung.
//
// A rung that disappears costs every viewer who was on it — they are moved to
// whatever is left, which is either more bandwidth than their connection has or
// fewer pixels than their screen. A rung appearing is someone adding one.
func renditionChanges(base, cur finding.Result) []finding.Finding {
	was, is := set(base.Renditions), set(cur.Renditions)
	var out []finding.Finding

	for _, name := range sorted(was) {
		if is[name] {
			continue
		}
		out = append(out, finding.Finding{
			Check: "baseline", Target: name, Status: finding.BAD,
			Message: fmt.Sprintf("rendition %q is no longer in the ladder: it was in the baseline and is gone", name),
			Hint:    "every viewer who was on this rung has been moved to another one — more bandwidth than they have, or fewer pixels than their screen",
		})
	}
	for _, name := range sorted(is) {
		if was[name] {
			continue
		}
		out = append(out, finding.Finding{
			Check: "baseline", Target: name, Status: finding.OK,
			Message: fmt.Sprintf("rendition %q is new since the baseline", name),
		})
	}
	return out
}

// checkSilences reports a check that spoke in the baseline and says nothing now.
//
// This is the failure mode the whole project treats as the worst one, because
// nothing reads exactly like a clean bill of health. ERROR rather than BAD: the
// stream is not known to be broken, the coverage is known to have a hole.
//
// The other direction is not a hole. A check that is new is almost always a
// newer segcheck, and reporting that as missing coverage would make every
// version bump look like a regression.
func checkSilences(base, cur finding.Result) []finding.Finding {
	was, is := checks(base.Findings), checks(cur.Findings)
	var out []finding.Finding
	for _, name := range sorted(was) {
		if is[name] {
			continue
		}
		out = append(out, finding.Finding{
			Check: "baseline", Target: name, Status: finding.ERROR,
			Message: fmt.Sprintf("the %s check reported in the baseline and reports nothing now", name),
			Hint:    "a check that falls silent looks exactly like a check that passed; either the stream stopped carrying what it looks at, or the check stopped reading it",
		})
	}
	return out
}

// findingChanges compares what each check said about each rendition.
//
// The unit is the check and the *rendition*, not the check and the target, and
// that distinction is the one thing here worth getting right. A check routinely
// reports an OK about a rendition and a BAD about one of its segments, so when a
// stream breaks the rendition-level finding is replaced by a segment-level one:
// `continuity/720p` becomes `continuity/720p seg 2`. Pairing exact targets loses
// exactly the regression a gate exists for — the old pair looks deleted and the
// new one is segment-scoped and skipped. Folding a rendition's segments into it
// and comparing the worst status keeps the signal while leaving the segment
// number out of the key, so the sample moving still costs nothing.
//
// Measurements and messages stay on exact targets: only a rendition-level
// finding carries a number that means the same thing twice.
func findingChanges(base, cur finding.Result) []finding.Finding {
	rends := sorted(union(base.Renditions, cur.Renditions))
	wasWorst, isWorst := worstPerRendition(base.Findings, rends), worstPerRendition(cur.Findings, rends)
	stillThere := set(cur.Renditions)

	var out []finding.Finding
	moved := map[key]bool{}
	for _, k := range sortedStatusKeys(wasWorst) {
		b := wasWorst[k]
		n, ok := isWorst[k]
		if !ok {
			// A rendition that vanished is already said once by renditionChanges,
			// and repeating it per check would bury it. A rendition still present
			// whose check went quiet is caught by checkSilences when the check went
			// quiet everywhere, and is otherwise a check that simply has nothing to
			// say about this rung — not a claim worth making.
			continue
		}
		if b == n || !stillThere[k.target] {
			continue
		}
		moved[k] = true
		out = append(out, statusChange(k, b, n, worstMessage(cur.Findings, k, rends)))
	}

	// Exact-target pairs, for the measurement and the prose.
	stable := union(base.Renditions, cur.Renditions)
	was, is := byKey(base.Findings, stable), byKey(cur.Findings, stable)
	for _, k := range sortedKeys(was) {
		b, n := was[k], is[k]
		if n == nil || moved[k] {
			// The status change already said what happened; a value delta beside it
			// would be the same event twice.
			continue
		}
		if d, ok := valueChange(*b, *n); ok {
			out = append(out, d)
			continue
		}
		// Only where there is no measurement to compare instead.
		if b.Value == nil && n.Value == nil && b.Message != n.Message {
			out = append(out, finding.Finding{
				Check: "baseline", Target: k.target, Status: finding.WARN,
				Message: fmt.Sprintf("%s changed what it reports about %s: %q, was %q",
					k.check, k.target, n.Message, b.Message),
				Hint: "this finding carries no measurement, so its message is the whole observation — it changed, which for a check like resolution means the media did",
			})
		}
	}
	return out
}

// worstPerRendition is the worst status each check reported about each rendition,
// counting what it said about that rendition's segments as being about it.
func worstPerRendition(fs []finding.Finding, rends []string) map[key]finding.Status {
	out := map[key]finding.Status{}
	for _, f := range fs {
		rend, ok := renditionOf(f.Target, rends)
		if !ok {
			continue
		}
		k := key{check: f.Check, target: rend}
		if cur, seen := out[k]; !seen || finding.Severity(f.Status) > finding.Severity(cur) {
			out[k] = f.Status
		}
	}
	return out
}

// worstMessage is the message of the finding that set a rendition's worst
// status, so the diff can quote what the check actually said rather than
// paraphrase it.
func worstMessage(fs []finding.Finding, k key, rends []string) string {
	best, msg := finding.OK, ""
	for _, f := range fs {
		rend, ok := renditionOf(f.Target, rends)
		if !ok || rend != k.target || f.Check != k.check {
			continue
		}
		if msg == "" || finding.Severity(f.Status) > finding.Severity(best) {
			best, msg = f.Status, f.Message
		}
	}
	return msg
}

// renditionOf attributes a target to a rendition. A segment's target is its
// rendition's label followed by " seg N" (analyze.segLabel), so the prefix is a
// documented construction rather than a guess — and it is only ever matched
// against the rendition names the run itself listed.
func renditionOf(target string, rends []string) (string, bool) {
	for _, r := range rends {
		if target == r || strings.HasPrefix(target, r+" ") {
			return r, true
		}
	}
	return "", false
}

func statusChange(k key, from, to finding.Status, msg string) finding.Finding {
	status, hint := finding.OK, "a finding that improved since the baseline"
	if finding.Severity(to) > finding.Severity(from) {
		status = finding.BAD
		hint = "this is the change a regression gate exists to catch: it was acceptable in the baseline and is not now"
	}
	detail := ""
	if msg != "" {
		detail = ": " + msg
	}
	return finding.Finding{
		Check: "baseline", Target: k.target, Status: status,
		Message: fmt.Sprintf("%s on %s went %s → %s%s", k.check, k.target, from, to, detail),
		Hint:    hint,
	}
}

// sortedStatusKeys is sortedKeys for the per-rendition status map.
func sortedStatusKeys(m map[key]finding.Status) []key {
	out := make([]key, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortKeys(out)
	return out
}

func union(a, b []string) map[string]bool {
	out := set(a)
	for _, s := range b {
		out[s] = true
	}
	return out
}

// valueChange reports a measurement that moved beyond the tolerance.
//
// Both sides must carry a value and the *same* unit: a check that changed how it
// expresses a number has not measured a different stream, and reporting a
// conversion from bps to Mbps as a 99.9999% collapse would be a lie about the
// media. A baseline of zero is not a percentage either — anything over nothing is
// infinite — so it is reported as an absolute move.
func valueChange(b, n finding.Finding) (finding.Finding, bool) {
	if b.Value == nil || n.Value == nil || b.Unit != n.Unit {
		return finding.Finding{}, false
	}
	from, to := *b.Value, *n.Value
	if from == to {
		return finding.Finding{}, false
	}
	if from == 0 {
		return finding.Finding{
			Check: "baseline", Target: n.Target, Status: finding.WARN,
			Message: fmt.Sprintf("%s on %s moved from 0 to %s %s", n.Check, n.Target, trim(to), n.Unit),
			Value:   finding.Num(to), Unit: n.Unit,
		}, true
	}
	pct := (to - from) / math.Abs(from) * 100
	if math.Abs(pct) < Tolerance*100 {
		return finding.Finding{}, false
	}
	direction := "up"
	if pct < 0 {
		direction = "down"
	}
	return finding.Finding{
		Check: "baseline", Target: n.Target, Status: finding.WARN,
		Message: fmt.Sprintf("%s on %s is %s %.1f%%: %s %s, was %s %s",
			n.Check, n.Target, direction, math.Abs(pct), trim(to), n.Unit, trim(from), b.Unit),
		Value: finding.Num(round1(pct)), Unit: "%",
		Hint: "measured against the saved baseline, not against the manifest",
	}, true
}

// key is one comparable observation: which check, about which stable target.
type key struct {
	check  string
	target string
}

// byKey indexes the findings whose target is stable enough to pair across runs.
// A check may report more than once on one target — several segments of one
// rendition — but those targets are segment-scoped and excluded here, so the
// last one wins and there is nothing to lose.
func byKey(fs []finding.Finding, stable map[string]bool) map[key]*finding.Finding {
	out := map[key]*finding.Finding{}
	for i := range fs {
		if !stable[fs[i].Target] {
			continue
		}
		out[key{check: fs[i].Check, target: fs[i].Target}] = &fs[i]
	}
	return out
}

func checks(fs []finding.Finding) map[string]bool {
	out := map[string]bool{}
	for _, f := range fs {
		out[f.Check] = true
	}
	return out
}

func set(ss []string) map[string]bool {
	out := map[string]bool{}
	for _, s := range ss {
		out[s] = true
	}
	return out
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedKeys keeps the output stable across runs: a map range order would make
// two identical comparisons print in different orders.
func sortedKeys(m map[key]*finding.Finding) []key {
	out := make([]key, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortKeys(out)
	return out
}

// sortKeys orders by check, then by target — the order a report reads in.
func sortKeys(ks []key) {
	sort.Slice(ks, func(i, j int) bool {
		if ks[i].check != ks[j].check {
			return ks[i].check < ks[j].check
		}
		return ks[i].target < ks[j].target
	})
}

// trim formats a measurement without the trailing zeros a float prints, since
// these land in prose an operator reads.
func trim(v float64) string { return fmt.Sprintf("%g", round1(v)) }

func round1(v float64) float64 { return math.Round(v*10) / 10 }
