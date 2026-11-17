package filelock_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/filelock"
)

func TestLockProcessHelper(t *testing.T) {
	path := os.Getenv("AGY_TEST_LOCK_PATH")
	if path == "" {
		t.Skip("subprocess helper")
	}
	release, err := filelock.Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	fmt.Println("lock-held")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestLockExcludesAnotherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.lock")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(exe, "-test.run=^TestLockProcessHelper$", "-test.timeout=10s")
	child.Env = append(os.Environ(), "AGY_TEST_LOCK_PATH="+path)
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = stdin.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			_ = child.Process.Kill()
		}
	})
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if scanner.Text() == "lock-held" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("child failed to acquire lock")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("child did not report lock readiness")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	release, err := filelock.Acquire(ctx, path)
	if err == nil {
		_ = release()
		t.Fatal("lock acquired while another process held it")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended lock error=%v, want deadline exceeded", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("child exited with error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("child did not release lock")
	}
	release, err = filelock.Acquire(context.Background(), path)
	if err != nil {
		t.Fatalf("lock remained held after child exit: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}
