package analyze

import (
	"fmt"
	"sort"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// The common encryption scheme is the quietest mismatch in streaming. cbcs and
// cenc encrypt the same media with the same key and differ by a four-character
// code in a box: the segments are the same size, the timing is identical, the
// manifest reads perfectly, and nothing about the stream looks different. So
// MPDs get copied between presentations, and cbcs content served as cenc plays
// nowhere at all.
//
// The container states it twice, which is what makes it checkable even against
// a manifest that says nothing: `schm` carries the scheme name, and `tenc`
// carries the pattern of encrypted to clear blocks — a pattern belongs to cbcs
// and cens, and cannot appear under cenc or cbc1.

// patternSchemes are the ones whose samples are encrypted in a repeating
// pattern of blocks rather than end to end.
var patternSchemes = map[string]bool{"cbcs": true, "cens": true}

func checkScheme(rends []*renditionData) []finding.Finding {
	var out []finding.Finding
	type rung struct {
		label  string
		scheme string
	}
	var rungs []rung

	for _, rd := range rends {
		if rd.err != nil {
			continue
		}
		scheme, keyID, crypt, skip, hasPattern, kind, ok := containerScheme(rd)
		declared := rd.r.KeyScheme
		if !ok && declared == "" {
			continue // nothing claims a scheme and nothing states one
		}
		label := rendLabel(rd.r)

		if !ok {
			out = append(out, finding.Finding{
				Check: "scheme", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("the manifest declares %s and no sampled segment states a scheme to compare it with", declared),
				Hint:    "the initialisation segment carries no schm box, so segcheck could not look",
			})
			continue
		}
		rungs = append(rungs, rung{label, scheme})

		// The container against itself first: it is the half that needs no
		// manifest at all, and a contradiction here is unambiguous.
		if hasPattern && !patternSchemes[scheme] {
			out = append(out, finding.Finding{
				Check: "scheme", Target: label, Status: finding.BAD,
				Message: fmt.Sprintf("the container states scheme %s and a %d:%d crypt pattern, which only cbcs and cens use",
					scheme, crypt, skip),
				Hint: "the two halves of the container disagree about how the samples are encrypted; a decoder following either one gets noise",
			})
		} else if !hasPattern && patternSchemes[scheme] && kind == media.Video {
			// Only video. Common encryption applies pattern encryption to video
			// and full-sample encryption to audio, so a cbcs audio track states no
			// pattern and is right not to — requiring one reported Axinom's own
			// cbcs vector as broken on its audio rung.
			out = append(out, finding.Finding{
				Check: "scheme", Target: label, Status: finding.BAD,
				Message: fmt.Sprintf("the container states scheme %s on a video track and no crypt pattern, which %s applies to video", scheme, scheme),
				Hint:    "without a pattern the samples are encrypted end to end, which is what cenc and cbc1 do and what a cbcs decoder will not expect",
			})
		}

		switch {
		case declared == "":
			out = append(out, finding.Finding{
				Check: "scheme", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("the container states scheme %s; the manifest declares none to compare it with", scheme),
			})
		case declared != scheme:
			out = append(out, finding.Finding{
				Check: "scheme", Target: label, Status: finding.BAD,
				Message: fmt.Sprintf("the media is encrypted with %s and the manifest declares %s", scheme, declared),
				Hint: "the two schemes encrypt the same media with the same key and differ by a box field, so nothing else about the stream looks wrong — " +
					"and a player that prepares for the declared one decodes noise",
			})
		default:
			// The key id is not a secret — it names which key, not what it is —
			// and quoting it is what lets an operator match the rung against an
			// entry in their key server without another tool.
			msg := fmt.Sprintf("encrypted with %s, as declared", scheme)
			if keyID != "" {
				msg = fmt.Sprintf("encrypted with %s under key %s, as declared", scheme, keyID)
			}
			out = append(out, finding.Finding{
				Check: "scheme", Target: label, Status: finding.OK,
				Message: msg,
			})
		}
	}

	// And across the ladder. A player negotiates one scheme with the key system
	// before it plays anything, and cannot switch into a rung using the other.
	seen := map[string][]string{}
	for _, r := range rungs {
		seen[r.scheme] = append(seen[r.scheme], r.label)
	}
	if len(seen) > 1 {
		schemes := make([]string, 0, len(seen))
		for s := range seen {
			schemes = append(schemes, s)
		}
		sort.Strings(schemes)
		var parts []string
		for _, s := range schemes {
			parts = append(parts, fmt.Sprintf("%s on %s", s, joinAnd(seen[s])))
		}
		out = append(out, finding.Finding{
			Check: "scheme", Target: "ladder", Status: finding.BAD,
			Message: fmt.Sprintf("the ladder mixes encryption schemes: %s", joinAnd(parts)),
			Value:   finding.Num(float64(len(seen))), Unit: "schemes",
			Hint: "a player negotiates one scheme with the key system before it plays anything, and cannot switch into a rung that uses the other",
		})
	}
	return out
}

// containerScheme is what this rendition's segments state about how they are
// protected. ok is false when nothing stated a scheme at all, which is not the
// same as "unprotected" and must not be compared against anything.
func containerScheme(rd *renditionData) (scheme, keyID string, crypt, skip int, hasPattern bool, kind media.TrackKind, ok bool) {
	for _, sd := range rd.segs {
		if !sd.parsed {
			continue
		}
		for _, t := range sd.info.Tracks {
			if t.Protection == "" {
				continue
			}
			crypt, skip, hasPattern = t.CryptPattern()
			return t.Protection, t.KeyID, crypt, skip, hasPattern, t.Kind, true
		}
	}
	// An HLS rendition states its scheme in EXT-X-KEY rather than in a box, and
	// SAMPLE-AES-CTR is cenc by another name.
	for _, sd := range rd.segs {
		switch sd.seg.KeyMethod {
		case "SAMPLE-AES-CTR":
			return "cenc", "", 0, 0, false, media.Other, true
		case "SAMPLE-AES":
			return "cbcs", "", 0, 0, false, media.Other, true
		}
	}
	return "", "", 0, 0, false, media.Other, false
}
