package profile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/keyring"
	"github.com/ZhangNy301/agy-switch/pkg/util"
)

type Profile struct {
	Name     string                 `json:"name"`
	Email    string                 `json:"email"`
	Tier     string                 `json:"tier,omitempty"`
	LastUsed time.Time              `json:"last_used"`
	Token    Token                  `json:"token"`
	RawToken map[string]interface{} `json:"raw_token"`
}

type Manager struct {
	paths    *config.Paths
	store    keyring.TokenStore
	locked   bool
	recovery *recoveryState
}

func NewManager(paths *config.Paths, store keyring.TokenStore) *Manager {
	return &Manager{
		paths: paths,
		store: store,
	}
}

func (m *Manager) profilePath(name string) string {
	return filepath.Join(m.paths.ProfilesDir, name+".json")
}

func (m *Manager) stashPath() string {
	return filepath.Join(m.paths.DataDir, ".stash-token")
}

func (m *Manager) List() ([]Profile, error) {
	if err := config.EnsureDirs(m.paths); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(m.paths.ProfilesDir)
	if err != nil {
		return nil, fmt.Errorf("read profiles directory: %w", err)
	}

	var profiles []Profile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		pPath := filepath.Join(m.paths.ProfilesDir, entry.Name())

		data, err := os.ReadFile(pPath)
		if err != nil {
			continue
		}

		tok, parseErr := parseCredential(data)
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "warning: profile %s corrupted: %v\n", entry.Name(), parseErr)
			continue
		}

		info, _ := entry.Info()
		var modTime time.Time
		if info != nil {
			modTime = info.ModTime()
		}

		p := Profile{
			Name:     name,
			Email:    tok.Email,
			LastUsed: modTime,
			Token:    *tok,
			RawToken: tok.Raw,
		}
		profiles = append(profiles, p)
	}

	sort.Slice(profiles, func(i, j int) bool {
		return profiles[i].Name < profiles[j].Name
	})

	return profiles, nil
}

func (m *Manager) snapshot(name string) error {
	if err := util.ValidateName(name); err != nil {
		return err
	}
	if err := config.EnsureDirs(m.paths); err != nil {
		return err
	}

	liveData, err := m.store.ReadLive()
	if err != nil {
		return fmt.Errorf("read live token: %w", err)
	}

	// Validate it parses as token JSON
	if _, err := parseCredential(liveData); err != nil {
		return fmt.Errorf("parse live token: %w", err)
	}

	target := m.profilePath(name)
	return m.writeFile(target, liveData, 0600)
}

// SaveLive preserves an account without replacing a different account's profile.
// A repeated login with the same grant updates its existing snapshot.
func (m *Manager) saveLive(base string) (string, error) {
	data, err := m.store.ReadLive()
	if err != nil {
		return "", err
	}
	return m.Save(base, data)
}

func (m *Manager) save(base string, data []byte) (string, error) {
	tok, err := ParseTokenJSON(data)
	if err != nil {
		return "", err
	}
	if tok.RefreshToken == "" && tok.AccessToken == "" {
		return "", fmt.Errorf("live credential has no access or refresh token")
	}
	profiles, err := m.List()
	if err != nil {
		return "", err
	}
	for _, p := range profiles {
		sameGrant := tok.RefreshToken != "" && tok.RefreshToken == p.Token.RefreshToken
		sameAccess := tok.RefreshToken == "" && tok.AccessToken == p.Token.AccessToken
		if sameGrant || sameAccess {
			return p.Name, m.writeFile(m.profilePath(p.Name), data, 0600)
		}
	}
	if base == "" {
		base = "default"
		if tok.Email != "" {
			base = strings.Split(tok.Email, "@")[0]
		}
		base = strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
				return r
			}
			return '-'
		}, base)
	}
	if err := util.ValidateName(base); err != nil {
		return "", err
	}
	for i := 1; ; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		path := m.profilePath(name)
		if _, err := os.Lstat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if err := m.writeFile(path, data, 0600); err != nil {
			return "", err
		}

		return name, nil
	}
}

