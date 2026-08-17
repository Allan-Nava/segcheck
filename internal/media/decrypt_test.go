package media

import (
	"bytes"
	"errors"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// SC-22: full-segment encryption.
//
// Every check in this tool reads the media, so on an AES-128 stream every one of
// them is blind and the honest report is "segcheck could not look". Given the key it
// can look — and the trap is the initialisation vector, which EXT-X-KEY need not
// state. When it does not, the IV is the segment's media sequence number, and a
// decrypter defaulting to zeroes produces noise that is indistinguishable from a
// wrong key.

func TestDecryptAES128_RoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x2A}, 16)
	plain := mediatest.TSWithSPS(0, 3600, 25, mediatest.SPSFor(1280, 720))

	for _, tc := range []struct {
		name string
		iv   []byte
	}{
		{"an explicit IV", bytes.Repeat([]byte{0x11}, 16)},
		{"the sequence number as an IV", SequenceIV(7)},
		{"a zero IV", make([]byte, 16)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enc := mediatest.EncryptAES128(plain, key, tc.iv)
			if bytes.Equal(enc, plain) {
				t.Fatal("the builder did not encrypt anything")
			}
			got, err := DecryptAES128(enc, key, tc.iv)
			if err != nil {
				t.Fatalf("DecryptAES128: %v", err)
			}
			if !bytes.Equal(got, plain) {
				t.Errorf("round trip changed %d bytes into %d", len(plain), len(got))
			}
			// And the decrypted bytes are the segment they were.
			if _, err := ParseTS(got); err != nil {
				t.Errorf("the decrypted segment does not parse: %v", err)
			}
		})
	}
}

// The IV HLS specifies when EXT-X-KEY states none: the media sequence number as a
// 128-bit big-endian value.
func TestSequenceIV(t *testing.T) {
	if got := SequenceIV(0); !bytes.Equal(got, make([]byte, 16)) {
		t.Errorf("SequenceIV(0) = %x, want zeroes", got)
	}
	want := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x01, 0x2C}
	if got := SequenceIV(300); !bytes.Equal(got, want) {
		t.Errorf("SequenceIV(300) = %x, want %x", got, want)
	}
	// Above 32 bits, because a live stream's media sequence gets there.
	big := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00}
	if got := SequenceIV(1 << 40); !bytes.Equal(got, big) {
		t.Errorf("SequenceIV(1<<40) = %x, want %x", got, big)
	}
}

// The wrong key does not fail cleanly at the cipher — CBC happily produces noise.
// It is the padding that catches it, and it has to be checked rather than trusted:
// noise whose last byte happens to be 1 would otherwise be accepted and handed to a
// container reader as if it were media.
func TestDecryptAES128_WrongKey(t *testing.T) {
	right := bytes.Repeat([]byte{0x2A}, 16)
	wrong := bytes.Repeat([]byte{0x2B}, 16)
	iv := bytes.Repeat([]byte{0x11}, 16)
	enc := mediatest.EncryptAES128(mediatest.TSWithSPS(0, 3600, 25, mediatest.SPSFor(1280, 720)), right, iv)

	if _, err := DecryptAES128(enc, wrong, iv); !errors.Is(err, ErrWrongKey) {
		t.Errorf("err = %v, want ErrWrongKey", err)
	}
	// The right key with the wrong IV corrupts only the first block, so the padding
	// still checks out — the segment is simply not parseable. That is the honest
	// outcome: a check downstream reports what it found, and this layer does not
	// pretend to know which of the two was wrong.
	other := bytes.Repeat([]byte{0x99}, 16)
	if got, err := DecryptAES128(enc, right, other); err != nil {
		t.Errorf("the right key with a wrong IV errored at the padding: %v", err)
	} else if bytes.Equal(got[:16], mediatest.TSWithSPS(0, 3600, 25, mediatest.SPSFor(1280, 720))[:16]) {
		t.Error("a wrong IV left the first block intact")
	}
}

// The shapes that are not something to decrypt at all.
func TestDecryptAES128_BadInputs(t *testing.T) {
	key := bytes.Repeat([]byte{0x2A}, 16)
	iv := make([]byte, 16)
	block := make([]byte, 32)
	for _, tc := range []struct {
		name    string
		data    []byte
		key, iv []byte
	}{
		{"a short key", block, key[:8], iv},
		{"a long key", block, append(key, key...), iv},
		{"a short IV", block, key, iv[:8]},
		{"no data", nil, key, iv},
		{"a length that is not a whole number of blocks", block[:20], key, iv},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecryptAES128(tc.data, tc.key, tc.iv); err == nil {
				t.Error("accepted input that cannot be decrypted")
			}
		})
	}
}

// The padding check, at each way it can be wrong.
func TestStripPKCS7(t *testing.T) {
	if got, err := stripPKCS7([]byte{1, 2, 3, 0x02, 0x02}); err != nil || !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Errorf("= %v/%v, want [1 2 3]", got, err)
	}
	for _, tc := range []struct {
		name string
		in   []byte
	}{
		{"empty", nil},
		{"a padding byte of zero", []byte{1, 2, 0x00}},
		{"a padding byte past the block size", []byte{1, 2, 0x11}},
		{"a padding byte longer than the data", []byte{1, 2, 0x08}},
		{"bytes that disagree", []byte{1, 2, 0x01, 0x02}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := stripPKCS7(tc.in); !errors.Is(err, ErrWrongKey) {
				t.Errorf("err = %v, want ErrWrongKey", err)
			}
		})
	}
}
