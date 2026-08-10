package manifest

import (
	"strconv"
	"strings"
)

// pathExt is the lower-cased extension of a URL path.
func pathExt(p string) string {
	i := strings.LastIndexByte(p, '.')
	if i < 0 {
		return ""
	}
	return strings.ToLower(p[i:])
}

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}

func trimSpaceBytes(b []byte, n int) []byte {
	if len(b) > n {
		b = b[:n]
	}
	return b
}

// parseAttrs parses an HLS attribute list: comma-separated NAME=VALUE pairs
// where a quoted value may itself contain commas. Splitting naively on commas
// mangles CODECS="avc1.4d401f,mp4a.40.2" — and a mangled CODECS silently
// disables the codec comparison, so this has to be done properly.
func parseAttrs(s string) map[string]string {
	out := map[string]string{}
	var key, val strings.Builder
	inKey, inQuotes := true, false
	flush := func() {
		k := strings.TrimSpace(key.String())
		if k != "" {
			out[strings.ToUpper(k)] = strings.TrimSpace(val.String())
		}
		key.Reset()
		val.Reset()
		inKey = true
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuotes:
			if c == '"' {
				inQuotes = false
				continue
			}
			val.WriteByte(c)
		case c == '"' && !inKey:
			inQuotes = true
		case c == '=' && inKey:
			inKey = false
		case c == ',' && !inKey:
			flush()
		default:
			if inKey {
				key.WriteByte(c)
			} else {
				val.WriteByte(c)
			}
		}
	}
	flush()
	return out
}

func attrInt(attrs map[string]string, key string) int {
	n, err := strconv.Atoi(strings.TrimSpace(attrs[key]))
	if err != nil {
		return 0
	}
	return n
}

func attrFloat(attrs map[string]string, key string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(attrs[key]), 64)
	if err != nil {
		return 0
	}
	return f
}

// attrResolution parses RESOLUTION=1920x1080.
func attrResolution(attrs map[string]string) (w, h int) {
	v := attrs["RESOLUTION"]
	i := strings.IndexAny(v, "xX")
	if i < 0 {
		return 0, 0
	}
	w, _ = strconv.Atoi(strings.TrimSpace(v[:i]))
	h, _ = strconv.Atoi(strings.TrimSpace(v[i+1:]))
	return w, h
}
