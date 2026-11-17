package cli

import (
	"context"
	"net/http"
	"os/exec"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/keyring"
	"github.com/ZhangNy301/agy-switch/pkg/profile"
	"github.com/ZhangNy301/agy-switch/pkg/proxy"
	"github.com/ZhangNy301/agy-switch/pkg/quota"
	"github.com/ZhangNy301/agy-switch/pkg/shim"
)

// Dependencies supplies the CLI's side effects. Nil fields use production defaults.
type Dependencies struct {
	TokenStore keyring.TokenStore
	HTTPClient *http.Client
	RunProcess func(*exec.Cmd) error
	FindAgy    func(*config.Paths) (string, error)
	LaunchAgy  func(*config.Paths, []string) (int, error)
}

type app struct {
	paths    *config.Paths
	deps     Dependencies
	profiles *profile.Manager
	proxies  *proxy.Store
	quota    *quota.Client
}

func newApp(paths *config.Paths, deps Dependencies) *app {
	if deps.TokenStore == nil {
		deps.TokenStore = keyring.DefaultStore(paths)
	}
	if deps.HTTPClient == nil {
		deps.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if deps.RunProcess == nil {
		deps.RunProcess = (*exec.Cmd).Run
	}
	if deps.FindAgy == nil {
		deps.FindAgy = shim.FindRealAgy
	}
	if deps.LaunchAgy == nil {
		deps.LaunchAgy = shim.ExecuteRealAgy
	}
	return &app{
		paths:    paths,
		deps:     deps,
		profiles: profile.NewManager(paths, deps.TokenStore),
		proxies:  proxy.NewStore(paths),
		quota:    quota.NewClientWithOptions(paths, quota.Options{HTTPClient: deps.HTTPClient}),
	}
}

func (a *app) lookupEmail(ctx context.Context, accessToken string) string {
	email, _ := profile.LookupEmail(ctx, a.deps.HTTPClient, accessToken)
	return email
}
