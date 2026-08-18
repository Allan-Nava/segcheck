package analyze

import (
	"fmt"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
)

// A conformance profile is a verdict layer over measurements segcheck has taken
// anyway: Apple's HLS Authoring Specification and DASH-IF IOP are largely lists
// of constraints on the media, and by this point nearly all of them are already
// numbers in a renditionData.
//
// Profiles are opt-in, and that is the whole reason this file exists separately
// from the checks. A conformance rule with no way to turn it off turns a run
// that was clean yesterday into a wall of findings today, on a stream nobody
// changed — so `none` is the default and it means none. Every rule names itself
// so an operator can argue with it, and reports the measured value beside the
// limit, because "fails rule 3.4" without a number is unactionable.
//
// The scope guard is the same line AGENTS.md draws: only rules the media can
// arbitrate. "Provide a 192 kbps audio rendition" is a manifest-only assertion
// and belongs in checkfleet. Every rule here must be able to fail on media a
// manifest-only reader would call fine.

// Conformance profiles.
const (
	ProfileNone   = "none"
	ProfileApple  = "apple"
	ProfileDASHIF = "dash-if"
)

// profileRule is one conformance rule: an identity an operator can quote and a
// function that turns the run into findings.
type profileRule struct {
	// id is stable and segcheck's own — `apple:peak-vs-average` — deliberately
	// not the specification's section number. Apple renumbers the document
	// between revisions, and a finding citing "3.4" against the wrong revision
	// is worse than one citing nothing: the requirement itself is quoted in the
	// message instead, which is the part worth arguing with.
	id  string
	run func(ctx profileContext) []finding.Finding
}

// profileContext is everything a rule may look at.
type profileContext struct {
	pl    manifest.Playlist
	rends []*renditionData
	opts  Options
}

// checkProfile runs the selected rule set.
func checkProfile(pl manifest.Playlist, rends []*renditionData, opts Options) []finding.Finding {
	switch opts.Profile {
	case "", ProfileNone:
		return nil
	}

	target := shortTarget(pl.URL)
	rules, name, implemented := profileRules(opts.Profile)
	if !implemented {
		return []finding.Finding{{
			Check: "profile", Target: target, Status: finding.OK,
			Message: fmt.Sprintf("%s is not implemented yet: no conformance rule ran", name),
			Hint:    "this is a limit of segcheck, not a verdict about the stream",
		}}
	}

	ctx := profileContext{pl: pl, rends: rends, opts: opts}
	var out []finding.Finding
	for _, r := range rules {
		for _, f := range r.run(ctx) {
			f.Check = "profile"
			f.Rule = r.id
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		// Silence would read as "no rules exist". An operator who asked for
		// conformance needs to know they got it.
		return []finding.Finding{{
			Check: "profile", Target: target, Status: finding.OK,
			Rule:    name,
			Message: fmt.Sprintf("%d rules ran and none had anything to say about this stream", len(rules)),
		}}
	}
	return out
}

func profileRules(profile string) (rules []profileRule, name string, implemented bool) {
	switch profile {
	case ProfileApple:
		return appleRules, "apple", true
	case ProfileDASHIF:
		// SC-62. The flag accepts the value so a caller can wire it up now, and
		// says plainly that nothing ran rather than reporting a pass.
		return nil, "the DASH-IF IOP rule set", false
	}
	return nil, profile, false
}

// ValidProfile reports whether s names a rule set, for the CLI to reject a
// typo before a run rather than after it.
func ValidProfile(s string) bool {
	switch s {
	case "", ProfileNone, ProfileApple, ProfileDASHIF:
		return true
	}
	return false
}

// appleRules is the measurable subset of Apple's HLS Authoring Specification,
// populated in profile_apple.go.
var appleRules []profileRule
