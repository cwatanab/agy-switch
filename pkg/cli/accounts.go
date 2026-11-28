package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/profile"
	"github.com/ZhangNy301/agy-switch/pkg/quota"
	"github.com/ZhangNy301/agy-switch/pkg/tui"
	"github.com/spf13/cobra"
)

func (a *app) adoptLiveIfOrphan(ctx context.Context, cmd *cobra.Command) (string, error) {
	profiles, err := a.profiles.List()
	if err != nil || len(profiles) > 0 {
		return "", err
	}
	data, err := a.deps.TokenStore.ReadLive()
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	tok, err := profile.ParseTokenJSON(data)
	if err != nil {
		return "", err
	}
	if tok.AccessToken == "" && tok.RefreshToken == "" {
		return "", nil
	}
	name, err := a.profiles.Save("", data)
	if err != nil {
		return "", err
	}
	if err := a.profiles.SetCurrent(name); err != nil {
		return "", err
	}
	_ = a.profiles.RecordActivated(name, time.Now())
	if info, qErr := a.quota.FetchContext(ctx, tok, false); qErr == nil && info != nil && info.RefreshedToken != nil {
		if updated, uErr := profile.UpdateRawToken(info.RefreshedToken); uErr == nil {
			_ = a.profiles.UpdateToken(name, updated)
		}
		if info.Email != "" {
			tok.Email = info.Email
		}
	}

	cmd.Printf("Adopted existing login as profile: %s (%s)\n", name, tok.Email)
	return name, nil
}

func (a *app) warnRunningAgy(cmd *cobra.Command) {
	if a.deps.CountAgyProcesses == nil {
		return
	}
	n, err := a.deps.CountAgyProcesses()
	if err == nil && n > 0 {
		cmd.PrintErrf("note: %d agy session(s) still running with the previous account — they keep it until restarted.\n  (their hourly token refresh may briefly overwrite the live credential; every new agy start auto-corrects back to this account)\n", n)
	}
}

