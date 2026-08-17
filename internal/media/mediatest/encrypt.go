package mediatest

import (
	"crypto/aes"
	"crypto/cipher"
)

// EncryptAES128 encrypts a segment the way HLS AES-128 does: AES in CBC mode with
// PKCS#7 padding. It exists so a test can plant a protected segment whose plaintext
// is known by construction — the only way to tell a decrypter that works from one
// that produces plausible noise.
func EncryptAES128(plain, key, iv []byte) []byte {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic("mediatest: " + err.Error())
	}
	padded := padPKCS7(plain, aes.BlockSize)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out
}

// padPKCS7 appends between one and blockSize bytes, each equal to the number added.
// A plaintext that is already a whole number of blocks gets a whole block of
// padding, which is what makes the length unambiguous.
func padPKCS7(b []byte, blockSize int) []byte {
	n := blockSize - len(b)%blockSize
	out := make([]byte, len(b)+n)
	copy(out, b)
	for i := len(b); i < len(out); i++ {
		out[i] = byte(n)
	}
	return out
}
