package analyze

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Allan-Nava/segcheck/internal/finding"
	"github.com/Allan-Nava/segcheck/internal/manifest"
	"github.com/Allan-Nava/segcheck/internal/media"
)

// A CODECS string is not one value. `avc1.640028` names a profile, a constraint
// byte and a level; `hvc1.2.4.L153.B0` adds a tier; `av01.0.13M.08` names a
// profile, a level and a tier of its own. Until now only the four-character code
// was compared against the media, and everything after it was believed.
//
// The two halves of the string fail in opposite directions, which is the whole
// reason both are reported and reported differently:
//
//   - Declared *below* what the media codes, a device reads the manifest,
//     decides it cannot decode this, and never asks for a segment. The rung is
//     dark on that device and nothing in any log says why — the origin sees no
//     request to fail.
//   - Declared *above* what the media codes, every device that could have played
//     it perfectly is excluded. Nobody sees an error; the top rung simply has
//     fewer viewers than it should, which no monitoring notices.

func checkCodecString(rends []*renditionData) []finding.Finding {
	var out []finding.Finding
	for _, rd := range rends {
		if rd.err != nil || rd.r.Kind != manifest.Video || rd.r.Codecs == "" {
			continue
		}
		measured, ok := codecProfileOf(rd)
		if !ok {
			continue // the media states nothing to compare against
		}
		label := rendLabel(rd.r)

		declared, family, ok := parseCodecString(rd.r.Codecs)
		if !ok {
			// The string names a codec whose components segcheck cannot
			// decompose, or names no components at all. That is a limit of this
			// tool: reporting it as a mismatch would send someone re-encoding
			// perfectly good media.
			out = append(out, finding.Finding{
				Check: "codecstring", Target: label, Status: finding.OK,
				Message: fmt.Sprintf("CODECS=%q states no profile and level segcheck can decompose: not verifiable", rd.r.Codecs),
			})
			continue
		}

		var problems []finding.Finding
		if declared.hasProfile && declared.profile != measured.Profile {
			status, direction := finding.WARN, "above"
			hint := "every device that could have played this is excluded by a profile it does not implement; nobody sees an error and the rung simply has fewer viewers"
			if declared.profile < measured.Profile {
				status = finding.BAD
				direction = "below"
				hint = "a device that reads this decides it can decode the stream and then cannot: the failure is at the decoder, after the manifest promised otherwise"
			}
			problems = append(problems, finding.Finding{
				Check: "codecstring", Target: label, Status: status,
				Message: fmt.Sprintf("CODECS=%q declares profile %d, %s the %d the media codes",
					rd.r.Codecs, declared.profile, direction, measured.Profile),
				Value: finding.Num(float64(measured.Profile)),
				Hint:  hint,
			})
		}
		if declared.hasLevel && declared.level != measured.Level {
			status, direction := finding.WARN, "above"
			hint := "devices that could have played this are held off it by a level they do not implement; nobody sees an error and the rung has fewer viewers than it should"
			if declared.level < measured.Level {
				status = finding.BAD
				direction = "below"
				hint = "a device reads the manifest, decides it cannot decode this level, and never asks for a segment: the rung is dark and the origin sees no request to fail"
			}
			problems = append(problems, finding.Finding{
				Check: "codecstring", Target: label, Status: status,
				Message: fmt.Sprintf("CODECS=%q declares level %d, %s the %d the media codes",
					rd.r.Codecs, declared.level, direction, measured.Level),
				Value: finding.Num(float64(measured.Level)),
				Hint:  hint,
			})
		}
		if declared.hasTier && declared.tier != measured.Tier {
			problems = append(problems, finding.Finding{
				Check: "codecstring", Target: label, Status: finding.WARN,
				Message: fmt.Sprintf("CODECS=%q declares the %s tier and the media codes the %s tier",
					rd.r.Codecs, tierName(declared.tier), tierName(measured.Tier)),
				Hint: "the high tier raises the bitrate ceiling a level allows; a device that implements only the main tier declines high-tier media",
			})
		}
		if len(problems) > 0 {
			out = append(out, problems...)
			continue
		}
		out = append(out, finding.Finding{
			Check: "codecstring", Target: label, Status: finding.OK,
			Message: fmt.Sprintf("CODECS=%q matches the media: %s profile %d, level %d",
				rd.r.Codecs, family, measured.Profile, measured.Level),
			Value: finding.Num(float64(measured.Level)),
		})
	}
	return out
}

func tierName(tier int) string {
	if tier == 1 {
		return "high"
	}
	return "main"
}

// codecProfileOf is what this rendition's video really states about its profile.
func codecProfileOf(rd *renditionData) (media.CodecProfile, bool) {
	for _, sd := range rd.segs {
		if !sd.parsed {
			continue
		}
		t, ok := sd.info.Track(media.Video)
		if !ok {
			continue
		}
		if p, ok := t.CodecProfile(); ok {
			return p, true
		}
	}
	return media.CodecProfile{}, false
}

// declaredProfile is what a CODECS string spells out. Each field carries its own
// presence flag because the string may state some and not others, and a missing
// component must never be compared as a zero.
type declaredProfile struct {
	profile    int
	level      int
	tier       int
	hasProfile bool
	hasLevel   bool
	hasTier    bool
}

