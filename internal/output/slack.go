package output

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Allan-Nava/segcheck/internal/finding"
)

// Slack is where a scheduled check is actually read. A cron run writing text to
// a log is a report nobody sees until they go looking, which is the opposite of
// what a check on a live stream is for.
//
// Three things about the format are not decoration, and getting any of them
// wrong means the message is rejected with a bare 400 that names no block:
//
//   - a `header` block may carry `plain_text` only, capped at 150 characters,
//     and a manifest URL is routinely longer than that by itself;
//   - a text field is capped at 3000 characters, and a finding whose message
//     quotes what it found can exceed it;
//   - a message may hold 50 blocks, and a broken ladder produces far more
//     findings than that.
//
// The fourth is subtler and is the one that bites in production. Slack's mrkdwn
// reserves `&`, `<` and `>`, and a segment is bytes off the open internet: the
// `container` check's message on an origin that served an HTML error page with a
// 200 contains `<html>`. Left raw, Slack parses it as a link and the finding
// arrives mangled — the report failing precisely in the situation that produced
// it. Every value that reaches a text field goes through slackEscape.
//
// The webhook URL is never a flag. It is a credential, and a flag lands in shell
// history and in the CI log of every run; it is read from SEGCHECK_SLACK_WEBHOOK
// by the caller in cmd/segcheck instead.

const (
	// Slack's documented limits. They are not advisory: the message is dropped.
	slackHeaderLimit = 150
	slackTextLimit   = 3000
	slackBlockLimit  = 50
	// slackMaxFindings is how many findings get a block of their own. It is well
	// under the block limit on purpose: a message long enough to scroll is a
	// message read as far as the first screen, so the rest is better summarised
	// than pasted. The count of what was left out is always stated.
	//
	// It is also what keeps the payload inside slackBlockLimit without a guard:
	// header, summary, divider, at most slackMaxFindings sections, the omitted
	// count and the footer come to 20. Raise this past 45 and the arithmetic
	// stops holding, which is what TestSlack_BoundsTheMessageAndSaysWhatItOmitted
	// asserts rather than trusts.
	slackMaxFindings = 15
)

