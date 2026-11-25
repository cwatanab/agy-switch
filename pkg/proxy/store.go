package proxy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

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

func (db *proxyDB) clone() *proxyDB {
	cp := &proxyDB{
		Version:  db.Version,
		ShimPath: db.ShimPath,
		RealPath: db.RealPath,
		Proxies:  make(map[string]Config, len(db.Proxies)),
	}
	if db.Active != nil {
		a := *db.Active
		cp.Active = &a
	}
	for k, v := range db.Proxies {
		if v.Check != nil {
			chk := *v.Check
			v.Check = &chk
		}
		cp.Proxies[k] = v
	}
	return cp
}

type Store struct {
	paths     *config.Paths
	mu        sync.Mutex
	cache     *proxyDB
	cacheInfo os.FileInfo
}

func NewStore(paths *config.Paths) *Store {
	return &Store{paths: paths}
}

func (s *Store) dbPath() string {
	return filepath.Join(s.paths.DataDir, "proxy.json")
}

func (s *Store) loadDBLocked() (*proxyDB, error) {
	path := s.dbPath()
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		s.cache = nil
		s.cacheInfo = nil
		return &proxyDB{Version: 2, Proxies: make(map[string]Config)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stat proxy db: %w", err)
	}
	if s.cache != nil && s.cacheInfo != nil &&
		os.SameFile(info, s.cacheInfo) && info.Size() == s.cacheInfo.Size() && info.ModTime() == s.cacheInfo.ModTime() {
		return s.cache, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.cache = nil
			s.cacheInfo = nil
			return &proxyDB{Version: 2, Proxies: make(map[string]Config)}, nil
		}
		return nil, fmt.Errorf("read proxy db: %w", err)
	}

	db := &proxyDB{
		Version: 2,
		Proxies: make(map[string]Config),
	}
	if err := json.Unmarshal(data, db); err != nil {
		return nil, fmt.Errorf("proxy db file is corrupted: %w", err)
	}
	if db.Proxies == nil {
		db.Proxies = make(map[string]Config)
	}
	s.cache = db
	s.cacheInfo = info
	return db, nil
}

func (s *Store) saveDBLocked(db *proxyDB) error {
	_ = config.EnsureDirs(s.paths)
	data, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal proxy db: %w", err)
	}
	path := s.dbPath()
	if err := util.WriteFileAtomic(path, data, 0600); err != nil {
		return err
	}
	if info, err := os.Stat(path); err == nil {
		s.cache = db
		s.cacheInfo = info
	} else {
		s.cache = nil
		s.cacheInfo = nil
	}
	return nil
}

func copyConfig(cfg Config) Config {
	if cfg.Check != nil {
		chk := *cfg.Check
		cfg.Check = &chk
	}
	return cfg
}

func (s *Store) List() ([]Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.loadDBLocked()
	if err != nil {
		return nil, err
	}

	list := make([]Config, 0, len(db.Proxies))
	for name, cfg := range db.Proxies {
		c := copyConfig(cfg)
		c.Name = name
		c.Active = db.Active != nil && *db.Active == name
		list = append(list, c)
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})

	return list, nil
}

func (s *Store) Active() (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.loadDBLocked()
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
	c := copyConfig(cfg)
	c.Name = *db.Active
	c.Active = true
	return &c, nil
}

// Get returns a named proxy for sessions that still use it after routing is off.
func (s *Store) Get(name string) (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.loadDBLocked()
	if err != nil {
		return nil, err
	}
	cfg, ok := db.Proxies[name]
	if !ok {
		return nil, fmt.Errorf("proxy %q not found", name)
	}
	c := copyConfig(cfg)
	c.Name = name
	c.Active = db.Active != nil && *db.Active == name
	return &c, nil
}

