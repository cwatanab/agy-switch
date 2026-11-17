package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ZhangNy301/agy-switch/pkg/bridge"
	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/quota"
	"github.com/ZhangNy301/agy-switch/pkg/shim"
	"github.com/spf13/cobra"
)

var Version = "1.0.6"

// BuildRootCmd creates the CLI with the platform's default dependencies.
func BuildRootCmd(paths *config.Paths) *cobra.Command {
	return NewCommand(paths, Dependencies{})
}

// NewCommand creates the CLI with optional replacements for its I/O dependencies.
func NewCommand(paths *config.Paths, deps Dependencies) *cobra.Command {
	a := newApp(paths, deps)
	rootCmd := &cobra.Command{
		Use:   "agy-switch",
		Short: "Multi-account manager for Antigravity CLI (agy)",
		RunE:  a.pickAndLaunch,
	}
	var quotaTimeout = quota.DefaultTimeout
	var quotaConcurrency = quota.DefaultConcurrency
	rootCmd.PersistentFlags().DurationVar(&quotaTimeout, "quota-timeout", quota.DefaultTimeout, "Overall deadline for quota requests, refresh, and retries")
	rootCmd.PersistentFlags().IntVar(&quotaConcurrency, "quota-concurrency", quota.DefaultConcurrency, "Maximum simultaneous quota HTTP requests (1-64)")
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		return a.quota.SetLimits(quotaTimeout, quotaConcurrency)
	}
	// shim-exec command (internal)
	shimExecCmd := &cobra.Command{
		Use:                "shim-exec",
		Hidden:             true,
		DisableFlagParsing: true,
		SilenceUsage:       true,
		SilenceErrors:      true,
		RunE: func(cmd *cobra.Command, args []string) error {
			code, err := a.deps.LaunchAgy(paths, args)
			if err != nil {
				cmd.PrintErrln(err)
			}
			return launchResult(code, err)
		},
	}

	// version command
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Printf("agy-switch %s (Go native)\n", Version)
		},
	}

	rootCmd.AddCommand(a.accountCommands()...)
	rootCmd.AddCommand(a.proxyCommand(), a.bridgeCommand(), shimExecCmd, versionCmd)
	rootCmd.Version = Version
	return rootCmd
}

func Execute(defaultPicker bool) {
	paths, err := config.ResolvePaths()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	base := strings.ToLower(filepath.Base(os.Args[0]))
	// Dispatch before multi-call handling: a Windows agy.exe shim can spawn us.
	if len(os.Args) == 3 && os.Args[1] == bridge.DaemonArg {
		daemonPaths := &config.Paths{DataDir: os.Args[2]}
		if err := bridge.RunDaemon(daemonPaths); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if (strings.HasPrefix(base, "agy") && !strings.Contains(base, "switch")) || base == "agy.exe" {
		// Multi-call shim: invoked directly as agy or agy.exe
		code, err := shim.ExecuteRealAgy(paths, os.Args[1:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "agy shim error: %v\n", err)
		}
		os.Exit(code)
	}

	rootCmd := BuildRootCmd(paths)
	if err := rootCmd.Execute(); err != nil {
		var childErr childExitError
		if errors.As(err, &childErr) && childErr.code > 0 {
			os.Exit(childErr.code)
		}
		os.Exit(1)
	}
}

type childExitError struct{ code int }

func (e childExitError) Error() string { return fmt.Sprintf("agy exited with code %d", e.code) }

func launchResult(code int, err error) error {
	if err != nil {
		return err
	}
	if code != 0 {
		return childExitError{code: code}
	}
	return nil
}
