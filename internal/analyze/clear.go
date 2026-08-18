package analyze

import (
	"fmt"
	"math"
	"time"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// This is the most expensive defect segcheck reports and the quietest. Content
// that ships unprotected plays everywhere: every player accepts it, every check
// in this tool passes, the manifest declares cenc, the sample entries say encv,
// the key server hands out keys nothing uses, and nobody files a bug. The first
// signal is a rights-holder audit, months later.
//
// The only evidence is per-sample. `saiz` says how much encryption information
// each sample carries, and a sample carrying none carries none because there is
// none — so the question "is this actually encrypted" is answerable, and it is
// answerable nowhere else.
//
// A clear lead is the same measurement used deliberately: the opening seconds
// left readable so a player can start before the licence arrives. Its length is
// a choice nothing in the manifest records, so segcheck measures and reports it,
// and only judges it when --clear-lead says what was asked for.

// clearLeadTolerance is how far a measured lead may sit from the one asked for
// before it is reported. A lead is expressed in whole samples, so it lands on a
// frame boundary rather than exactly on the requested second.
const clearLeadTolerance = 100 * time.Millisecond

func checkClear(rends []*renditionData, opts Options) []finding.Finding {
	var out []finding.Finding
	for _, rd := range rends {
		if rd.err != nil {
			continue
		}
		// Only media that claims protection. Plain content is not a defect, and
		// this check is about a promise broken rather than about the absence of
		// one.
		if !claimsProtection(rd) {
			continue
		}
		label := rendLabel(rd.r)

		clear, encrypted, lead, known := sampleEncryptionOf(rd)
		if !known {
			out = append(out, finding.Finding{
				Check: "clear", Target: label, Status: finding.OK,
				Message: "the segments state no per-sample encryption, so whether the samples are really encrypted could not be checked",
				Hint:    "a fragment need not carry saiz; without it there is nothing that distinguishes protected samples from clear ones",
			})
			continue
		}
		total := clear + encrypted

		if encrypted == 0 && total > 0 {
			out = append(out, finding.Finding{
				Check: "clear", Target: label, Status: finding.BAD,
				Message: fmt.Sprintf("the manifest declares this rendition protected and all %d sampled samples are in the clear", total),
				Value:   finding.Num(float64(total)), Unit: "samples",
				Hint: "the content is shipping unprotected: it plays everywhere, nothing alerts, and the first signal is usually a rights-holder audit",
			})
			continue
		}

		leadSec, haveLead := clearLeadSeconds(rd, lead)
		switch {
		case clear == 0:
			out = append(out, finding.Finding{
				Check: "clear", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("all %d sampled samples are encrypted, with no clear lead", total),
				Value:   finding.Num(float64(total)), Unit: "samples",
			})
		case !haveLead:
			out = append(out, finding.Finding{
				Check: "clear", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("%d of %d sampled samples are in the clear; the segments state no sample duration to turn that into a lead length",
					clear, total),
				Value: finding.Num(float64(clear)), Unit: "samples",
			})
		case opts.ClearLead <= 0:
			out = append(out, finding.Finding{
				Check: "clear", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("%.1fs of clear lead, then encrypted (%d of %d sampled samples clear)", leadSec, clear, total),
				Value:   finding.Num(leadSec), Unit: "s",
				Hint: "pass --clear-lead to check this against the length that was asked for",
			})
		case math.Abs(leadSec-opts.ClearLead.Seconds()) > clearLeadTolerance.Seconds():
			out = append(out, finding.Finding{
				Check: "clear", Target: label, Status: finding.BAD,
				Message: fmt.Sprintf("%.1fs of clear lead against the %s asked for", leadSec, opts.ClearLead),
				Value:   finding.Num(leadSec), Unit: "s",
				Hint: "too long leaves content readable that was meant to be protected; too short makes a player wait for a licence before it can show anything",
			})
		default:
			out = append(out, finding.Finding{
				Check: "clear", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("%.1fs of clear lead, as asked for", leadSec),
				Value:   finding.Num(leadSec), Unit: "s",
			})
		}
	}
	return out
}

// claimsProtection reports whether anything says this rendition is protected:
// the manifest, or the sample entries themselves.
func claimsProtection(rd *renditionData) bool {
	if rd.r.KeyMethod != "" || rd.r.KeyScheme != "" || len(rd.r.DRMSystems) > 0 {
		return true
	}
	for _, sd := range rd.segs {
		if sd.seg.KeyMethod != "" {
			return true
		}
		if sd.parsed && sd.info.Encrypted() {
			return true
		}
	}
	return false
}

// sampleEncryptionOf totals the per-sample state across the sampled segments,
// in playlist order, and counts the leading run of clear samples across the
// segment boundary — a clear lead does not stop where a segment does.
func sampleEncryptionOf(rd *renditionData) (clear, encrypted, lead int, known bool) {
	stillLeading := true
	for _, sd := range rd.segs {
		if !sd.parsed {
			continue
		}
		t, ok := sd.info.Track(media.Video)
		if !ok {
			t, ok = sd.info.Track(media.Audio)
		}
		if !ok {
			continue
		}
		c, e, k := t.SampleEncryption()
		if !k {
			continue
		}
		known = true
		clear += c
		encrypted += e
		if stillLeading {
			lead += t.LeadingClearSamples
			if e > 0 {
				stillLeading = false
			}
		}
	}
	return clear, encrypted, lead, known
}

// clearLeadSeconds turns a count of leading clear samples into a length, from
// the sample duration the segments state. Without one the count cannot become a
// measurement, and inventing a frame rate to convert it would be inventing the
// answer.
func clearLeadSeconds(rd *renditionData, lead int) (float64, bool) {
	for _, sd := range rd.segs {
		if !sd.parsed {
			continue
		}
		t, ok := sd.info.Timeline()
		if !ok || t.Timescale == 0 || t.FrameDur <= 0 {
			continue
		}
		return float64(lead) * float64(t.FrameDur) / float64(t.Timescale), true
	}
	return 0, false
}
