package mediatest

import "testing"

// The builders are test infrastructure, and a builder that quietly produces
// something malformed makes every test asserted against it meaningless. This
// covers the one place tsPacket makes a decision rather than just writing bytes.

// A transport packet holds 184 bytes of payload at most. A caller handing over
// more is truncated rather than producing an over-long packet, because an
// over-long packet would shift every packet after it and the fixture would no
// longer be MPEG-TS at all.
func TestTsPacket_TruncatesAnOversizedPayload(t *testing.T) {
	payload := make([]byte, 300)
	for i := range payload {
		payload[i] = byte(i)
	}
	pkt := tsPacket(0x0100, true, 0, payload)

	if len(pkt) != 188 {
		t.Fatalf("packet length = %d, want 188", len(pkt))
	}
	// With a full 184-byte payload there is no room for an adaptation field, so
	// the payload starts straight after the four-byte header.
	if got := pkt[4:]; string(got) != string(payload[:184]) {
		t.Errorf("payload was not the first 184 bytes of the input")
	}
}

// The complement: a short payload is padded with an adaptation field so the
// packet still comes to exactly 188 bytes.
func TestTsPacket_PadsAShortPayloadToTheFullPacket(t *testing.T) {
	pkt := tsPacket(0x0100, true, 3, []byte{0xAA, 0xBB})
	if len(pkt) != 188 {
		t.Fatalf("packet length = %d, want 188", len(pkt))
	}
	if pkt[0] != 0x47 {
		t.Errorf("sync byte = %#x, want 0x47", pkt[0])
	}
	// adaptation_field_control 0x03: adaptation field followed by payload.
	if (pkt[3]>>4)&0x03 != 0x03 {
		t.Errorf("adaptation_field_control = %#x, want 0x3 for a padded packet", (pkt[3]>>4)&0x03)
	}
	if pkt[3]&0x0F != 3 {
		t.Errorf("continuity counter = %d, want 3", pkt[3]&0x0F)
	}
	if string(pkt[186:]) != string([]byte{0xAA, 0xBB}) {
		t.Errorf("payload = %v, want it at the end after the stuffing", pkt[186:])
	}
}
