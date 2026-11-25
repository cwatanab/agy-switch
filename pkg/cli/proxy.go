package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/proxy"
	"github.com/ZhangNy301/agy-switch/pkg/shim"
	"github.com/ZhangNy301/agy-switch/pkg/util"
	"github.com/spf13/cobra"
)

func buildProxyAddCmd(paths *config.Paths, store *proxy.Store) *cobra.Command {
	var cfg proxy.Config
	var passwordEnv string
	var validity string
	var activate bool
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a proxy (protocol defaults to automatic detection)",
		Args:  cobra.ExactArgs(1),
		Example: "  agy-switch proxy add work --server proxy.example.com --port 1080\n" +
			"  agy-switch proxy add work --server proxy.example.com --port 1080 --protocol socks5h --username user --password-env PROXY_PASSWORD --validity 30d --fallback --activate",
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
			if validity != "" {
				exp, err := util.ParseValidity(validity, time.Now())
				if err != nil {
					return err
				}
				cfg.ExpiresAt = exp
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
				sp := shim.ShimPathFor(paths)
				_ = store.SetShimPaths(sp, shim.BackupPathFor(sp))
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
	cmd.Flags().StringVar(&validity, "validity", "", "Expiration duration or date (e.g. 30d, 12h, 2026-10-01)")
	cmd.Flags().BoolVar(&cfg.Fallback, "fallback", false, "Fall back to direct connection when expired")
	cmd.Flags().BoolVar(&activate, "activate", false, "Enable this proxy and install the agy shim")
	_ = cmd.MarkFlagRequired("server")
	_ = cmd.MarkFlagRequired("port")
	return cmd
}

func buildProxyEditCmd(store *proxy.Store) *cobra.Command {
	var server, protocol, username, passwordEnv, validity string
	var port int
	var fallback bool
	cmd := &cobra.Command{
		Use:   "edit <name>",
		Short: "Edit fields of a configured proxy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cfgPtr, err := store.Get(name)
			if err != nil {
				return err
			}
			cfg := *cfgPtr
			if cmd.Flags().Changed("server") {
				if strings.TrimSpace(server) == "" {
					return fmt.Errorf("server cannot be empty")
				}
				cfg.Server = server
				cfg.Host = server
			}
			if cmd.Flags().Changed("port") {
				if port < 1 || port > 65535 {
					return fmt.Errorf("port must be between 1 and 65535")
				}
				cfg.Port = port
			}
			if cmd.Flags().Changed("protocol") {
				p := strings.ToLower(protocol)
				if p != "http" && p != "https" && p != "socks5" && p != "socks5h" {
					return fmt.Errorf("protocol must be http, https, socks5, or socks5h")
				}
				cfg.Protocol = p
			}
			if cmd.Flags().Changed("username") {
				cfg.Username = username
			}
			if cmd.Flags().Changed("password-env") {
				if passwordEnv == "" {
					cfg.Password = ""
				} else {
					pw, ok := os.LookupEnv(passwordEnv)
					if !ok {
						return fmt.Errorf("password environment variable %q is not set", passwordEnv)
					}
					cfg.Password = pw
				}
			}
			if cmd.Flags().Changed("validity") {
				exp, err := util.ParseValidity(validity, time.Now())
				if err != nil {
					return err
				}
				cfg.ExpiresAt = exp
			}
			if cmd.Flags().Changed("fallback") {
				cfg.Fallback = fallback
			}
			if err := store.Update(name, cfg); err != nil {
				return err
			}
			cmd.Printf("Updated proxy: %s (%s)\n", cfg.Name, cfg.RawURL(true))
			return nil
		},
	}
	cmd.Flags().StringVar(&server, "server", "", "Proxy hostname or IP address")
	cmd.Flags().IntVar(&port, "port", 0, "Proxy port")
	cmd.Flags().StringVar(&protocol, "protocol", "", "http, https, socks5, or socks5h")
	cmd.Flags().StringVar(&username, "username", "", "Proxy username")
	cmd.Flags().StringVar(&passwordEnv, "password-env", "", "Environment variable containing the proxy password")
	cmd.Flags().StringVar(&validity, "validity", "", "Expiration duration or date (e.g. 30d, 12h, 2026-10-01, never)")
	cmd.Flags().BoolVar(&fallback, "fallback", false, "Fall back to direct connection when expired")
	return cmd
}
