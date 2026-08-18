package manifest

import "fmt"

// A rendition's name is the key every finding in the report is filed under, so
// two renditions sharing one is not a cosmetic problem. A ladder with two 720p
// rungs at different bitrates produces two `bitrate 720p` rows, two `container
// 720p` rows and two `pdt 720p` rows, and nothing in the report says which rung
// any of them is about — the operator is left to guess which half of the ladder
// to go and look at. Unified Streaming's own live demo has exactly that shape.
//
// The disambiguation is deliberately only applied where it is needed. `720p` is
// what an operator says out loud and `720p@1.3M` is not, so a ladder whose names
// are already distinct is left exactly as it was, and only the rungs that
// actually collide grow a suffix.

// disambiguate makes every rendition name unique, changing as few of them as it
// can.
func disambiguate(rends []Rendition) {
	count := map[string]int{}
	for _, r := range rends {
		count[r.Name]++
	}
	taken := map[string]bool{}
	for i := range rends {
		if count[rends[i].Name] < 2 {
			taken[rends[i].Name] = true
			continue
		}
		// What differs between two rungs of one resolution is the bitrate, which
		// is also what an operator picks between them by.
		base := rends[i].Name
		candidate := base
		if bw := rends[i].Bandwidth; bw > 0 {
			candidate = fmt.Sprintf("%s %dkbps", base, bw/1000)
		}
		// Two rungs identical in both respects are a defect `ladder` reports, and
		// they still have to be nameable: a suffix that is the same on both would
		// leave the report as ambiguous as it was.
		if taken[candidate] {
			for n := 2; ; n++ {
				alt := fmt.Sprintf("%s #%d", candidate, n)
				if !taken[alt] {
					candidate = alt
					break
				}
			}
		}
		rends[i].Name = candidate
		taken[candidate] = true
	}
}
