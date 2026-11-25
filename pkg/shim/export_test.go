package shim

import (
	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/proxy"
)

// SetEnsureBridgeForTest swaps the bridge launcher in tests and returns the previous function.
func SetEnsureBridgeForTest(fn func(*config.Paths, *proxy.Config) (int, string, error)) func(*config.Paths, *proxy.Config) (int, string, error) {
	prev := ensureBridgeFn
	ensureBridgeFn = fn
	return prev
}
