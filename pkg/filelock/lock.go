// Package filelock provides cancellable locks for shared on-disk state.
package filelock

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// Acquire waits for an exclusive lock and returns a function that releases it.
// The five-second acquisition limit does not limit the protected operation.
func Acquire(ctx context.Context, path string) (func() error, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	lock := flock.New(path, flock.SetPermissions(0600))
	ok, err := lock.TryLockContext(ctx, 20*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("acquire state lock: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("acquire state lock: %w", ctx.Err())
	}
	return lock.Close, nil
}