// parseCodecString decomposes the codec string for the rendition's video, and
// names the family so a finding can say which grammar it read.
//
// The grammars are genuinely different — `avc1.PPCCLL` packs three fields into
// six hex digits, `hvc1.P.C.LX.B` uses dot-separated components with a letter
// marking the tier, `av01.P.LL[MH].BB` puts the tier inside the level component
// — and there is no shared shape to exploit. Guessing one grammar for another
// yields plausible numbers, which is why an unrecognised prefix returns false
// rather than a best effort.
func parseCodecString(codecs string) (declaredProfile, string, bool) {
	for _, c := range strings.Split(codecs, ",") {
		c = strings.TrimSpace(c)
		lower := strings.ToLower(c)
		switch {
		case strings.HasPrefix(lower, "avc1."), strings.HasPrefix(lower, "avc3."):
			if d, ok := parseAVCCodecString(lower); ok {
				return d, "H.264", true
			}
		case strings.HasPrefix(lower, "hvc1."), strings.HasPrefix(lower, "hev1."):
			if d, ok := parseHEVCCodecString(lower); ok {
				return d, "HEVC", true
			}
		case strings.HasPrefix(lower, "av01."):
			if d, ok := parseAV1CodecString(lower); ok {
				return d, "AV1", true
			}
		case strings.HasPrefix(lower, "vp09."):
			if d, ok := parseVP9CodecString(lower); ok {
				return d, "VP9", true
			}
		}
	}
	return declaredProfile{}, "", false
}

// parseAVCCodecString reads `avc1.PPCCLL`: profile_idc, the constraint byte and
// level_idc, as three pairs of hexadecimal digits.
//
// The older dotted form `avc1.66.30` exists too, and its components are decimal.
// Reading them as hex would turn level 30 into 48, so the two are told apart by
// shape rather than assumed.
func parseAVCCodecString(c string) (declaredProfile, bool) {
	rest := c[len("avc1."):]
	if len(rest) == 6 && isHex(rest) {
		p, _ := strconv.ParseInt(rest[0:2], 16, 32)
		cs, _ := strconv.ParseInt(rest[2:4], 16, 32)
		l, _ := strconv.ParseInt(rest[4:6], 16, 32)
		_ = cs
		return declaredProfile{profile: int(p), level: int(l), hasProfile: true, hasLevel: true}, true
	}
	parts := strings.Split(rest, ".")
	if len(parts) >= 2 {
		p, err1 := strconv.Atoi(parts[0])
		l, err2 := strconv.Atoi(parts[len(parts)-1])
		if err1 == nil && err2 == nil {
			return declaredProfile{profile: p, level: l, hasProfile: true, hasLevel: true}, true
		}
	}
	return declaredProfile{}, false
}

// parseHEVCCodecString reads `hvc1.P.C.LX.B…`: the profile with an optional
// space prefix, the compatibility flags, then a tier letter and the level.
func parseHEVCCodecString(c string) (declaredProfile, bool) {
	parts := strings.Split(c[len("hvc1."):], ".")
	var out declaredProfile
	for i, p := range parts {
		switch {
		case i == 0:
			// The profile may carry a general_profile_space prefix of A, B or C,
			// which is not part of the number.
			p = strings.TrimLeft(p, "abc")
			if v, err := strconv.Atoi(p); err == nil {
				out.profile, out.hasProfile = v, true
			}
		case strings.HasPrefix(p, "l"), strings.HasPrefix(p, "h"):
			out.tier, out.hasTier = 0, true
			if strings.HasPrefix(p, "h") {
				out.tier = 1
			}
			if v, err := strconv.Atoi(p[1:]); err == nil {
				out.level, out.hasLevel = v, true
			}
		}
	}
	return out, out.hasProfile || out.hasLevel
}

// parseAV1CodecString reads `av01.P.LL[MH].BB`: the tier is a letter glued to
// the end of the level component rather than a component of its own.
func parseAV1CodecString(c string) (declaredProfile, bool) {
	parts := strings.Split(c[len("av01."):], ".")
	if len(parts) < 2 {
		return declaredProfile{}, false
	}
	var out declaredProfile
	if v, err := strconv.Atoi(parts[0]); err == nil {
		out.profile, out.hasProfile = v, true
	}
	lvl := parts[1]
	if n := len(lvl); n > 0 && (lvl[n-1] == 'm' || lvl[n-1] == 'h') {
		out.tier, out.hasTier = 0, true
		if lvl[n-1] == 'h' {
			out.tier = 1
		}
		lvl = lvl[:n-1]
	}
	if v, err := strconv.Atoi(lvl); err == nil {
		out.level, out.hasLevel = v, true
	}
	return out, out.hasProfile || out.hasLevel
}

// parseVP9CodecString reads `vp09.PP.LL.DD`, whose components are decimal.
func parseVP9CodecString(c string) (declaredProfile, bool) {
	parts := strings.Split(c[len("vp09."):], ".")
	if len(parts) < 2 {
		return declaredProfile{}, false
	}
	var out declaredProfile
	if v, err := strconv.Atoi(parts[0]); err == nil {
		out.profile, out.hasProfile = v, true
	}
	if v, err := strconv.Atoi(parts[1]); err == nil {
		out.level, out.hasLevel = v, true
	}
	return out, out.hasProfile && out.hasLevel
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return len(s) > 0
}
