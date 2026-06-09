package secret

import (
	"bytes"
	"testing"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	pw := []byte("correct horse battery staple")
	env, dek, err := NewEnvelope(pw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := env.Unwrap(pw)
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatal("unwrapped DEK mismatch")
	}
	if _, err := env.Unwrap([]byte("wrong")); err != ErrWrongPassphrase {
		t.Fatalf("wrong password => %v, want ErrWrongPassphrase", err)
	}
}

func TestSealBlobRoundTrip(t *testing.T) {
	dek := make([]byte, 32)
	for i := range dek {
		dek[i] = byte(i)
	}
	plain := []byte("a tarball of a browser profile")
	sealed, err := SealBlob(dek, plain)
	if err != nil {
		t.Fatal(err)
	}
	// on-disk codec round trip
	got, err := Decode(Encode(sealed))
	if err != nil {
		t.Fatal(err)
	}
	out, err := OpenBlob(dek, got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, plain) {
		t.Fatal("blob round-trip mismatch")
	}
	// a wrong DEK must fail the GCM tag
	wrong := make([]byte, 32)
	if _, err := OpenBlob(wrong, got); err == nil {
		t.Fatal("expected wrong-DEK open to fail")
	}
}

func TestKeyStoreZero(t *testing.T) {
	dek := make([]byte, 32)
	k := NewKeyStore(dek)
	if err := k.Use(func(d []byte) error {
		if len(d) != 32 {
			t.Fatal("bad dek len")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	k.Zero()
	if err := k.Use(func([]byte) error { return nil }); err == nil {
		t.Fatal("expected locked keystore to error after Zero")
	}
}