func (s *Store) Save(cfg Config) error {
	if err := util.ValidateName(cfg.Name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	loaded, err := s.loadDBLocked()
	if err != nil {
		return err
	}
	db := loaded.clone()

	if cfg.Server != "" && cfg.Host == "" {
		cfg.Host = cfg.Server
	}
	if cfg.Host != "" && cfg.Server == "" {
		cfg.Server = cfg.Host
	}
	db.Proxies[cfg.Name] = copyConfig(cfg)

	if cfg.Active {
		activeName := cfg.Name
		db.Active = &activeName
	}

	return s.saveDBLocked(db)
}

// Update modifies an existing proxy entry and optionally renames it atomically.
func (s *Store) Update(oldName string, cfg Config) error {
	if err := util.ValidateName(oldName); err != nil {
		return err
	}
	if err := util.ValidateName(cfg.Name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	loaded, err := s.loadDBLocked()
	if err != nil {
		return err
	}
	existing, ok := loaded.Proxies[oldName]
	if !ok {
		return fmt.Errorf("proxy %q not found", oldName)
	}
	db := loaded.clone()
	if cfg.Name != oldName {
		if _, exists := db.Proxies[cfg.Name]; exists {
			return fmt.Errorf("proxy %q already exists", cfg.Name)
		}
		delete(db.Proxies, oldName)
		if db.Active != nil && *db.Active == oldName {
			newName := cfg.Name
			db.Active = &newName
		}
	}
	if cfg.Server != "" && cfg.Host == "" {
		cfg.Host = cfg.Server
	}
	if cfg.Host != "" && cfg.Server == "" {
		cfg.Server = cfg.Host
	}
	if cfg.Check == nil && existing.Check != nil &&
		existing.GetServer() == cfg.GetServer() && existing.Port == cfg.Port && existing.Protocol == cfg.Protocol {
		cfg.Check = existing.Check
	}
	cfg.Active = db.Active != nil && *db.Active == cfg.Name
	db.Proxies[cfg.Name] = copyConfig(cfg)
	return s.saveDBLocked(db)
}

func (s *Store) Rename(oldName, newName string) error {
	if err := util.ValidateName(oldName); err != nil {
		return err
	}
	if err := util.ValidateName(newName); err != nil {
		return err
	}
	if oldName == newName {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	loaded, err := s.loadDBLocked()
	if err != nil {
		return err
	}
	cfg, ok := loaded.Proxies[oldName]
	if !ok {
		return fmt.Errorf("proxy %q not found", oldName)
	}
	if _, exists := loaded.Proxies[newName]; exists {
		return fmt.Errorf("proxy %q already exists", newName)
	}
	db := loaded.clone()
	delete(db.Proxies, oldName)
	cfg.Name = newName
	db.Proxies[newName] = copyConfig(cfg)
	if db.Active != nil && *db.Active == oldName {
		db.Active = &newName
	}
	return s.saveDBLocked(db)
}

func (s *Store) SaveProbe(name string, res *ProbeResult) error {
	if err := util.ValidateName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	loaded, err := s.loadDBLocked()
	if err != nil {
		return err
	}
	cfg, ok := loaded.Proxies[name]
	if !ok {
		return fmt.Errorf("proxy %q not found", name)
	}
	db := loaded.clone()
	if res != nil {
		chk := *res
		cfg.Check = &chk
		if res.CC != "" {
			cfg.Country = res.CC
		}
	} else {
		cfg.Check = nil
	}
	db.Proxies[name] = cfg
	return s.saveDBLocked(db)
}

func (s *Store) SetActive(name string, active bool) error {
	if err := util.ValidateName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	loaded, err := s.loadDBLocked()
	if err != nil {
		return err
	}
	db := loaded.clone()

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

	return s.saveDBLocked(db)
}

func (s *Store) Delete(name string) error {
	if err := util.ValidateName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	loaded, err := s.loadDBLocked()
	if err != nil {
		return err
	}
	db := loaded.clone()

	delete(db.Proxies, name)
	if db.Active != nil && *db.Active == name {
		db.Active = nil
	}

	return s.saveDBLocked(db)
}

func (s *Store) ShimPaths() (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.loadDBLocked()
	if err != nil {
		return "", "", err
	}
	return db.ShimPath, db.RealPath, nil
}

func (s *Store) SetShimPaths(shimPath, realPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	loaded, err := s.loadDBLocked()
	if err != nil {
		return err
	}
	db := loaded.clone()
	db.ShimPath = shimPath
	db.RealPath = realPath
	return s.saveDBLocked(db)
}

func (s *Store) HasAny() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.loadDBLocked()
	if err != nil {
		return false, err
	}
	return len(db.Proxies) > 0 || db.ShimPath != "", nil
}
