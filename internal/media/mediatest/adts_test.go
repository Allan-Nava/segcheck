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

// The SEI framing states the payload type and size as chains of 0xFF bytes
// terminated by a smaller one. A caption payload never needs the chain — cc_count
// is five bits wide — but another SEI in the same NAL can, and a builder that got
// the framing wrong would silently shift every message after it.
func TestSEIMessage_Chains(t *testing.T) {
	payload := make([]byte, 300)
	got := seiMessage(260, payload)

	// Type 260 is 0xFF + 5; size 300 is 0xFF + 45.
	want := []byte{0xFF, 0x05, 0xFF, 0x2D}
	for i, b := range want {
		if got[i] != b {
			t.Fatalf("framing = %v, want prefix %v", got[:len(want)], want)
		}
	}
	if len(got) != len(want)+len(payload)+1 {
		t.Errorf("length = %d, want %d", len(got), len(want)+len(payload)+1)
	}
	if got[len(got)-1] != 0x80 {
		t.Errorf("the message does not end in rbsp_trailing_bits: %#x", got[len(got)-1])
	}
}
