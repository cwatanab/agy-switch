package keyring

import (
	"errors"
	"testing"
)

type observedStore struct {
	data []byte
	err  error
}

func (s observedStore) ReadLive() ([]byte, error) { return s.data, s.err }
func (s observedStore) WriteLive([]byte) error    { return nil }
func (s observedStore) DeleteLive() error         { return nil }

func TestMirroredWriteRejectsStaleNativeCredential(t *testing.T) {
	denied := errors.New("native write denied")
	if err := verifyWrite(observedStore{data: []byte("old")}, []byte("new"), denied, nil); !errors.Is(err, denied) {
		t.Fatalf("write error=%v, want native failure", err)
	}
	if err := verifyWrite(observedStore{data: []byte("old")}, []byte("new"), nil, nil); err == nil {
		t.Fatal("silent native failure was accepted")
	}
	if err := verifyWrite(observedStore{data: []byte("new")}, []byte("new"), denied, nil); err != nil {
		t.Fatalf("readable file fallback rejected: %v", err)
	}
	mirrorErr := errors.New("mirror write denied")
	if err := verifyWrite(observedStore{data: []byte("new")}, []byte("new"), nil, mirrorErr); !errors.Is(err, mirrorErr) {
		t.Fatal("mirror failure ignored")
	}
}
