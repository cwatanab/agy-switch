package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/bridge"
	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/keyring"
	"github.com/ZhangNy301/agy-switch/pkg/profile"
	"github.com/ZhangNy301/agy-switch/pkg/proxy"
	"github.com/ZhangNy301/agy-switch/pkg/quota"
	"github.com/ZhangNy301/agy-switch/pkg/shim"
)

// Dependencies supplies the CLI's side effects. Nil fields use production defaults.
type Dependencies struct {
	TokenStore        keyring.TokenStore
	HTTPClient        *http.Client
	RunProcess        func(*exec.Cmd) error
	FindAgy           func(*config.Paths) (string, error)
	LaunchAgy         func(*config.Paths, []string) (int, error)
	IsTTY             func() bool
	CountAgyProcesses func() (int, error)
	NetCheck          func(context.Context) (string, string)
	DisableNetCheck   bool
}

type app struct {
	paths    *config.Paths
	deps     Dependencies
	profiles *profile.Manager
	proxies  *proxy.Store
	quota    *quota.Client
}

func defaultIsTTY() bool {
	inInfo, err1 := os.Stdin.Stat()
	outInfo, err2 := os.Stdout.Stat()
	if err1 != nil || err2 != nil {
		return false
	}
	return (inInfo.Mode()&os.ModeCharDevice) != 0 && (outInfo.Mode()&os.ModeCharDevice) != 0
}

func newApp(paths *config.Paths, deps Dependencies) *app {
	proxies := proxy.NewStore(paths)
	if deps.TokenStore == nil {
		deps.TokenStore = keyring.DefaultStore(paths)
	}
	if deps.HTTPClient == nil {
		deps.HTTPClient = proxy.NewManagedHTTPClient(proxies, 10*time.Second)
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
	if deps.IsTTY == nil {
		deps.IsTTY = defaultIsTTY
	}
	if deps.CountAgyProcesses == nil {
		deps.CountAgyProcesses = bridge.CountAgyProcesses
	}
	return &app{
		paths:    paths,
		deps:     deps,
		profiles: profile.NewManager(paths, deps.TokenStore),
		proxies:  proxies,
		quota:    quota.NewClientWithOptions(paths, quota.Options{HTTPClient: deps.HTTPClient, FindAgy: deps.FindAgy}),
	}
}

func (a *app) lookupEmail(ctx context.Context, accessToken string) string {
	email, _ := profile.LookupEmail(ctx, a.deps.HTTPClient, accessToken)
	return email
}

func (a *app) checkNetwork(ctx context.Context) (string, string) {
	if a.deps.NetCheck != nil {
		return a.deps.NetCheck(ctx)
	}
	if a.deps.DisableNetCheck {
		return proxy.NetStateOK, ""
	}
	return proxy.CheckNetwork(ctx, a.paths, a.proxies, proxy.NetCheckOptions{})
}

func readLine(r io.Reader) (string, error) {
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func confirmYesNo(r io.Reader, w io.Writer, prompt string) bool {
	_, _ = fmt.Fprint(w, prompt)
	ans, err := readLine(r)
	if err != nil {
		return false
	}
	low := strings.ToLower(ans)
	return low == "y" || low == "yes"
}
