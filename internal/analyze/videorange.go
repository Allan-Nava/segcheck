package analyze

import (
	"fmt"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// A player reads VIDEO-RANGE — or DASH's CICP transfer descriptor — to decide
// whether to ask the display for HDR, before it has decoded a single frame. So
// the claim is acted on earlier than any other in this tool, and it is acted on
// differently by different devices.
//
// A PQ rung whose samples are really BT.709 is tone-mapped twice by every device
// that believes the manifest and once by every device that believes the
// bitstream: the two halves of the audience see different pictures of the same
// stream, and neither half sees an error. The other direction is worse for the
// viewer — media that really is PQ, declared SDR, is never given an HDR display
// to render on, and comes out washed out and flat everywhere.

func checkVideoRange(rends []*renditionData) []finding.Finding {
	var out []finding.Finding
	for _, rd := range rends {
		if rd.err != nil || rd.r.Kind == manifest.Text || rd.r.Kind == manifest.Audio {
			continue
		}
		declaredRange := rd.r.VideoRange
		declaredTransfer := rd.r.Transfer
		if declaredRange == "" && declaredTransfer == 0 {
			continue // the manifest makes no dynamic-range claim
		}
		label := rendLabel(rd.r)

		colour, ok := colourOf(rd)
		if !ok {
			out = append(out, finding.Finding{
				Check: "videorange", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("the manifest declares %s and the media states no colour description to compare it with",
					declaredClaim(declaredRange, declaredTransfer)),
				Hint: "neither the sequence parameter set's VUI nor a colr box carries one, so segcheck could not look",
			})
			continue
		}

		// DASH states the code point itself, so compare numbers where there are
		// numbers: it is the same registry the bitstream uses and no mapping
		// stands between the two.
		if declaredTransfer != 0 && declaredTransfer != colour.Transfer {
			out = append(out, finding.Finding{
				Check: "videorange", Target: label, Status: finding.BAD,
				Message: fmt.Sprintf("the manifest declares transfer characteristic %d (%s) and the media codes %d (%s)",
					declaredTransfer, rangeLabel(declaredTransfer), colour.Transfer, colour.Label()),
				Value: finding.Num(float64(colour.Transfer)),
				Hint:  "a device that trusts the manifest and one that trusts the bitstream tone-map this differently, so the audience sees two different pictures",
			})
			continue
		}

		measuredRange := manifest.VideoRangeForTransfer(colour.Transfer)
		if declaredRange != "" && measuredRange != "" && declaredRange != measuredRange {
			out = append(out, finding.Finding{
				Check: "videorange", Target: label, Status: finding.BAD,
				Message: fmt.Sprintf("the manifest declares VIDEO-RANGE=%s and the media codes %s (transfer characteristic %d)",
					declaredRange, colour.Label(), colour.Transfer),
				Value: finding.Num(float64(colour.Transfer)),
				Hint: "a player asks the display for the declared range before it decodes anything: the wrong answer is tone-mapped twice, " +
					"or never given the HDR display it needed",
			})
			continue
		}
		out = append(out, finding.Finding{
			Check: "videorange", Target: label, Status: finding.OK,
			Message: fmt.Sprintf("%s, as declared: transfer characteristic %d over primaries %d",
				colour.Label(), colour.Transfer, colour.Primaries),
			Value: finding.Num(float64(colour.Transfer)),
		})
	}
	return out
}

// colourOf is the colour description this rendition's video really states.
func colourOf(rd *renditionData) (media.ColourDescription, bool) {
	for _, sd := range rd.segs {
		if !sd.parsed {
			continue
		}
		t, ok := sd.info.Track(media.Video)
		if !ok {
			continue
		}
		if c, ok := t.Colour(); ok {
			return c, true
		}
	}
	return media.ColourDescription{}, false
}

// declaredClaim renders whichever form the manifest used, for a message that
// quotes the manifest rather than segcheck's normalisation of it.
func declaredClaim(rangeName string, transfer int) string {
	switch {
	case rangeName != "" && transfer != 0:
		return fmt.Sprintf("%s (transfer characteristic %d)", rangeName, transfer)
	case rangeName != "":
		return "VIDEO-RANGE=" + rangeName
	default:
		return fmt.Sprintf("transfer characteristic %d", transfer)
	}
}

func rangeLabel(transfer int) string {
	if n := media.TransferName(transfer); n != "" {
		return n
	}
	return manifest.VideoRangeForTransfer(transfer)
}
