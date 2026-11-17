package proxy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ZhangNy301/agy-switch/pkg/config"
	"github.com/ZhangNy301/agy-switch/pkg/util"
)

type proxyDB struct {
	Version  int               `json:"version"`
	Active   *string           `json:"active"`
	ShimPath string            `json:"shim_path,omitempty"`
	RealPath string            `json:"real_path,omitempty"`
	Proxies  map[string]Config `json:"proxies"`
}

type Store struct {
	paths *config.Paths
}

func NewStore(paths *config.Paths) *Store {
	return &Store{paths: paths}
}

func (s *Store) dbPath() string {
	return filepath.Join(s.paths.DataDir, "proxy.json")
}

func (s *Store) loadDB() (*proxyDB, error) {
	db := &proxyDB{
		Version: 2,
		Proxies: make(map[string]Config),
	}
	data, err := os.ReadFile(s.dbPath())
	if err != nil {
		if os.IsNotExist(err) {
			return db, nil
		}
		return nil, fmt.Errorf("read proxy db: %w", err)
	}

	if err := json.Unmarshal(data, db); err != nil {
		// Review Focus #2: report error to prevent destructive overwrite of corrupt proxy config
		return nil, fmt.Errorf("proxy db file is corrupted: %w", err)
	}
	if db.Proxies == nil {
		db.Proxies = make(map[string]Config)
	}
	return db, nil
}

func (s *Store) saveDB(db *proxyDB) error {
	_ = config.EnsureDirs(s.paths)
	data, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal proxy db: %w", err)
	}
	return util.WriteFileAtomic(s.dbPath(), data, 0600)
}

func (s *Store) List() ([]Config, error) {
	db, err := s.loadDB()
	if err != nil {
		return nil, err
	}

	var list []Config
	for name, cfg := range db.Proxies {
		cfg.Name = name
		if db.Active != nil && *db.Active == name {
			cfg.Active = true
		} else {
			cfg.Active = false
		}
		list = append(list, cfg)
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})

	return list, nil
}

func (s *Store) Active() (*Config, error) {
	db, err := s.loadDB()
	if err != nil {
		return nil, err
	}
	if db.Active == nil {
		return nil, nil
	}
	cfg, ok := db.Proxies[*db.Active]
	if !ok {
		return nil, nil
	}
	cfg.Name = *db.Active
	cfg.Active = true
	return &cfg, nil
}

// Get returns a named proxy for sessions that still use it after routing is off.
func (s *Store) Get(name string) (*Config, error) {
	db, err := s.loadDB()
	if err != nil {
		return nil, err
	}
	cfg, ok := db.Proxies[name]
	if !ok {
		return nil, fmt.Errorf("proxy %q not found", name)
	}
	cfg.Name = name
	return &cfg, nil
}

func (s *Store) Save(cfg Config) error {
	if err := util.ValidateName(cfg.Name); err != nil {
		return err
	}
	db, err := s.loadDB()
	if err != nil {
		return err
	}

	if cfg.Server != "" && cfg.Host == "" {
		cfg.Host = cfg.Server
	}
	db.Proxies[cfg.Name] = cfg

	if cfg.Active {
		activeName := cfg.Name
		db.Active = &activeName
	}

	return s.saveDB(db)
}

func (s *Store) SetActive(name string, active bool) error {
	if err := util.ValidateName(name); err != nil {
		return err
	}
	db, err := s.loadDB()
	if err != nil {
		return err
	}

	if active {
		if _, ok := db.Proxies[name]; !ok {
			return fmt.Errorf("proxy %s not found", name)
		}
		db.Active = &name
	} else {
		if db.Active != nil && *db.Active == name {
			db.Active = nil
		}
	}

	return s.saveDB(db)
}

func (s *Store) Delete(name string) error {
	if err := util.ValidateName(name); err != nil {
		return err
	}
	db, err := s.loadDB()
	if err != nil {
		return err
	}

	delete(db.Proxies, name)
	if db.Active != nil && *db.Active == name {
		db.Active = nil
	}

	return s.saveDB(db)
}