// Slack renders the result as a Block Kit message for an incoming webhook.
//
// It returns no error, for the same reason OTLP does not: every value in the
// payload is a string or an int this function built, so there is nothing
// encoding/json can refuse. A finding's own *float64 Value — the one field that
// could be NaN — is formatted into prose here rather than marshalled.
// TestSlack_IsBlockKitShapedJSON is what holds that claim up: it parses the
// output.
func Slack(res finding.Result) string {
	s := finding.Summarize(res.Findings)
	worst := finding.Worst(res.Findings)

	type text struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type block struct {
		Type     string  `json:"type"`
		Text     *text   `json:"text,omitempty"`
		Elements []*text `json:"elements,omitempty"`
	}
	payload := struct {
		Text   string  `json:"text"`
		Blocks []block `json:"blocks"`
	}{}

	// The header is the verdict and the stream, in that order, because the
	// verdict is what decides whether the rest gets read at all.
	head := fmt.Sprintf("%s segcheck: %s — %s", statusIcon[worst], worst, res.Source)
	payload.Text = slackEscape(fmt.Sprintf("segcheck %s: %s", worst, SummaryLine(res)))
	payload.Blocks = append(payload.Blocks, block{
		Type: "header",
		// plain_text is not mrkdwn, so it is not escaped — but it is still cut to
		// the limit, and cut from the left so the tail of the URL survives: the
		// rendition or playlist name at the end is what identifies the stream.
		Text: &text{Type: "plain_text", Text: truncateHeader(head, slackHeaderLimit)},
	})

	payload.Blocks = append(payload.Blocks, block{
		Type: "section",
		Text: &text{Type: "mrkdwn", Text: slackTruncate(fmt.Sprintf(
			"*%d* OK · *%d* WARN · *%d* BAD · *%d* ERROR\n%d segments, %s in %s",
			s[finding.OK], s[finding.WARN], s[finding.BAD], s[finding.ERROR],
			res.Segments, humanBytes(res.Bytes), res.Duration.Round(time.Millisecond)))},
	})

	var problems []finding.Finding
	for _, f := range res.Findings {
		if f.Status != finding.OK {
			problems = append(problems, f)
		}
	}

	payload.Blocks = append(payload.Blocks, block{Type: "divider"})

	if len(problems) == 0 {
		payload.Blocks = append(payload.Blocks, block{
			Type: "section",
			Text: &text{Type: "mrkdwn", Text: fmt.Sprintf("Nothing to look at: every check came back OK across %d findings.",
				len(res.Findings))},
		})
	}

	shown := problems
	if len(shown) > slackMaxFindings {
		shown = shown[:slackMaxFindings]
	}
	for _, f := range shown {
		body := fmt.Sprintf("%s *%s* · `%s` · %s\n%s",
			statusIcon[f.Status], f.Status, slackEscape(f.Check), slackEscape(f.Target),
			slackEscape(ruledMessage(f)))
		if f.Hint != "" {
			body += "\n_" + slackEscape(f.Hint) + "_"
		}
		payload.Blocks = append(payload.Blocks, block{
			Type: "section",
			Text: &text{Type: "mrkdwn", Text: slackTruncate(body)},
		})
	}

	// What was left out, always — a message showing the first fifteen of two
	// hundred problems reads as fifteen problems.
	if omitted := len(problems) - len(shown); omitted > 0 {
		payload.Blocks = append(payload.Blocks, block{
			Type: "context",
			Elements: []*text{{Type: "mrkdwn", Text: fmt.Sprintf(
				"and *%d* more finding(s) above OK — run segcheck with `--output markdown` for all of them", omitted)}},
		})
	}

	payload.Blocks = append(payload.Blocks, block{
		Type:     "context",
		Elements: []*text{{Type: "mrkdwn", Text: slackEscape(SummaryLine(res))}},
	})

	// HTML escaping off. encoding/json turns `&`, `<` and `>` into \u0026 and
	// friends by default, which Slack decodes correctly — but it also means a
	// payload that forgot slackEscape would still *look* escaped, so the
	// distinction this renderer depends on would be untestable. Off, the bytes on
	// the wire are the bytes Slack renders, and the escaping is either there or
	// visibly absent.
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	// The error is discarded for the reason given above, the same one OTLP
	// discards its own: every value here is a string or an int this function
	// built, so there is nothing encoding/json can reject. A branch no input can
	// reach is a branch no test can hold up, and dead defence reads as live
	// defence. Encode also appends the trailing newline.
	_ = enc.Encode(payload)
	return buf.String()
}

// slackEscape escapes the three characters Slack's mrkdwn reserves, in the one
// order that works: the ampersand first, or the ampersands introduced by the
// other two get escaped a second time and arrive as `&amp;lt;`.
func slackEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return strings.ReplaceAll(s, ">", "&gt;")
}

// slackTruncate keeps a text field inside Slack's limit. It says it cut, because
// a message that just stops mid-word reads as the check having crashed.
//
// Both this and truncateHeader budget for the marker's own length — "…" is three
// bytes, not one, and forgetting that is how a 150-byte limit becomes 152 — and
// both drop a partial rune at the cut. A subtitle check's message can quote a
// cue, so the text is not always ASCII, and half a rune is invalid UTF-8 that
// arrives as a replacement character.
func slackTruncate(s string) string {
	if len(s) <= slackTextLimit {
		return s
	}
	const note = "… (truncated)"
	return strings.ToValidUTF8(s[:slackTextLimit-len(note)], "") + note
}

// truncateHeader cuts a header from the left. The end of a manifest URL is what
// distinguishes one rendition or channel from another, so keeping the tail keeps
// the part that says which stream this is.
func truncateHeader(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	const ell = "…"
	keep := limit - len(ell)
	if keep <= 0 {
		return strings.ToValidUTF8(s[:limit], "")
	}
	return ell + strings.ToValidUTF8(s[len(s)-keep:], "")
}
