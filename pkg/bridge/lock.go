package bridge

import (
	"github.com/gofrs/flock"
)

type Lock struct {
	f *flock.Flock
}

func NewLock(path string) *Lock {
	return &Lock{
		f: flock.New(path),
	}
}

func (l *Lock) TryLock() (bool, error) {
	return l.f.TryLock()
}

func (l *Lock) Unlock() error {
	return l.f.Unlock()
}
