package output

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Allan-Nava/segcheck/internal/finding"
)

// blockKit is enough of the payload to assert against. Slack ignores what it
// does not know, so a test that only reads these fields is testing the contract
// rather than the struct.
type blockKit struct {
	Text   string `json:"text"`
	Blocks []struct {
		Type string `json:"type"`
		Text *struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"text"`
		Elements []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"elements"`
	} `json:"blocks"`
}

func parseSlack(t *testing.T, s string) blockKit {
	t.Helper()
	var got blockKit
	if err := json.Unmarshal([]byte(s), &got); err != nil {
		t.Fatalf("the payload is not JSON (%v):\n%s", err, s)
	}
	return got
}

// A webhook payload that Slack rejects is a report nobody receives, and the
// rejection is a 400 with no detail about which block was wrong. So the limits
// are asserted here rather than discovered in production.
func TestSlack_IsBlockKitShapedJSON(t *testing.T) {
	got := parseSlack(t, Slack(sample()))

	if len(got.Blocks) == 0 {
		t.Fatal("no blocks in the payload")
	}
	if got.Blocks[0].Type != "header" {
		t.Errorf("the first block is %q, want a header: the verdict has to be the thing you see", got.Blocks[0].Type)
	}
	// A header may only carry plain_text. Slack rejects mrkdwn there outright.
	if h := got.Blocks[0].Text; h == nil || h.Type != "plain_text" {
		t.Errorf("the header text is %+v, want plain_text", h)
	}
	// The fallback: what a notification preview and a screen reader get.
	if got.Text == "" {
		t.Error("no top-level text: the notification preview and any screen reader get nothing")
	}
	for i, b := range got.Blocks {
		if b.Text != nil && len(b.Text.Text) > slackTextLimit {
			t.Errorf("block %d (%s) carries %d characters, over Slack's %d limit",
				i, b.Type, len(b.Text.Text), slackTextLimit)
		}
	}
}

// Worst first, in every renderer. A Slack message is read from the top and
// often only the top, so the ordering matters more here than anywhere.
func TestSlack_TheWorstFindingIsTheFirstOneShown(t *testing.T) {
	got := parseSlack(t, Slack(sample()))

	var body string
	for _, b := range got.Blocks[1:] {
		if b.Text != nil {
			body += b.Text.Text + "\n"
		}
	}
	bad := strings.Index(body, "continuity")
	warn := strings.Index(body, "bitrate")
	if bad < 0 || warn < 0 {
		t.Fatalf("both findings should appear:\n%s", body)
	}
	if bad > warn {
		t.Errorf("the WARN is shown above the BAD:\n%s", body)
	}
}

// Slack's mrkdwn reserves `&`, `<` and `>`, and a segment is bytes off the open
// internet: a parser that met an HTML error page reports its message with the
// tags in it. Left raw, Slack reads `<...>` as a link and the finding arrives
// mangled or not at all — which is the report failing silently in exactly the
// situation it was needed.
func TestSlack_EscapesTheThreeCharactersSlackReserves(t *testing.T) {
	res := finding.Result{
		Source: "https://cdn.example/master.m3u8?a=1&b=2",
		Findings: []finding.Finding{{
			Check: "container", Target: "720p seg 3", Status: finding.BAD,
			Message: `not a segment: <html><body>404 & gone</body></html>`,
			Hint:    "the origin served an error page with a 200",
		}},
	}
	out := Slack(res)

	if strings.Contains(out, "<html>") {
		t.Error("a raw <html> survived into the payload: Slack will read it as a link")
	}
	for _, want := range []string{"&lt;html&gt;", "&amp; gone"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is not in the payload:\n%s", want, out)
		}
	}
	// And the escaping must not have broken the JSON.
	parseSlack(t, out)
}

// A header over 150 characters is rejected, and a manifest URL is routinely
// longer than that on its own.
func TestSlack_TheHeaderFitsSlacksLimit(t *testing.T) {
	res := sample()
	res.Source = "https://cdn.example/" + strings.Repeat("very-long-path-segment/", 20) + "master.m3u8"

	got := parseSlack(t, Slack(res))
	h := got.Blocks[0].Text
	if h == nil {
		t.Fatal("no header text")
	}
	if len(h.Text) > slackHeaderLimit {
		t.Errorf("the header is %d characters, over Slack's %d limit: %q", len(h.Text), slackHeaderLimit, h.Text)
	}
}

