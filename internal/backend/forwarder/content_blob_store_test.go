package forwarder

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

func TestContentBlobStorePutGetIsIdempotent(t *testing.T) {
	store := NewContentBlobStore(t.TempDir())
	data := []byte("stable blob bytes")
	id := sha256.Sum256(data)
	if err := store.Put(id[:], data); err != nil {
		t.Fatalf("first Put() error = %v", err)
	}
	if err := store.Put(id[:], append([]byte(nil), data...)); err != nil {
		t.Fatalf("second Put() error = %v", err)
	}
	got, err := store.Get(id[:])
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Get() = %q, want %q", got, data)
	}
	got[0] ^= 0xff
	again, err := store.Get(id[:])
	if err != nil {
		t.Fatalf("second Get() error = %v", err)
	}
	if !bytes.Equal(again, data) {
		t.Fatalf("stored data was mutated: %q", again)
	}
}

func TestContentBlobStoreRejectsMismatchedID(t *testing.T) {
	store := NewContentBlobStore(t.TempDir())
	if err := store.Put(bytes.Repeat([]byte{0xff}, sha256.Size), []byte("payload")); err == nil {
		t.Fatal("Put() accepted mismatched content id")
	}
}
