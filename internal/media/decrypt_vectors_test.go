package media

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// SC-96: the cipher layer, against an authority outside this repository.
//
// Every other assertion about decryption round-trips through this package's own
// encryptor, and a round trip cannot catch a shared misunderstanding: an encryptor and a
// decrypter that both chained the IV wrongly would agree with each other perfectly. The
// usual answer is a real stream, and none is available — no public AES-128 HLS test
// stream has been reachable, across every provider that publishes one.
//
// RFC 3602 §4 is the authority that is available. Its AES-CBC vectors are third-party
// plaintext and third-party ciphertext, and they pin exactly what a round trip cannot:
// the mode, the key schedule and the IV chaining across block boundaries.
//
// These are raw CBC vectors with no PKCS#7 padding, which is why they exercise
// cbcDecrypt rather than DecryptAES128 — the padding layer has its own tests, and
// feeding it a plaintext that was never padded would only assert that it says no.
func TestCBCDecrypt_RFC3602Vectors(t *testing.T) {
	unhex := func(s string) []byte {
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatalf("bad vector hex %q: %v", s, err)
		}
		return b
	}

	for _, tc := range []struct {
		name       string
		key, iv    string
		ciphertext string
		plaintext  []byte
	}{
		{
			name:       "RFC 3602 case 1, one block",
			key:        "06a9214036b8a15b512e03d534120006",
			iv:         "3dafba429d9eb430b422da802c9fac41",
			ciphertext: "e353779c1079aeb82708942dbe77181a",
			plaintext:  []byte("Single block msg"),
		},
		{
			name:       "RFC 3602 case 2, two blocks",
			key:        "c286696d887c9aa0611bbb3e2025a45a",
			iv:         "562e17996d093d28ddb3ba695a2e6f58",
			ciphertext: "d296cd94c2cccf8a3a863028b5e1dc0a7586602d253cfff91b8266bea6d61ab1",
			plaintext:  unhex("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"),
		},
		{
			name: "RFC 3602 case 3, three blocks",
			key:  "6c3ea0477630ce21a2ce334aa746c2cd",
			iv:   "c782dc4c098c66cbd9cd27d825682c81",
			ciphertext: "d0a02b3836451753d493665d33f0e8862dea54cdb293abc7506939276772f8d5" +
				"021c19216bad525c8579695d83ba2684",
			plaintext: []byte("This is a 48-byte message (exactly 3 AES blocks)"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cbcDecrypt(unhex(tc.ciphertext), unhex(tc.key), unhex(tc.iv))
			if err != nil {
				t.Fatalf("cbcDecrypt: %v", err)
			}
			if !bytes.Equal(got, tc.plaintext) {
				t.Errorf("decrypted %q, want %q", got, tc.plaintext)
			}
		})
	}
}

// And the composition: the same vector key and IV through DecryptAES128, over ciphertext
// whose plaintext really is PKCS#7-padded. This is the one place the two layers are
// checked together against externally sourced key material rather than a repeated byte.
func TestDecryptAES128_WithVectorKeyMaterial(t *testing.T) {
	key, _ := hex.DecodeString("06a9214036b8a15b512e03d534120006")
	iv, _ := hex.DecodeString("3dafba429d9eb430b422da802c9fac41")

	plain := []byte("WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nhello\n")
	enc := mediatest.EncryptAES128(plain, key, iv)
	got, err := DecryptAES128(enc, key, iv)
	if err != nil {
		t.Fatalf("DecryptAES128: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("round trip under RFC 3602 key material changed the plaintext")
	}
	// And the decrypted bytes are what they were: a subtitle segment, not noise that
	// happened to survive the padding check.
	if info, err := ParseWebVTT(got); err != nil || info.Tracks[0].Cues != 1 {
		t.Errorf("the decrypted segment does not read as WebVTT: %v", err)
	}
}

// The cipher layer checks the key length itself, so both it and DecryptAES128 above it
// report the same thing for the same reason.
func TestCBCDecrypt_KeyLength(t *testing.T) {
	block := make([]byte, 16)
	iv := make([]byte, 16)
	for _, key := range [][]byte{nil, make([]byte, 8), make([]byte, 24)} {
		if _, err := cbcDecrypt(block, key, iv); err == nil {
			t.Errorf("cbcDecrypt accepted a %d-byte key", len(key))
		}
		if _, err := DecryptAES128(block, key, iv); err == nil {
			t.Errorf("DecryptAES128 accepted a %d-byte key", len(key))
		}
	}
}
