package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ZhangNy301/agy-switch/pkg/profile"
	"github.com/ZhangNy301/agy-switch/pkg/shim"
	"github.com/spf13/cobra"
)

func (a *app) login(cmd *cobra.Command, args []string) error {
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
		base := ""
		if len(args) > 0 {
			base = strings.Map(func(r rune) rune {
				if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
					return r
				}
				return '-'
			}, args[0])
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
	cmd.Printf("Successfully saved and activated new profile: %s (%s)\n", name, tok.Email)
	// A quota failure must not undo an already committed login.
	_, _ = a.quota.FetchContext(cmd.Context(), tok, true)
	return nil
}
