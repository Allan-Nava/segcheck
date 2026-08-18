package analyze

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// Which DRM systems can unlock a stream is claimed twice: by the manifest, in
// DASH ContentProtection or HLS KEYFORMAT, and by the initialisation segment,
// in its pssh boxes. Only the second is what a player is actually handed.
//
// A ladder whose MPD advertises PlayReady and whose CMAF init carries only a
// Widevine pssh plays perfectly on Chrome and dies on Xbox and Edge. Every
// segment fetches, every timing check passes, the manifest reads impeccably,
// and the failure arrives weeks later as "your app is broken on our devices"
// from a platform certification team — which is why this belongs in a tool that
// reads the media rather than in one that reads the manifest.

func checkDRM(rends []*renditionData) []finding.Finding {
	var out []finding.Finding
	for _, rd := range rends {
		if rd.err != nil {
			continue
		}
		declared := declaredDRMSystems(rd)
		present, known := presentDRMSystems(rd)
		if len(declared) == 0 && len(present) == 0 {
			continue // nothing claims protection and nothing carries it
		}
		label := rendLabel(rd.r)

		if !known {
			// The samples are protected and no initialisation segment could be
			// read, so the pssh boxes were never seen. That is a hole in the
			// coverage, not a verdict about the stream.
			out = append(out, finding.Finding{
				Check: "drm", Target: label, Status: finding.ERROR,
				Message: fmt.Sprintf("the manifest declares %s and no initialisation segment could be read to check it",
					joinAnd(namesOf(declared))),
				Hint: "without the init there are no pssh boxes to compare against",
			})
			continue
		}

		// A system whose key-acquisition data the manifest carries itself has
		// nothing for the init to be missing. DASH-IF's guidance puts the pssh in
		// the MPD, and every real multi-DRM vector does — demanding one in the
		// init reported Axinom's reference stream as missing both its systems.
		inManifest := lowerAll(rd.r.DRMInManifest)
		missing := difference(declared, append(systemIDs(present), inManifest...))
		extra := difference(systemIDs(present), declared)

		// When the init carries no pssh at all, it is not the acquisition path:
		// the data is in the manifest, or behind the key URI an EXT-X-KEY names.
		// Reporting every declared system as missing would be reporting the
		// packaging convention rather than a defect.
		if len(present) == 0 {
			out = append(out, finding.Finding{
				Check: "drm", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("%s declared; the initialisation segment carries no pssh, so the key data comes from the manifest or the key URI",
					joinAnd(namesOf(declared))),
				Value: finding.Num(float64(len(declared))), Unit: "systems",
			})
			continue
		}

		if len(missing) > 0 {
			out = append(out, finding.Finding{
				Check: "drm", Target: label, Status: finding.BAD,
				Message: fmt.Sprintf("the manifest declares %s and the initialisation segment carries no such pssh (it carries %s)",
					joinAnd(namesOf(missing)), presentList(present)),
				Value: finding.Num(float64(len(missing))), Unit: "systems",
				Hint: "devices that use one of the missing systems cannot obtain a key: the stream plays everywhere else and fails only on them",
			})
		}
		if len(extra) > 0 {
			out = append(out, finding.Finding{
				Check: "drm", Target: label, Status: finding.WARN,
				Message: fmt.Sprintf("the initialisation segment carries %s, which the manifest does not declare",
					joinAnd(namesOf(extra))),
				Value: finding.Num(float64(len(extra))), Unit: "systems",
				Hint: "a player chooses a system from the manifest, so these keys go unused; it is the shape of a key rotation applied to the media and not to the manifest",
			})
		}
		if len(missing) == 0 && len(extra) == 0 && len(present) > 0 {
			out = append(out, finding.Finding{
				Check: "drm", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("the initialisation segment carries %s, as declared", presentList(present)),
				Value:   finding.Num(float64(len(present))), Unit: "systems",
			})
		}
	}
	return out
}

func lowerAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, strings.ToLower(s))
	}
	return out
}

// declaredDRMSystems is what the manifest promises for this rendition: DASH
// ContentProtection urn:uuid entries, or the KEYFORMAT of the keys its segments
// name. Both are normalised to bare lower-case UUIDs so they compare with a
// pssh directly.
func declaredDRMSystems(rd *renditionData) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, s := range rd.r.DRMSystems {
		add(strings.ToLower(s))
	}
	for _, sd := range rd.segs {
		add(manifest.DRMSystemForKeyFormat(sd.seg.KeyFormat))
	}
	sort.Strings(out)
	return out
}

// presentDRMSystems is what the sampled segments' initialisation actually
// advertises. known is false when no segment could be parsed at all, so the
// absence of a pssh means nobody looked rather than none is there.
func presentDRMSystems(rd *renditionData) (systems []media.DRMSystem, known bool) {
	seen := map[string]bool{}
	for _, sd := range rd.segs {
		if !sd.parsed {
			continue
		}
		known = true
		for _, s := range sd.info.DRMSystems {
			if seen[s.SystemID] {
				continue
			}
			seen[s.SystemID] = true
			systems = append(systems, s)
		}
	}
	sort.Slice(systems, func(i, j int) bool { return systems[i].SystemID < systems[j].SystemID })
	return systems, known
}

func systemIDs(systems []media.DRMSystem) []string {
	out := make([]string, 0, len(systems))
	for _, s := range systems {
		out = append(out, s.SystemID)
	}
	return out
}

// namesOf renders a list of system UUIDs the way an operator argues about them:
// by name where segcheck knows one, by UUID where it does not.
func namesOf(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, media.DRMSystemFor(id).Label())
	}
	return out
}

func presentList(systems []media.DRMSystem) string {
	if len(systems) == 0 {
		return "none"
	}
	out := make([]string, 0, len(systems))
	for _, s := range systems {
		out = append(out, s.Label())
	}
	return joinAnd(out)
}

// difference is the members of a that are not in b.
func difference(a, b []string) []string {
	inB := map[string]bool{}
	for _, s := range b {
		inB[s] = true
	}
	var out []string
	for _, s := range a {
		if !inB[s] {
			out = append(out, s)
		}
	}
	return out
}
