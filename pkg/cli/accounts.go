package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ZhangNy301/agy-switch/pkg/profile"
	"github.com/ZhangNy301/agy-switch/pkg/tui"
	"github.com/spf13/cobra"
)

func (a *app) pickAndLaunch(cmd *cobra.Command, args []string) error {
	pm, qc, paths := a.profiles, a.quota, a.paths
	if _, err := pm.AdoptLiveIfOrphan(); err != nil {
		return err
	}
	selected, err := tui.RunPickerContext(cmd.Context(), pm, qc)
	if err != nil {
		return err
	}
	if selected != "" {
		if err := pm.Use(selected); err != nil {
			return fmt.Errorf("switch to profile %s: %w", selected, err)
		}
		cmd.Printf("Switched to profile: %s\n", selected)
		code, execErr := a.deps.LaunchAgy(paths, nil)
		return launchResult(code, execErr)
	}
	return nil
}

func (a *app) accountCommands() []*cobra.Command {
	pm, qc, paths := a.profiles, a.quota, a.paths
	// list / picker command
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "Interactive account picker (switch + launch agy)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.pickAndLaunch(cmd, args)
		},
	}

	// ls command
	lsCmd := &cobra.Command{
		Use:   "ls",
		Short: "Plain list of accounts (non-interactive)",
		RunE: func(cmd *cobra.Command, args []string) error {
			adopted, err := pm.AdoptLiveIfOrphan()
			if err != nil {
				return err
			}
			if adopted != "" {
				cmd.Printf("Adopted existing login as profile: %s\n", adopted)
			}
			profiles, err := pm.List()
			if err != nil {
				return err
			}
			if len(profiles) == 0 {
				cmd.Println("No profiles found.")
				return nil
			}
			cmd.Printf("%-16s %-8s %-14s %-14s %s\n", "NAME", "TIER", "GEMINI (5h)", "CLAUDE (5h)", "EMAIL")
			cmd.Printf("%s\n", strings.Repeat("-", 75))
			for _, p := range profiles {
				qInfo, _ := qc.FetchContext(cmd.Context(), &p.Token, false)
				tier := p.Tier
				if qInfo != nil && qInfo.Tier != "?" && qInfo.Tier != "" {
					tier = qInfo.Tier
				}
				if tier == "" {
					tier = "?"
				}

				geminiStr := "—"
				claudeStr := "—"
				if qInfo != nil {
					if qInfo.HasGemini {
						geminiStr = fmt.Sprintf("%3.0f%%", qInfo.Gemini5hFraction*100)
					}
					if qInfo.HasClaude {
						claudeStr = fmt.Sprintf("%3.0f%%", qInfo.Claude5hFraction*100)
					}
				}
				cmd.Printf("%-16s %-8s %-14s %-14s %s\n", p.Name, tier, geminiStr, claudeStr, p.Email)
			}
			return nil
		},
	}

	// use command
	useCmd := &cobra.Command{
		Use:   "use <name>",
		Short: "Switch to profile <name> non-interactively",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := pm.Use(name); err != nil {
				return err
			}
			cmd.Printf("Switched to profile: %s\n", name)
			return nil
		},
	}

	// rename command
	renameCmd := &cobra.Command{
		Use:   "rename <old> <new>",
		Short: "Rename a profile",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return pm.Rename(args[0], args[1])
		},
	}

	// logout command
	logoutCmd := &cobra.Command{
		Use:   "logout [name]",
		Args:  cobra.MaximumNArgs(1),
		Short: "Remove an account profile from this machine",
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) > 0 {
				name = args[0]
			} else {
				selected, err := tui.RunPickerContext(cmd.Context(), pm, qc)
				if err != nil {
					return err
				}
				name = selected
			}
			if name == "" {
				return nil
			}

			// Read profile email to clear quota cache
			var email string
			if profData, err := os.ReadFile(filepath.Join(paths.ProfilesDir, name+".json")); err == nil {
				if tok, err := profile.ParseTokenJSON(profData); err == nil {
					email = tok.Email
				}
			}

			if err := pm.Delete(name); err != nil {
				return err
			}
			if email != "" {
				if err := qc.ClearCached(email); err != nil {
					cmd.PrintErrf("warning: profile removed, but quota cache cleanup failed: %v\n", err)
				}
			}
			cmd.Printf("Successfully logged out and removed profile: %s\n", name)
			cmd.Println("Live session cleared. Run 'agy-switch login' to connect an account.")
			return nil
		},
	}

	loginCmd := &cobra.Command{
		Use:   "login [name]",
		Short: "Add another account (browser login via real agy)",
		Args:  cobra.MaximumNArgs(1),
		RunE:  a.login,
	}

	return []*cobra.Command{listCmd, lsCmd, useCmd, renameCmd, logoutCmd, loginCmd}
}