func (m *Manager) currentPath() string {
	return filepath.Join(m.paths.DataDir, "current")
}

func (m *Manager) Current() (string, error) {
	data, err := os.ReadFile(m.currentPath())
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(data))
	if err := util.ValidateName(name); err != nil {
		return "", err
	}
	if _, err := os.Stat(m.profilePath(name)); err != nil {
		return "", err
	}
	return name, nil
}

func (m *Manager) setCurrent(name string) error {
	if err := util.ValidateName(name); err != nil {
		return err
	}
	if err := config.EnsureDirs(m.paths); err != nil {
		return err
	}
	return m.writeFile(m.currentPath(), []byte(name), 0644)
}

func (m *Manager) adoptLiveIfOrphan() (string, error) {
	profiles, err := m.List()
	if err != nil {
		return "", err
	}
	if len(profiles) > 0 {
		return "", nil
	}
	data, err := m.store.ReadLive()
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	tok, err := ParseTokenJSON(data)
	if err != nil {
		return "", err
	}
	if tok.AccessToken == "" && tok.RefreshToken == "" {
		return "", nil
	}
	name, err := m.Save("", data)
	if err != nil {
		return "", err
	}
	if err := m.SetCurrent(name); err != nil {
		return "", err
	}
	return name, nil
}

func (m *Manager) reassertCurrent() error {
	cur, err := m.Current()
	if os.IsNotExist(err) || cur == "" && err == nil {
		return nil
	}
	if err != nil {
		return err
	}

	profData, err := os.ReadFile(m.profilePath(cur))
	if err != nil {
		return err
	}
	profTok, err := parseCredential(profData)
	if err != nil {
		return err
	}

	liveData, err := m.store.ReadLive()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && len(liveData) > 0 {
		if liveTok, err := ParseTokenJSON(liveData); err == nil {
			if liveTok.RefreshToken != "" && liveTok.RefreshToken == profTok.RefreshToken {
				return nil
			}
		}
	}

	return m.writeLive(profData)
}

func (m *Manager) use(name string) error {
	if err := util.ValidateName(name); err != nil {
		return err
	}
	target := m.profilePath(name)
	data, err := os.ReadFile(target)
	if err != nil {
		return fmt.Errorf("read profile %s: %w", name, err)
	}

	if _, err := parseCredential(data); err != nil {
		return fmt.Errorf("profile %s is corrupted: %w", name, err)
	}

	if err := m.writeLive(data); err != nil {
		return fmt.Errorf("write live token: %w", err)
	}

	if err := m.SetCurrent(name); err != nil {
		return err
	}

	// Last-used display is best effort; it must not undo a completed switch.
	now := time.Now()
	_ = os.Chtimes(target, now, now)

	return nil
}

func parseCredential(data []byte) (*Token, error) {
	tok, err := ParseTokenJSON(data)
	if err != nil {
		return nil, err
	}
	if tok.AccessToken == "" && tok.RefreshToken == "" {
		return nil, fmt.Errorf("credential has no access or refresh token")
	}
	return tok, nil
}

