package analyze

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/Allan-Nava/segcheck/internal/fetch"
	"github.com/Allan-Nava/segcheck/internal/finding"
)

// One edge serving different bytes from another for the same URL is the defect a
// single-shot check cannot see by construction. It asks one edge, gets a perfect
// answer, and the viewers routed elsewhere are the ones complaining — and a stale
// POP holding a segment from before a re-encode does not fail: it plays the wrong
// content, at the right length, with the right timestamps.
//
// The comparison is deliberately of the *same URLs* rather than a second full
// run. Re-running the analysis against another edge would sample a live playlist
// at a different moment and compare two different windows, and the difference
// would be the clock rather than the edges.

// popResult is what one edge returned for one segment.
type popResult struct {
	digest string
	size   int
	err    error
}

// popComparison is one edge's answers, keyed by segment URI.
type popComparison struct {
	addr string
	// err is set when the edge could not be reached at all, which is a hole in
	// the coverage rather than a verdict about the stream.
	err     error
	results map[string]popResult
}

// comparePOPs re-fetches every sampled segment through each extra edge.
//
// It costs one full copy of the sample per edge, which is why it happens only
// when asked for: an operator passing three POPs has asked to download the
// sample four times.
func comparePOPs(ctx context.Context, c *fetch.Client, rends []*renditionData, opts Options) []popComparison {
	if len(opts.POPs) == 0 {
		return nil
	}
	// Which URLs to ask for: exactly the ones already fetched, so the two answers
	// are about the same bytes rather than about two moments.
	type want struct {
		uri   string
		rng   string
		label string
	}
	var wanted []want
	seen := map[string]bool{}
	for _, rd := range rends {
		for _, sd := range rd.segs {
			if sd.fetchErr != nil || seen[sd.seg.URI] {
				continue
			}
			seen[sd.seg.URI] = true
			rng := ""
			if sd.seg.ByteRange != nil {
				rng = sd.seg.ByteRange.Header()
			}
			wanted = append(wanted, want{uri: sd.seg.URI, rng: rng, label: rendLabel(rd.r)})
		}
	}
	if len(wanted) == 0 {
		return nil
	}

	conc := opts.Concurrency
	if conc <= 0 {
		conc = 1
	}

	out := make([]popComparison, len(opts.POPs))
	for i, addr := range opts.POPs {
		client := c.WithResolve(addr)
		cmp := popComparison{addr: addr, results: make(map[string]popResult, len(wanted))}

		var mu sync.Mutex
		sem := make(chan struct{}, conc)
		var wg sync.WaitGroup
		for _, w := range wanted {
			wg.Add(1)
			go func(w want) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				resp, err := client.Get(ctx, w.uri, w.rng)
				r := popResult{err: err}
				if err == nil {
					r.size = len(resp.Body)
					sum := sha256.Sum256(resp.Body)
					r.digest = hex.EncodeToString(sum[:8])
				}
				mu.Lock()
				cmp.results[w.uri] = r
				mu.Unlock()
			}(w)
		}
		wg.Wait()

		// An edge that answered nothing at all was not reachable, and saying that
		// is different from saying its content differs.
		reached := false
		for _, r := range cmp.results {
			if r.err == nil {
				reached = true
				break
			}
		}
		if !reached {
			for _, r := range cmp.results {
				cmp.err = r.err
				break
			}
		}
		out[i] = cmp
	}
	return out
}

// checkPOP compares what each extra edge returned against what the run already
// fetched.
func checkPOP(rends []*renditionData, comparisons []popComparison) []finding.Finding {
	if len(comparisons) == 0 {
		return nil
	}
	// The reference: what the default resolution returned, per segment URI.
	reference := map[string]popResult{}
	label := map[string]string{}
	for _, rd := range rends {
		for _, sd := range rd.segs {
			if sd.fetchErr != nil {
				continue
			}
			sum := sha256.Sum256(sd.res.Body)
			reference[sd.seg.URI] = popResult{digest: hex.EncodeToString(sum[:8]), size: len(sd.res.Body)}
			label[sd.seg.URI] = segLabel(rd, sd)
		}
	}
	if len(reference) == 0 {
		return nil
	}

	var out []finding.Finding
	agreed := 0
	for _, cmp := range comparisons {
		if cmp.err != nil {
			out = append(out, finding.Finding{
				Check: "pop", Target: cmp.addr, Status: finding.ERROR,
				Message: fmt.Sprintf("%s could not be reached: %v", cmp.addr, cmp.err),
				Hint:    "nothing was compared against this edge; the findings above are about the one this run resolved to",
			})
			continue
		}

		var missing, differing []string
		var firstDiff, firstMissing string
		for uri, ref := range reference {
			got, ok := cmp.results[uri]
			if !ok {
				continue
			}
			switch {
			case got.err != nil:
				missing = append(missing, uri)
				if firstMissing == "" {
					firstMissing = fmt.Sprintf("%s: %v", label[uri], got.err)
				}
			case got.digest != ref.digest:
				differing = append(differing, uri)
				if firstDiff == "" {
					firstDiff = fmt.Sprintf("%s (%d bytes here, %d there)", label[uri], ref.size, got.size)
				}
			default:
				agreed++
			}
		}

		if len(missing) > 0 {
			out = append(out, finding.Finding{
				Check: "pop", Target: cmp.addr, Status: finding.BAD,
				Message: fmt.Sprintf("%s did not serve %d of %d segments the other edge did — %s",
					cmp.addr, len(missing), len(reference), firstMissing),
				Value: finding.Num(float64(len(missing))), Unit: "segments",
				Hint: "viewers routed to this edge get a hole where the others get media, and nothing at the other edges shows it",
			})
		}
		if len(differing) > 0 {
			out = append(out, finding.Finding{
				Check: "pop", Target: cmp.addr, Status: finding.BAD,
				Message: fmt.Sprintf("%s returned different bytes for %d of %d segments — %s",
					cmp.addr, len(differing), len(reference), firstDiff),
				Value: finding.Num(float64(len(differing))), Unit: "segments",
				Hint: "one of the two is stale: the content plays perfectly and it is not the same content, so only the viewers routed there see it",
			})
		}
	}

	if agreed > 0 && len(out) == 0 {
		out = append(out, finding.Finding{
			Check: "pop", Target: "edges", Status: finding.OK,
			Message: fmt.Sprintf("%d edges return byte-identical media for all %d sampled segments",
				len(comparisons)+1, len(reference)),
			Value: finding.Num(float64(len(reference))), Unit: "segments",
		})
	}
	return out
}
