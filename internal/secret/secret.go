// Package secret is Showstone's at-rest crypto: an argon2id password-derived
// key-encryption-key (KEK) wraps a random data-encryption-key (DEK); the DEK seals the
// browser profile blob (a tar of the Chromium user-data-dir). In suite mode the DEK is
// injected (the aggregator owns derivation); standalone, it is derived from a password.
// Single-blob AES-256-GCM. Pure Go, no CGo. Mirrors Capsule/Vault's seal.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"runtime"
	"sync"

	"golang.org/x/crypto/argon2"
)

// ErrWrongPassphrase is returned when KEK derivation + AEAD open fails (bad password or
// tampered envelope).
var ErrWrongPassphrase = errors.New("showstone: wrong passphrase")

// KDFParams are stored in plaintext so the KEK can be re-derived. Threads is PINNED to
// the stored value so a stick moved between hosts derives the same key.
type KDFParams struct {
	Algo    string `json:"algo"`
	Time    uint32 `json:"time"`
	Memory  uint32 `json:"memory"` // KiB
	Threads uint8  `json:"threads"`
	KeyLen  uint32 `json:"key_len"`
	Salt    []byte `json:"salt"`
}

// Sealed is one AES-256-GCM ciphertext with its nonce.
type Sealed struct {
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// Envelope is the secret block persisted in the standalone config. Only WrappedDEK is
// secret; KDF params + salt + nonces are plaintext (needed to derive/open).
type Envelope struct {
	KDF        KDFParams `json:"kdf"`
	WrappedDEK Sealed    `json:"wrapped_dek"`
	Version    int       `json:"version"`
}

// DefaultKDF returns calibrated argon2id params (256 MiB floor; threads pinned).
func DefaultKDF() KDFParams {
	return KDFParams{Algo: "argon2id", Time: 4, Memory: 256 * 1024, Threads: 4, KeyLen: 32}
}

func deriveKEK(password []byte, p KDFParams) []byte {
	return argon2.IDKey(password, p.Salt, p.Time, p.Memory, p.Threads, p.KeyLen)
}

// aad binds the full-width KDF parameters + salt so tampering surfaces as an auth
// failure rather than a silently different key.
func (e Envelope) aad() []byte {
	a := []byte(e.KDF.Algo)
	a = binary.BigEndian.AppendUint32(a, e.KDF.Time)
	a = binary.BigEndian.AppendUint32(a, e.KDF.Memory)
	a = append(a, e.KDF.Threads)
	a = binary.BigEndian.AppendUint32(a, e.KDF.KeyLen)
	a = binary.BigEndian.AppendUint32(a, uint32(e.Version))
	a = binary.BigEndian.AppendUint32(a, uint32(len(e.KDF.Salt)))
	return append(a, e.KDF.Salt...)
}

// NewEnvelope creates a fresh envelope for a new password: random salt + random DEK
// wrapped under the password-derived KEK. Returns the cleartext DEK for a KeyStore.
func NewEnvelope(password []byte) (Envelope, []byte, error) {
	p := DefaultKDF()
	p.Salt = make([]byte, 16)
	if _, err := rand.Read(p.Salt); err != nil {
		return Envelope{}, nil, err
	}
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return Envelope{}, nil, err
	}
	env := Envelope{KDF: p, Version: 1}
	kek := deriveKEK(password, p)
	defer zero(kek)
	wrapped, err := seal(kek, dek, env.aad())
	if err != nil {
		return Envelope{}, nil, err
	}
	env.WrappedDEK = wrapped
	return env, dek, nil
}

// Unwrap re-derives the KEK from the password and recovers the DEK.
func (e Envelope) Unwrap(password []byte) ([]byte, error) {
	kek := deriveKEK(password, e.KDF)
	defer zero(kek)
	dek, err := open(kek, e.WrappedDEK, e.aad())
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	return dek, nil
}

// SealBlob encrypts a whole blob (e.g. the profile tar) under the DEK.
func SealBlob(dek, plaintext []byte) (Sealed, error) { return seal(dek, plaintext, nil) }

// OpenBlob decrypts a sealed blob under the DEK.
func OpenBlob(dek []byte, s Sealed) ([]byte, error) { return open(dek, s, nil) }

// SealBlobAAD / OpenBlobAAD bind a domain string as additional authenticated data, so a
// blob sealed for one purpose (e.g. the profile) cannot be swapped in for another (e.g.
// the session state) under the same DEK — the GCM tag fails on a domain mismatch.
func SealBlobAAD(dek, plaintext, aad []byte) (Sealed, error) { return seal(dek, plaintext, aad) }

// OpenBlobAAD decrypts a blob sealed with the given domain AAD.
func OpenBlobAAD(dek []byte, s Sealed, aad []byte) ([]byte, error) { return open(dek, s, aad) }

// Encode lays a Sealed out as [nonce][ciphertext] for on-disk storage.
func Encode(s Sealed) []byte {
	out := make([]byte, 0, len(s.Nonce)+len(s.Ciphertext))
	out = append(out, s.Nonce...)
	return append(out, s.Ciphertext...)
}

// Decode parses [nonce(12)][ciphertext] back into a Sealed.
func Decode(b []byte) (Sealed, error) {
	const nonceLen = 12
	if len(b) < nonceLen {
		return Sealed{}, errors.New("showstone: sealed blob too short")
	}
	return Sealed{Nonce: append([]byte(nil), b[:nonceLen]...), Ciphertext: append([]byte(nil), b[nonceLen:]...)}, nil
}

func seal(key, plaintext, aad []byte) (Sealed, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return Sealed{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Sealed{}, err
	}
	return Sealed{Nonce: nonce, Ciphertext: gcm.Seal(nil, nonce, plaintext, aad)}, nil
}

func open(key []byte, s Sealed, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(s.Nonce) != gcm.NonceSize() {
		return nil, errors.New("showstone: bad nonce length")
	}
	return gcm.Open(nil, s.Nonce, s.Ciphertext, aad)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
	runtime.KeepAlive(b)
}

// KeyStore holds the decrypted DEK in memory for the unlocked lifetime.
type KeyStore struct {
	mu  sync.RWMutex
	dek []byte
}

func NewKeyStore(dek []byte) *KeyStore { return &KeyStore{dek: append([]byte(nil), dek...)} }

// Use runs fn with the DEK held under the read lock; the slice must not escape.
func (k *KeyStore) Use(fn func(dek []byte) error) error {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.dek == nil {
		return errors.New("showstone: keystore locked")
	}
	return fn(k.dek)
}

// Zero wipes the DEK (called on lock/shutdown).
func (k *KeyStore) Zero() {
	k.mu.Lock()
	defer k.mu.Unlock()
	zero(k.dek)
	k.dek = nil
}

// ConstantTimeEqual is used for token comparison.
func ConstantTimeEqual(a, b []byte) bool { return subtle.ConstantTimeCompare(a, b) == 1 }
