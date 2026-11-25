package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/profile"
	"github.com/ZhangNy301/agy-switch/pkg/proxy"
	"github.com/ZhangNy301/agy-switch/pkg/shim"
	"github.com/ZhangNy301/agy-switch/pkg/util"
	"github.com/spf13/cobra"
)

func sanitizeProfileBase(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			return r
		}
		return '-'
	}, s)
}

func (a *app) login(cmd *cobra.Command, args []string) error {
	if !a.deps.DisableNetCheck {
		state, cc := a.checkNetwork(cmd.Context())
		if state != proxy.NetStateOK {
			why := "Google's API is unreachable from this network"
			if state == proxy.NetStateRegion {
				if cc == "" {
					cc = "?"
				}
				why = fmt.Sprintf("your IP region (%s) is not supported by Google AI", cc)
			}
			cmd.PrintErrf("warning: %s — the login callback will likely hang.\n  fix: agy-switch proxy (add a proxy through a supported region first)\n", why)
			if !a.deps.IsTTY() {
				return fmt.Errorf("%s", why)
			}
			if !confirmYesNo(cmd.InOrStdin(), cmd.ErrOrStderr(), "  continue anyway? [y/N] ") {
				return fmt.Errorf("login cancelled")
			}
		}
	}

	realAgy, err := a.deps.FindAgy(a.paths)
	if err != nil {
		return err
	}
	var name string
	var tok *profile.Token
	err = a.profiles.Transaction(cmd.Context(), func(tx *profile.Manager) error {
		if err := tx.Stash(); err != nil {
			return fmt.Errorf("stash existing token: %w", err)
		}
		proxyEnv, err := shim.BuildProxyEnv(a.paths, a.proxies)
		if err != nil {
			return err
		}
		cmd.Println("Starting agy — log in with the NEW account in the browser.")
		cmd.Println("(tip: pick the right Google account in the browser's account chooser)")
		child := exec.CommandContext(cmd.Context(), realAgy)
		child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
		child.Env = append(os.Environ(), proxyEnv...)
		child.Env = append(child.Env, "AGY_SWITCH_NO_REASSERT=1")
		runErr := a.deps.RunProcess(child)
		if err := cmd.Context().Err(); err != nil {
			return err
		}
		data, err := a.deps.TokenStore.ReadLive()
		if err != nil {
			return errors.Join(fmt.Errorf("no new token found after login: %w", err), runErr)
		}
		tok, err = profile.ParseTokenJSON(data)
		if err != nil {
			return fmt.Errorf("parse new token: %w", err)
		}
		if tok.AccessToken != "" {
			if email := a.lookupEmail(cmd.Context(), tok.AccessToken); email != "" && email != tok.Email {
				tok.Email = email
				tok.Raw["id_token_email"] = email
				data, err = json.Marshal(tok.Raw)
				if err != nil {
					return err
				}
			}
		}

		// Check if grant already exists before prompting for a name.
		existingMatch := false
		if existing, lErr := tx.List(); lErr == nil {
			for _, p := range existing {
				if (tok.RefreshToken != "" && tok.RefreshToken == p.Token.RefreshToken) ||
					(tok.RefreshToken == "" && tok.AccessToken == p.Token.AccessToken) {
					existingMatch = true
					break
				}
			}
		}

		base := ""
		if len(args) > 0 {
			base = sanitizeProfileBase(args[0])
		} else if !existingMatch && a.deps.IsTTY() {
			def := "account"
			if tok.Email != "" {
				def = sanitizeProfileBase(strings.Split(tok.Email, "@")[0])
				if def == "" {
					def = "account"
				}
			}
			cmd.Printf("logged in as %s — save profile as [%s]: ", tok.Email, def)
			if entered, rErr := readLine(cmd.InOrStdin()); rErr == nil && entered != "" && util.ValidateName(entered) == nil {
				base = entered
			} else {
				base = def
			}
		}

		name, err = tx.Save(base, data)
		if err != nil {
			return fmt.Errorf("snapshot profile: %w", err)
		}
		if err := tx.Use(name); err != nil {
			return fmt.Errorf("activate profile %s: %w", name, err)
		}
		return tx.DropStash()
	})
	if err != nil {
		return err
	}
	_ = a.profiles.RecordActivated(name, time.Now())
	cmd.Printf("Successfully saved and activated new profile: %s (%s)\n", name, tok.Email)
	// A quota failure must not undo an already committed login.
	_, _ = a.quota.FetchContext(cmd.Context(), tok, true)
	return nil
}
