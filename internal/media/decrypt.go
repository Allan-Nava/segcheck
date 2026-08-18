package media

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
)

// Full-segment decryption.
//
// HLS AES-128 encrypts a whole segment with AES in CBC mode and PKCS#7 padding.
// Every check in this tool reads the media, so on a protected stream every one of
// them is blind: the container reader sees noise, and the honest report is "segcheck
// could not look". Given the key it can look.
//
// The initialisation vector is the trap. EXT-X-KEY may state one, and when it does
// not the IV is the segment's media sequence number as a 128-bit big-endian value —
// so a decrypter that defaulted to zeroes would produce garbage on the large share
// of streams that omit the attribute, and garbage is indistinguishable from a wrong
// key.

// AESBlockSize is the block size of AES, which is also the length of an IV.
const AESBlockSize = aes.BlockSize

// ErrWrongKey marks bytes that decrypted without a structural error but did not
// yield anything recognisable. It is reported as a failure of the key, not of the
// stream: a segment that will not decrypt with the key supplied is far more often a
// wrong key than a broken segment, and saying so points at the right thing.
var ErrWrongKey = errors.New("decrypted bytes are not a recognised container")

// DecryptAES128 decrypts a full-segment-encrypted segment.
//
// The key must be 16 bytes. The IV must be 16 bytes; SequenceIV builds the one HLS
// specifies when EXT-X-KEY states none.
func DecryptAES128(data, key, iv []byte) ([]byte, error) {
	if len(iv) != AESBlockSize {
		return nil, fmt.Errorf("AES-128 needs a %d-byte IV, got %d", AESBlockSize, len(iv))
	}
	if len(data) == 0 || len(data)%AESBlockSize != 0 {
		// CBC works on whole blocks. A segment that is not a multiple of the block
		// size was truncated in transit or is not encrypted at all, and either way
		// there is nothing to decrypt.
		return nil, fmt.Errorf("encrypted segment is %d bytes, not a multiple of %d", len(data), AESBlockSize)
	}

	out, err := cbcDecrypt(data, key, iv)
	if err != nil {
		return nil, err
	}
	return stripPKCS7(out)
}

// cbcDecrypt is the cipher layer on its own: AES-128 in CBC mode, no padding removed.
//
// It is separate so it can be checked against published test vectors. A round trip
// against this package's own encryptor cannot catch a shared misunderstanding of the
// mode or the IV chaining — it would agree with itself — and no public AES-128 HLS
// stream has been reachable to check against instead. RFC 3602's vectors are the
// external authority available, and decrypt_vectors_test.go holds them.
func cbcDecrypt(data, key, iv []byte) ([]byte, error) {
	// The one place the key length is checked, so the error has one spelling and this
	// branch is reachable from both callers rather than shadowed by a duplicate above.
	if len(key) != 16 {
		return nil, fmt.Errorf("AES-128 needs a 16-byte key, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	return out, nil
}

// stripPKCS7 removes the padding CBC needs. A padding byte outside 1..16, or one
// the trailing bytes do not agree with, means the plaintext is not what was
// encrypted — which in practice means the key was wrong.
func stripPKCS7(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, ErrWrongKey
	}
	n := int(b[len(b)-1])
	if n < 1 || n > AESBlockSize || n > len(b) {
		return nil, fmt.Errorf("%w: padding byte %d", ErrWrongKey, n)
	}
	for _, c := range b[len(b)-n:] {
		if int(c) != n {
			return nil, fmt.Errorf("%w: padding bytes disagree", ErrWrongKey)
		}
	}
	return b[:len(b)-n], nil
}

// SequenceIV builds the initialisation vector HLS specifies when EXT-X-KEY states
// none: the segment's media sequence number as a 128-bit big-endian value.
//
// This is the whole reason a decrypter cannot default to zeroes. A stream that omits
// the IV attribute — and a large share of them do — decrypts to noise under a zero
// IV, and noise is indistinguishable from a wrong key.
func SequenceIV(sequence int) []byte {
	iv := make([]byte, AESBlockSize)
	n := uint64(sequence)
	for i := 0; i < 8; i++ {
		iv[AESBlockSize-1-i] = byte(n >> (8 * uint(i)))
	}
	return iv
}