func (a *app) pickAndLaunch(cmd *cobra.Command, args []string) error {
	pm, qc, paths := a.profiles, a.quota, a.paths
	if _, err := a.adoptLiveIfOrphan(cmd.Context(), cmd); err != nil {
		return err
	}
	opts := tui.PickerOptions{
		ActiveName: pm.ActiveName(),
		Action:     "switch",
	}
	if !a.deps.DisableNetCheck {
		opts.NetCheckFn = a.checkNetwork
	}
	selected, err := tui.RunPickerWithOptions(cmd.Context(), pm, qc, opts)
	if err != nil {
		return err
	}
	if selected != "" {
		if err := pm.Use(selected); err != nil {
			return fmt.Errorf("switch to profile %s: %w", selected, err)
		}
		cmd.Printf("Switched to profile: %s\n", selected)
		a.warnRunningAgy(cmd)
		return a.deps.StartAgy(paths, nil)
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
	var refresh bool
	lsCmd := &cobra.Command{
		Use:   "ls",
		Short: "Plain list of accounts (non-interactive)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := a.adoptLiveIfOrphan(cmd.Context(), cmd); err != nil {
				return err
			}
			profiles, err := pm.List()
			if err != nil {
				return err
			}
			if len(profiles) == 0 {
				cmd.Println("No profiles found.")
				return nil
			}
			type lsQuotaResult struct {
				info *quota.QuotaInfo
				err  error
			}
			results := make([]lsQuotaResult, len(profiles))
			var wg sync.WaitGroup
			for i := range profiles {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					results[idx].info, results[idx].err = qc.FetchContext(cmd.Context(), &profiles[idx].Token, refresh)
				}(i)
			}
			wg.Wait()

			var warnings []string
			for i := range profiles {
				qInfo, fetchErr := results[i].info, results[i].err
				if qInfo != nil {
					if qInfo.Tier != "" && qInfo.Tier != "?" {
						profiles[i].Tier = qInfo.Tier
					}
					if qInfo.Email != "" && qInfo.Email != profiles[i].Email {
						profiles[i].Email = qInfo.Email
					}
					if qInfo.RefreshedToken != nil {
						profiles[i].Token = *qInfo.RefreshedToken
						if updated, uErr := profile.UpdateRawToken(qInfo.RefreshedToken); uErr == nil {
							_ = pm.UpdateToken(profiles[i].Name, updated)
						}
					}
				}
				if fetchErr != nil {
					label := profiles[i].Name
					if profiles[i].Email != "" {
						label += " (" + profiles[i].Email + ")"
					}
					statusNote := "no cached quota"
					if qInfo != nil && !qInfo.UpdatedAt.IsZero() {
						statusNote = "cached " + qInfo.UpdatedAt.Local().Format("2006-01-02 15:04")
					}
					warnings = append(warnings, fmt.Sprintf("warning: quota refresh failed for %s (%s): %v", label, statusNote, fetchErr))
				}
			}

			activeName := pm.ActiveName()
			widths := tui.ComputeAccountBlockWidths(profiles)
			now := time.Now()
			for i, p := range profiles {
				block := tui.FormatAccountBlock(p, results[i].info, widths, p.Name == activeName, false, now)
				for _, line := range block {
					cmd.Println(line)
				}
				cmd.Println()
			}
			if clobbered, cur := pm.LiveWasClobbered(); clobbered {
				cmd.PrintErrf("warning: the live login was overwritten by a still-running agy of another account;\n  the next agy start auto-corrects back to '%s'\n", cur)
			}
			if !refresh {
				cmd.Println("quota shown from cache when fresh; pass -r/--refresh or use the interactive picker to refresh")
			}
			for _, w := range warnings {
				cmd.PrintErrln(w)
			}
			return nil
		},
	}
	lsCmd.Flags().BoolVarP(&refresh, "refresh", "r", false, "Force refresh quotas from API")

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
			a.warnRunningAgy(cmd)
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
	var revoke bool
	logoutCmd := &cobra.Command{
		Use:   "logout [name]",
		Args:  cobra.MaximumNArgs(1),
		Short: "Remove an account profile from this machine",
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			interactivePick := false
			if len(args) > 0 {
				name = args[0]
			} else {
				if !a.deps.IsTTY() {
					return fmt.Errorf("usage: agy-switch logout <name> [--revoke]")
				}
				selected, err := tui.RunPickerWithOptions(cmd.Context(), pm, qc, tui.PickerOptions{
					ActiveName: pm.ActiveName(),
					Action:     "logout",
				})
				if err != nil {
					return err
				}
				name = selected
				interactivePick = true
			}
			if name == "" {
				return nil
			}

			profPath := filepath.Join(paths.ProfilesDir, name+".json")
			profData, err := os.ReadFile(profPath)
			if err != nil {
				return fmt.Errorf("profile %q not found: %w", name, err)
			}
			tok, err := profile.ParseTokenJSON(profData)
			if err != nil {
				return fmt.Errorf("parse profile %q: %w", name, err)
			}

			if interactivePick {
				verb := "Log out"
				if revoke {
					verb = "REVOKE the Google grant for"
				}
				if !confirmYesNo(cmd.InOrStdin(), cmd.OutOrStdout(), fmt.Sprintf("%s '%s' (%s)? [y/N] ", verb, name, tok.Email)) {
					cmd.Println("no change")
					return nil
				}
			}

			if revoke {
				if err := qc.RevokeToken(cmd.Context(), tok.RefreshToken); err != nil {
					return fmt.Errorf("revoke failed (%w) — the grant is still valid, nothing was deleted", err)
				}
				cmd.Printf("Grant revoked at Google for %s\n", tok.Email)
			}

			wasLive := false
			if cur, cErr := pm.Current(); cErr == nil && cur == name {
				wasLive = true
			} else if tok.RefreshToken != "" {
				if liveData, lErr := a.deps.TokenStore.ReadLive(); lErr == nil {
					if liveTok, pErr := profile.ParseTokenJSON(liveData); pErr == nil && liveTok.RefreshToken == tok.RefreshToken {
						wasLive = true
					}
				}
			}

			if err := pm.Delete(name); err != nil {
				return err
			}
			if tok.Email != "" {
				if err := qc.ClearCached(tok.Email); err != nil {
					cmd.PrintErrf("warning: profile removed, but quota cache cleanup failed: %v\n", err)
				}
			}
			cmd.Printf("Successfully logged out and removed profile: %s (%s)\n", name, tok.Email)
			if wasLive {
				cmd.Println("Live session cleared. Run 'agy-switch login' to connect an account.")
			}
			if !revoke {
				cmd.Println("The grant is still valid at Google — re-run with --revoke to kill it for real.")
			}
			return nil
		},
	}
	logoutCmd.Flags().BoolVar(&revoke, "revoke", false, "Also revoke the OAuth grant at Google")

	loginCmd := &cobra.Command{
		Use:   "login [name]",
		Short: "Add another account (browser login via real agy)",
		Args:  cobra.MaximumNArgs(1),
		RunE:  a.login,
	}

	return []*cobra.Command{listCmd, lsCmd, useCmd, renameCmd, logoutCmd, loginCmd}
}