// Slack takes at most 50 blocks and drops the whole message otherwise. A broken
// ladder produces far more findings than that, so the payload has to be bounded
// — and has to say what it left out, because a report that silently shows the
// first twenty of two hundred problems reads as twenty problems.
func TestSlack_BoundsTheMessageAndSaysWhatItOmitted(t *testing.T) {
	res := sample()
	res.Findings = nil
	for i := 0; i < 200; i++ {
		res.Findings = append(res.Findings, finding.Finding{
			Check: "continuity", Target: "720p seg " + strings.Repeat("9", i%5+1),
			Status: finding.BAD, Message: "gap of +512ms",
		})
	}
	out := Slack(res)
	got := parseSlack(t, out)

	if len(got.Blocks) > slackBlockLimit {
		t.Errorf("%d blocks, over Slack's limit of %d", len(got.Blocks), slackBlockLimit)
	}
	if !strings.Contains(out, "more") {
		t.Errorf("the payload does not say how many findings it left out:\n%s", out)
	}
}

// A clean run is the common case and must not read like a problem report.
func TestSlack_ACleanRunSaysSo(t *testing.T) {
	res := sample()
	res.Findings = []finding.Finding{
		{Check: "ladder", Target: "master", Status: finding.OK, Message: "4 video renditions"},
	}
	out := Slack(res)

	if !strings.Contains(out, "every check came back OK") {
		t.Errorf("a clean run does not say so:\n%s", out)
	}
	parseSlack(t, out)
}

// A single finding whose message is enormous — a manifest echoed back, say —
// must not take the whole message over the per-block limit.
func TestSlack_TruncatesAnEnormousMessage(t *testing.T) {
	res := sample()
	res.Findings = []finding.Finding{{
		Check: "manifest", Target: "master", Status: finding.BAD,
		Message: strings.Repeat("x", slackTextLimit*2),
	}}

	got := parseSlack(t, Slack(res))
	for i, b := range got.Blocks {
		if b.Text != nil && len(b.Text.Text) > slackTextLimit {
			t.Errorf("block %d carries %d characters, over the %d limit", i, len(b.Text.Text), slackTextLimit)
		}
	}
}

// The rule a finding came from is part of what it says, the same way it is in
// every other renderer.
func TestSlack_NamesTheRuleAFindingComesFrom(t *testing.T) {
	res := sample()
	res.Findings = []finding.Finding{{
		Check: "bitrate", Target: "720p", Status: finding.WARN,
		Rule: "apple:peak-vs-average", Message: "peak is 260% of average",
	}}

	if out := Slack(res); !strings.Contains(out, "apple:peak-vs-average") {
		t.Errorf("the rule is not in the payload:\n%s", out)
	}
}

// The two truncators, directly. Both take a budget rather than reading the
// constant, so the awkward cases — a limit smaller than the marker, a cut that
// lands inside a multi-byte rune — are theirs to get right whatever the caller
// passes.
func TestTruncateHeader(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		limit    int
		want     string
	}{
		{"under the limit is untouched", "segcheck: OK", 150, "segcheck: OK"},
		// The tail survives, because the end of a URL is what names the stream.
		// 15 bytes of budget: three for the marker, twelve for the tail.
		{"keeps the tail", "aaaaaaaaaa/master.m3u8", 15, "…/master.m3u8"},
		// A budget smaller than the ellipsis itself: no room to mark the cut, so
		// it does not pretend to. It must still not exceed the budget.
		{"a limit under the marker", "abcdefghij", 2, "ab"},
	} {
		got := truncateHeader(tc.in, tc.limit)
		if got != tc.want {
			t.Errorf("%s: truncateHeader(%q, %d) = %q, want %q", tc.name, tc.in, tc.limit, got, tc.want)
		}
		if len(got) > tc.limit && len(tc.in) > tc.limit {
			t.Errorf("%s: result is %d bytes, over the %d budget", tc.name, len(got), tc.limit)
		}
	}
}

// A cut that lands mid-rune produces invalid UTF-8, which arrives as a
// replacement character. A subtitle check quotes cue text, so the input is not
// always ASCII.
func TestSlackTruncate_DropsAPartialRune(t *testing.T) {
	// "é" is two bytes; a wall of them guarantees the cut lands inside one.
	long := strings.Repeat("é", slackTextLimit)
	got := slackTruncate(long)

	if len(got) > slackTextLimit {
		t.Errorf("result is %d bytes, over the %d limit", len(got), slackTextLimit)
	}
	if !strings.Contains(got, "truncated") {
		t.Error("the result does not say it was cut")
	}
	if !utf8.ValidString(got) {
		t.Error("the cut left invalid UTF-8, which renders as a replacement character")
	}
	if short := "é"; slackTruncate(short) != short {
		t.Error("a string under the limit was altered")
	}
}
