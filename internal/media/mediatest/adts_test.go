package mediatest

import "testing"

// The builder refuses to plant a layout AC-3 cannot express, rather than writing
// an acmod that means something else.
func TestAC3Acmod_RefusesUnknownLayout(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("ac3Acmod accepted a channel count AC-3 has no acmod for")
		}
	}()
	ac3Acmod(5)
}
