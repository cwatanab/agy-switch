package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/proxy"
	"github.com/ZhangNy301/agy-switch/pkg/shim"
	"github.com/ZhangNy301/agy-switch/pkg/util"
	"github.com/spf13/cobra"
)

func buildProxyAddCmd(paths *config.Paths, store *proxy.Store) *cobra.Command {
	var cfg proxy.Config
	var passwordEnv string
	var activate bool
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a proxy (protocol defaults to automatic detection)",
		Args:  cobra.ExactArgs(1),
		Example: "  agy-switch proxy add work --server proxy.example.com --port 1080\n" +
			"  agy-switch proxy add work --server proxy.example.com --port 1080 --protocol socks5h --username user --password-env PROXY_PASSWORD --activate",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg.Name = args[0]
			if err := util.ValidateName(cfg.Name); err != nil {
				return err
			}
			if strings.TrimSpace(cfg.Server) == "" || cfg.Port < 1 || cfg.Port > 65535 {
				return fmt.Errorf("server and a port between 1 and 65535 are required")
			}
			list, err := store.List()
			if err != nil {
				return err
			}
			for _, existing := range list {
				if existing.Name == cfg.Name {
					return fmt.Errorf("proxy %q already exists", cfg.Name)
				}
			}
			if passwordEnv != "" {
				var ok bool
				cfg.Password, ok = os.LookupEnv(passwordEnv)
				if !ok {
					return fmt.Errorf("password environment variable %q is not set", passwordEnv)
				}
			}
			cfg.Protocol = strings.ToLower(cfg.Protocol)
			switch cfg.Protocol {
			case "auto":
				cfg.Protocol, err = proxy.RaceProtocol(cfg.Server, cfg.Port, cfg.Username, cfg.Password)
				if err != nil {
					return err
				}
			case "http", "https", "socks5", "socks5h":
			default:
				return fmt.Errorf("protocol must be auto, http, https, socks5, or socks5h")
			}
			if activate {
				realAgy, err := shim.FindRealAgy(paths)
				if err != nil {
					return err
				}
				if err := shim.Install(paths, realAgy); err != nil {
					return err
				}
			}
			cfg.Active = activate
			if err := store.Save(cfg); err != nil {
				return err
			}
			cmd.Printf("Added proxy: %s (%s)\n", cfg.Name, cfg.Protocol)
			return nil
		},
	}
	cmd.Flags().StringVar(&cfg.Server, "server", "", "Proxy hostname or IP address")
	cmd.Flags().IntVar(&cfg.Port, "port", 0, "Proxy port")
	cmd.Flags().StringVar(&cfg.Protocol, "protocol", "auto", "auto, http, https, socks5, or socks5h")
	cmd.Flags().StringVar(&cfg.Username, "username", "", "Proxy username")
	cmd.Flags().StringVar(&passwordEnv, "password-env", "", "Environment variable containing the proxy password")
	cmd.Flags().BoolVar(&activate, "activate", false, "Enable this proxy and install the agy shim")
	_ = cmd.MarkFlagRequired("server")
	_ = cmd.MarkFlagRequired("port")
	return cmd
}
