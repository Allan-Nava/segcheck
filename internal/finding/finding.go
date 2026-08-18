// Package finding is the result model: one Finding is one observation about one
// target, a Result aggregates a run. The severity order and the "worst first"
// sort are the two rules the whole tool is built on.
package finding

import (
	"sort"
	"time"
)

// Status of a single finding. Severity order: OK < WARN < BAD < ERROR.
type Status string

const (
	OK    Status = "OK"
	WARN  Status = "WARN"
	BAD   Status = "BAD"
	ERROR Status = "ERROR" // the check itself could not run against the target
)

var severity = map[Status]int{OK: 0, WARN: 1, BAD: 2, ERROR: 3}

// Severity is the numeric rank of s in the order OK < WARN < BAD < ERROR.
func Severity(s Status) int { return severity[s] }

// AtLeast reports whether s is at or above threshold. An empty threshold is
// satisfied by anything, since severity[""] is the zero value — callers that
// mean "no threshold at all" must test for "" themselves.
func AtLeast(s, threshold Status) bool { return severity[s] >= severity[threshold] }

// Finding is one observation about one target.
//
// Check is the analysis that produced it (continuity, duration, ladder, …) and
// Target identifies what was looked at — a rendition, a segment, the master
// playlist. Value/Unit carry the measurement when there is one, so machine
// consumers do not have to parse Message.
type Finding struct {
	Check string `json:"check"`
	// Rule identifies the conformance rule a finding comes from, for the checks
	// that have one — `apple:peak-vs-average` and its like. It is a field rather
	// than a prefix on Message for the same reason Value is: a machine consumer
	// must never have to parse prose, and a rule an operator wants to argue with
	// has to be quotable.
	Rule    string   `json:"rule,omitempty"`
	Target  string   `json:"target"`
	Status  Status   `json:"status"`
	Message string   `json:"message"`
	Value   *float64 `json:"value,omitempty"`
	Unit    string   `json:"unit,omitempty"`
	Hint    string   `json:"hint,omitempty"`
}

// Num returns a pointer to v, for setting Finding.Value inline.
func Num(v float64) *float64 { return &v }

// Result aggregates the findings of a run.
type Result struct {
	Source   string        `json:"source"`
	Findings []Finding     `json:"findings"`
	Started  time.Time     `json:"started"`
	Duration time.Duration `json:"duration_ns"`
	// Segments is how many media segments were actually downloaded and parsed.
	Segments int `json:"segments"`
	// Bytes is how much media was downloaded.
	Bytes int64 `json:"bytes"`
}

// Summarize counts findings per status.
func Summarize(fs []Finding) map[Status]int {
	out := map[Status]int{OK: 0, WARN: 0, BAD: 0, ERROR: 0}
	for _, f := range fs {
		out[f.Status]++
	}
	return out
}

// Worst returns the highest severity present, or OK for no findings.
func Worst(fs []Finding) Status {
	worst := OK
	for _, f := range fs {
		if AtLeast(f.Status, worst) {
			worst = f.Status
		}
	}
	return worst
}

// SortWorstFirst orders findings by descending severity, then by check and
// target so the output of two identical runs is byte-identical.
func SortWorstFirst(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if a, b := severity[fs[i].Status], severity[fs[j].Status]; a != b {
			return a > b
		}
		if fs[i].Check != fs[j].Check {
			return fs[i].Check < fs[j].Check
		}
		return fs[i].Target < fs[j].Target
	})
}
