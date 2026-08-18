// Package output renders a Result as terminal text, JSON or an ops-style
// markdown report. Every renderer keeps the same rule: the thing you must look
// at is the first line.
package output

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Allan-Nava/segcheck/internal/finding"
)

var statusIcon = map[finding.Status]string{
	finding.OK:    "🟢",
	finding.WARN:  "🟡",
	finding.BAD:   "🔴",
	finding.ERROR: "⛔",
}

const ansiReset = "\x1b[0m"

var statusColor = map[finding.Status]string{
	finding.OK:    "\x1b[32m",
	finding.WARN:  "\x1b[33m",
	finding.BAD:   "\x1b[31m",
	finding.ERROR: "\x1b[35m",
}

// Text renders for the terminal. Findings arrive pre-sorted worst first.
func Text(res finding.Result, color bool) string {
	var b strings.Builder
	checkW, targetW := 0, 0
	for _, f := range res.Findings {
		checkW = max(checkW, len(f.Check))
		targetW = max(targetW, len(f.Target))
	}
	if targetW > 34 {
		targetW = 34
	}

	for _, f := range res.Findings {
		status := string(f.Status)
		if color {
			status = statusColor[f.Status] + status + ansiReset
			// Pad outside the escape codes so the column stays aligned.
			status += strings.Repeat(" ", 5-len(f.Status))
		} else {
			status = fmt.Sprintf("%-5s", status)
		}
		fmt.Fprintf(&b, "%s %s  %-*s  %-*s  %s\n",
			statusIcon[f.Status], status, checkW, f.Check, targetW, truncate(f.Target, targetW), ruledMessage(f))
		if f.Hint != "" && f.Status != finding.OK {
			fmt.Fprintf(&b, "%s↳ %s\n", strings.Repeat(" ", 9+checkW+2), f.Hint)
		}
	}
	if len(res.Findings) > 0 {
		b.WriteString("\n")
	}
	b.WriteString(SummaryLine(res) + "\n")
	return b.String()
}

// SummaryLine is the one-line verdict at the foot of a run.
func SummaryLine(res finding.Result) string {
	s := finding.Summarize(res.Findings)
	return fmt.Sprintf("%d checks: %d OK, %d WARN, %d BAD, %d ERROR — %d segments, %s in %s",
		len(res.Findings), s[finding.OK], s[finding.WARN], s[finding.BAD], s[finding.ERROR],
		res.Segments, humanBytes(res.Bytes), res.Duration.Round(time.Millisecond))
}

// JSON renders the whole result, for a pipeline to consume.
func JSON(res finding.Result) (string, error) {
	type wire struct {
		Source   string            `json:"source"`
		Worst    finding.Status    `json:"worst"`
		Summary  map[string]int    `json:"summary"`
		Segments int               `json:"segments"`
		Bytes    int64             `json:"bytes"`
		Started  time.Time         `json:"started"`
		Duration float64           `json:"duration_seconds"`
		Findings []finding.Finding `json:"findings"`
	}
	summary := map[string]int{}
	for k, v := range finding.Summarize(res.Findings) {
		summary[string(k)] = v
	}
	b, err := json.MarshalIndent(wire{
		Source:   res.Source,
		Worst:    finding.Worst(res.Findings),
		Summary:  summary,
		Segments: res.Segments,
		Bytes:    res.Bytes,
		Started:  res.Started,
		Duration: res.Duration.Seconds(),
		Findings: res.Findings,
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

// Markdown renders an ops-style report: what needs attention first, then the
// full table — the shape that can be pasted into an incident doc unedited.
func Markdown(res finding.Result) string {
	var b strings.Builder
	s := finding.Summarize(res.Findings)

	fmt.Fprintf(&b, "# segcheck — %s\n\n", res.Source)
	fmt.Fprintf(&b, "- **Verdict**: %s\n", finding.Worst(res.Findings))
	fmt.Fprintf(&b, "- **Run**: %s (UTC), %s\n", res.Started.UTC().Format("2006-01-02 15:04:05"), res.Duration.Round(time.Millisecond))
	fmt.Fprintf(&b, "- **Sampled**: %d segments, %s downloaded\n", res.Segments, humanBytes(res.Bytes))
	fmt.Fprintf(&b, "- **Findings**: %d OK, %d WARN, %d BAD, %d ERROR\n\n", s[finding.OK], s[finding.WARN], s[finding.BAD], s[finding.ERROR])

	var problems []finding.Finding
	for _, f := range res.Findings {
		if f.Status != finding.OK {
			problems = append(problems, f)
		}
	}
	if len(problems) == 0 {
		b.WriteString("## Needs attention\n\nNothing: every check came back OK.\n\n")
	} else {
		b.WriteString("## Needs attention\n\n")
		for _, f := range problems {
			fmt.Fprintf(&b, "- **%s** · `%s` · %s — %s\n", f.Status, f.Check, f.Target, ruledMessage(f))
			if f.Hint != "" {
				fmt.Fprintf(&b, "  - %s\n", f.Hint)
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("## All findings\n\n")
	b.WriteString("| Status | Check | Target | Message |\n|---|---|---|---|\n")
	for _, f := range res.Findings {
		msg := ruledMessage(f)
		if f.Hint != "" && f.Status != finding.OK {
			msg += "<br>↳ " + f.Hint
		}
		fmt.Fprintf(&b, "| %s %s | `%s` | %s | %s |\n", statusIcon[f.Status], f.Status, f.Check, escapePipes(f.Target), escapePipes(msg))
	}
	return b.String()
}

// ruledMessage prefixes the message with the rule a finding came from, where
// there is one. It reads as "apple:peak-vs-average: peak 5.2 Mbps is 260% of
// …", which is the order an operator wants: what rule, then why.
func ruledMessage(f finding.Finding) string {
	if f.Rule == "" {
		return f.Message
	}
	return f.Rule + ": " + f.Message
}

func escapePipes(s string) string { return strings.ReplaceAll(s, "|", "\\|") }

func truncate(s string, n int) string {
	if n <= 1 || len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	v := float64(b)
	for _, u := range []string{"KiB", "MiB", "GiB"} {
		v /= unit
		if v < unit {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f TiB", v/unit)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
