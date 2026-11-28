package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/bridge"
	"github.com/ZhangNy301/agy-switch/pkg/proxy"
	"github.com/ZhangNy301/agy-switch/pkg/shim"
	"github.com/ZhangNy301/agy-switch/pkg/tui"
	"github.com/ZhangNy301/agy-switch/pkg/util"
	"github.com/spf13/cobra"
)

func (a *app) syncShimState() error {
	active, err := a.proxies.Active()
	if err != nil {
		return err
	}
	if active == nil {
		return shim.Uninstall(a.paths)
	}
	realAgy, err := a.deps.FindAgy(a.paths)
	if err != nil {
		return err
	}
	if err := shim.Install(a.paths, realAgy); err != nil {
		return err
	}
	sp := shim.ShimPathFor(a.paths)
	_ = a.proxies.SetShimPaths(sp, shim.BackupPathFor(sp))
	return nil
}

func (a *app) printProxyStatus(cmd *cobra.Command) error {
	list, err := a.proxies.List()
	if err != nil {
		return err
	}
	now := time.Now()
	if len(list) == 0 {
		cmd.Println("No proxy configured — run: agy-switch proxy")
	} else {
		for _, cfg := range list {
			marker := "○"
			stateTag := "off"
			if cfg.Active {
				marker = "●"
				switch cfg.State(now) {
				case "on":
					stateTag = "ACTIVE"
				case "expired_fallback":
					stateTag = "ACTIVE but EXPIRED — falls back to direct"
				default:
					stateTag = "ACTIVE but EXPIRED — agy will refuse to run"
				}
			}
			cmd.Printf("%s %s: %s\n", marker, cfg.Name, cfg.RawURL(true))
			cmd.Printf("  state: %s; expires: %s\n", stateTag, util.FormatExpiry(cfg.ExpiresAt, now))
			if cfg.Check != nil {
				cmd.Printf("  exit: %s\n", formatPlainProbe(cfg.Check, now))
			}
		}
	}

	if installed, shimPath := shim.Status(a.paths, a.proxies); installed {
		cmd.Printf("shim: installed at %s\n", shimPath)
	} else {
		cmd.Println("shim: NOT active")
	}

	if active, _ := a.proxies.Active(); active != nil && active.IsSOCKS() {
		running, port, _ := bridge.Status(a.paths)
		if running {
			cmd.Printf("bridge: running on port %d\n", port)
		} else {
			cmd.Println("bridge: stopped (starts automatically on agy run)")
		}
	}

	if !a.deps.DisableNetCheck {
		netState, _ := a.checkNetwork(cmd.Context())
		switch netState {
		case proxy.NetStateOK:
			cmd.Println("network: ok")
		default:
			cmd.Println("network: Google unreachable")
		}
	}
	return nil
}

func formatPlainProbe(chk *proxy.ProbeResult, now time.Time) string {
	if !chk.OK {
		if chk.Err != "" {
			return "unreachable — " + chk.Err
		}
		return "unreachable"
	}
	if chk.ExitIP == "" {
		return "tunnel ok · exit geo unknown"
	}
	loc := strings.TrimSpace(strings.Join([]string{chk.CC, chk.City}, " "))
	kind := chk.Kind
	if kind == "" {
		kind = "?"
	}
	age := util.FormatRelativeTime(time.Unix(chk.At, 0).UTC(), now)
	out := fmt.Sprintf("%s · %s", loc, kind)
	if chk.ISP != "" {
		out += " · " + chk.ISP
	}
	out += " (" + age + ")"
	return out
}

