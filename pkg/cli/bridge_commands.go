package cli

import (
	"github.com/ZhangNy301/agy-switch/pkg/bridge"
	"github.com/spf13/cobra"
)

func (a *app) bridgeCommand() *cobra.Command {
	paths := a.paths
	// bridge command
	bridgeCmd := &cobra.Command{
		Use:   "bridge",
		Short: "Inspect or manage the local SOCKS bridge",
	}
	bridgeStatusCmd := &cobra.Command{
		Use:   "status",
		Short: "Check bridge status",
		RunE: func(cmd *cobra.Command, args []string) error {
			running, port, _ := bridge.Status(paths)
			if running {
				cmd.Printf("Bridge is RUNNING on port %d\n", port)
			} else {
				cmd.Println("Bridge is STOPPED")
			}
			return nil
		},
	}
	bridgeStopCmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the local bridge",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := bridge.StopDaemon(paths); err != nil {
				return err
			}
			cmd.Println("Bridge stopped.")
			return nil
		},
	}
	bridgeCmd.AddCommand(bridgeStatusCmd, bridgeStopCmd)

	return bridgeCmd
}