func (m *Manager) rename(oldName, newName string) error {
	if err := util.ValidateName(oldName); err != nil {
		return err
	}
	if err := util.ValidateName(newName); err != nil {
		return err
	}

	oldPath := m.profilePath(oldName)
	newPath := m.profilePath(newName)

	if _, err := os.Stat(oldPath); err != nil {
		return fmt.Errorf("profile %s not found: %w", oldName, err)
	}
	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("profile %s already exists", newName)
	}

	data, err := os.ReadFile(oldPath)
	if err != nil {
		return err
	}
	if err := m.writeFile(newPath, data, 0600); err != nil {
		return err
	}
	isCurrent := false
	if cur, _ := m.Current(); cur == oldName {
		isCurrent = true
	}

	if err := m.removeFile(oldPath); err != nil {
		return err
	}

	if isCurrent {
		if err := m.SetCurrent(newName); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) delete(name string) error {
	if err := util.ValidateName(name); err != nil {
		return err
	}

	target := m.profilePath(name)
	data, readErr := os.ReadFile(target)

	cur, _ := m.Current()
	wasLive := (cur == name)
	if !wasLive && readErr == nil {
		if profTok, err := ParseTokenJSON(data); err == nil && profTok.RefreshToken != "" {
			if liveData, err := m.store.ReadLive(); err == nil {
				if liveTok, err := ParseTokenJSON(liveData); err == nil {
					if liveTok.RefreshToken == profTok.RefreshToken {
						wasLive = true
					}
				}
			}
		}
	}

	if wasLive {
		if err := m.deleteLive(); err != nil {
			return err
		}
		if err := m.removeFile(m.currentPath()); err != nil {
			return err
		}
	}

	if err := m.removeFile(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete profile %s: %w", name, err)
	}
	return nil
}

func (m *Manager) stash() error {
	liveData, err := m.store.ReadLive()
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to stash
		}
		return err
	}
	// Preserve the current login even when it has not been adopted by the picker.
	if _, err := m.SaveLive(""); err != nil {
		return fmt.Errorf("preserve existing account: %w", err)
	}
	if err := config.EnsureDirs(m.paths); err != nil {
		return err
	}
	if err := m.writeFile(m.stashPath(), liveData, 0600); err != nil {
		return fmt.Errorf("write stash: %w", err)
	}
	return m.deleteLive()
}

func (m *Manager) DropStash() error {
	return m.Transaction(context.Background(), func(tx *Manager) error { return tx.removeFile(tx.stashPath()) })
}

func (m *Manager) unstash() error {
	stashFile := m.stashPath()
	data, err := os.ReadFile(stashFile)
	if err != nil {
		return nil // no stash
	}
	if err := m.writeLive(data); err != nil {
		return err // keep the stash available for recovery
	}
	return m.removeFile(stashFile)
}

func (m *Manager) Snapshot(name string) error {
	return m.Transaction(context.Background(), func(tx *Manager) error { return tx.snapshot(name) })
}

func (m *Manager) SetCurrent(name string) error {
	return m.Transaction(context.Background(), func(tx *Manager) error { return tx.setCurrent(name) })
}

func (m *Manager) Use(name string) error {
	return m.Transaction(context.Background(), func(tx *Manager) error { return tx.use(name) })
}

func (m *Manager) Rename(oldName, newName string) error {
	return m.Transaction(context.Background(), func(tx *Manager) error { return tx.rename(oldName, newName) })
}

func (m *Manager) Delete(name string) error {
	return m.Transaction(context.Background(), func(tx *Manager) error { return tx.delete(name) })
}

func (m *Manager) ReassertCurrent() error {
	return m.Transaction(context.Background(), func(tx *Manager) error { return tx.reassertCurrent() })
}

func (m *Manager) Stash() error {
	return m.Transaction(context.Background(), func(tx *Manager) error { return tx.stash() })
}

func (m *Manager) Unstash() error {
	return m.Transaction(context.Background(), func(tx *Manager) error { return tx.unstash() })
}

// SaveLive preserves the live credential under a unique profile name.
func (m *Manager) SaveLive(base string) (name string, err error) {
	err = m.Transaction(context.Background(), func(tx *Manager) error {
		name, err = tx.saveLive(base)
		return err
	})
	return name, err
}

// Save preserves credential JSON, updating only a profile with the same grant.
func (m *Manager) Save(base string, data []byte) (name string, err error) {
	err = m.Transaction(context.Background(), func(tx *Manager) error {
		name, err = tx.save(base, data)
		return err
	})
	return name, err
}

func (m *Manager) AdoptLiveIfOrphan() (name string, err error) {
	err = m.Transaction(context.Background(), func(tx *Manager) error {
		name, err = tx.adoptLiveIfOrphan()
		return err
	})
	return name, err
}