func (a *app) proxyCommand() *cobra.Command {
	paths, ps := a.paths, a.proxies
	proxyCmd := &cobra.Command{
		Use:   "proxy",
		Short: "Manage and route agy through proxies",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !a.deps.IsTTY() {
				return a.printProxyStatus(cmd)
			}
			exitMsg, err := tui.RunProxyManagerWithOptions(ps, tui.ProxyOptions{
				OnEnable: func(string) error {
					realAgy, err := a.deps.FindAgy(paths)
					if err != nil {
						return err
					}
					if err := shim.Install(paths, realAgy); err != nil {
						return err
					}
					sp := shim.ShimPathFor(paths)
					_ = ps.SetShimPaths(sp, shim.BackupPathFor(sp))
					return nil
				},
				OnDisable: func() error {
					return shim.Uninstall(paths)
				},
			})
			if err != nil {
				return err
			}
			if exitMsg != "" {
				cmd.Println(exitMsg)
			}
			return a.syncShimState()
		},
	}

	proxyOnCmd := &cobra.Command{
		Use:   "on [name]",
		Short: "Enable proxy routing",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			list, err := ps.List()
			if err != nil {
				return err
			}
			if len(list) == 0 {
				return fmt.Errorf("no proxy configured yet; add one with 'agy-switch proxy add'")
			}
			name := ""
			if len(args) > 0 {
				name = args[0]
			} else if active, _ := ps.Active(); active != nil {
				name = active.Name
			} else if len(list) == 1 {
				name = list[0].Name
			} else {
				names := make([]string, len(list))
				for i, cfg := range list {
					names[i] = cfg.Name
				}
				return fmt.Errorf("more than one proxy — pick one: agy-switch proxy on <name> (%s)", strings.Join(names, ", "))
			}

			var target *proxy.Config
			for i := range list {
				if list[i].Name == name {
					target = &list[i]
					break
				}
			}
			if target == nil {
				return fmt.Errorf("proxy %q not found; add one with 'agy-switch proxy add'", name)
			}
			realAgy, err := a.deps.FindAgy(paths)
			if err != nil {
				return err
			}
			if err := shim.Install(paths, realAgy); err != nil {
				return err
			}
			sp := shim.ShimPathFor(paths)
			_ = ps.SetShimPaths(sp, shim.BackupPathFor(sp))
			if err := ps.SetActive(name, true); err != nil {
				return err
			}
			warn := ""
			if target.State(time.Now()) != "on" {
				warn = " (expired!)"
			}
			cmd.Printf("Proxy enabled (%s: %s)%s. agy commands will now route through the configured proxy.\n", name, target.RawURL(true), warn)
			return nil
		},
	}

	proxyOffCmd := &cobra.Command{
		Use:   "off",
		Short: "Disable proxy routing",
		RunE: func(cmd *cobra.Command, args []string) error {
			active, err := ps.Active()
			if err != nil {
				return err
			}
			if active != nil {
				if err := ps.SetActive(active.Name, false); err != nil {
					return err
				}
			}
			if err := shim.Uninstall(paths); err != nil {
				return err
			}
			cmd.Println("Proxy disabled.")
			return nil
		},
	}

	proxyStatusCmd := &cobra.Command{
		Use:   "status",
		Short: "Display proxy and network status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.printProxyStatus(cmd)
		},
	}

	proxyRenameCmd := &cobra.Command{
		Use:   "rename <old> <new>",
		Short: "Rename a configured proxy",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := ps.Rename(args[0], args[1]); err != nil {
				return err
			}
			cmd.Printf("Renamed proxy '%s' -> '%s'\n", args[0], args[1])
			return nil
		},
	}

	proxyDeleteCmd := &cobra.Command{
		Use:     "delete <name>",
		Aliases: []string{"rm"},
		Short:   "Delete a configured proxy",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			active, err := ps.Active()
			if err != nil {
				return err
			}
			wasActive := active != nil && active.Name == name
			if _, err := ps.Get(name); err != nil {
				return err
			}
			if err := ps.Delete(name); err != nil {
				return err
			}
			if wasActive {
				if err := shim.Uninstall(paths); err != nil {
					return err
				}
			}
			cmd.Printf("Deleted proxy: %s\n", name)
			return nil
		},
	}

	proxyCmd.AddCommand(proxyOnCmd, proxyOffCmd, proxyStatusCmd, buildProxyAddCmd(paths, ps), buildProxyEditCmd(ps), proxyRenameCmd, proxyDeleteCmd)

	return proxyCmd
}
