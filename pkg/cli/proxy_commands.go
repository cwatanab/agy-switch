package cli

import (
	"fmt"

	"github.com/ZhangNy301/agy-switch/pkg/proxy"
	"github.com/ZhangNy301/agy-switch/pkg/shim"
	"github.com/ZhangNy301/agy-switch/pkg/tui"
	"github.com/spf13/cobra"
)

func (a *app) proxyCommand() *cobra.Command {
	paths, ps := a.paths, a.proxies
	// proxy command
	proxyCmd := &cobra.Command{
		Use:   "proxy",
		Short: "Manage and route agy through proxies",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := tui.RunProxyManager(ps); err != nil {
				return err
			}
			active, err := ps.Active()
			if err != nil {
				return err
			}
			if active == nil {
				return shim.Uninstall(paths)
			}
			realAgy, err := a.deps.FindAgy(paths)
			if err != nil {
				return err
			}
			return shim.Install(paths, realAgy)
		},
	}

	proxyOnCmd := &cobra.Command{
		Use:   "on [name]",
		Short: "Enable proxy routing",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			active, err := ps.Active()
			if err != nil {
				return err
			}
			name := ""
			if active != nil {
				name = active.Name
			}
			if len(args) > 0 {
				name = args[0]
			}
			list, err := ps.List()
			if err != nil {
				return err
			}
			found := false
			for _, cfg := range list {
				found = found || cfg.Name == name
			}
			if !found {
				return fmt.Errorf("proxy %q not found; add one with 'agy-switch proxy add'", name)
			}
			realAgy, err := a.deps.FindAgy(paths)
			if err != nil {
				return err
			}
			if err := shim.Install(paths, realAgy); err != nil {
				return err
			}
			if err := ps.SetActive(name, true); err != nil {
				return err
			}
			cmd.Println("Proxy enabled. agy commands will now route through the configured proxy.")
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
			active, err := ps.Active()
			if err != nil {
				return err
			}
			if active == nil {
				cmd.Println("Proxy: OFF")
				return nil
			}
			cmd.Printf("Proxy: ON (%s: %s://%s:%d)\n", active.Name, active.Protocol, active.GetServer(), active.Port)
			cc, blocked, gErr := proxy.ProbeGeo(active.URL())
			if gErr == nil {
				cmd.Printf("Exit IP Country: %s (Blocked: %v)\n", cc, blocked)
			}
			return nil
		},
	}
	proxyCmd.AddCommand(proxyOnCmd, proxyOffCmd, proxyStatusCmd, buildProxyAddCmd(paths, ps))

	return proxyCmd
}
