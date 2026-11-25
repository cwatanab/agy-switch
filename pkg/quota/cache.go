package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ZhangNy301/agy-switch/pkg/filelock"
	"github.com/ZhangNy301/agy-switch/pkg/util"
)

func (c *Client) cacheFilePath() string { return filepath.Join(c.paths.DataDir, "quota_cache.json") }

func (c *Client) loadCached(email string) (*QuotaInfo, bool) { return c.readCached(email, false) }

// Cached returns a stored quota, including stale values, without network I/O.
func (c *Client) Cached(email string) (*QuotaInfo, bool) { return c.readCached(email, true) }

// CachedAll loads one consistent snapshot for an account picker. The returned map
// and its values belong to the caller, so background updates cannot mutate it.
func (c *Client) CachedAll() map[string]QuotaInfo {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if err := c.reloadCache(); err != nil {
		return nil
	}
	snapshot := make(map[string]QuotaInfo, len(c.cache))
	for email, info := range c.cache {
		snapshot[email] = info
	}
	return snapshot
}

// CachedBatch loads cached quotas for the given emails in one lock acquisition
// without cloning the full cache map.
func (c *Client) CachedBatch(emails []string) ([]QuotaInfo, []bool) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if err := c.reloadCache(); err != nil || len(emails) == 0 {
		return nil, nil
	}
	infos := make([]QuotaInfo, len(emails))
	found := make([]bool, len(emails))
	for i, email := range emails {
		if info, ok := c.cache[email]; ok {
			infos[i] = info
			found[i] = true
		}
	}
	return infos, found
}

func (c *Client) readCached(email string, allowStale bool) (*QuotaInfo, bool) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if err := c.reloadCache(); err != nil {
		return nil, false
	}
	info, ok := c.cache[email]
	if !ok || (!allowStale && time.Since(info.UpdatedAt) > CacheTTL) {
		return nil, false
	}
	return &info, true
}

func (c *Client) reloadCache() error {
	if c.paths == nil || c.paths.DataDir == "" {
		return nil
	}
	info, err := os.Stat(c.cacheFilePath())
	if os.IsNotExist(err) {
		c.cache, c.cacheInfo = nil, nil
		return nil
	}
	if err != nil {
		return err
	}
	if c.cacheInfo != nil && os.SameFile(info, c.cacheInfo) && info.Size() == c.cacheInfo.Size() && info.ModTime() == c.cacheInfo.ModTime() {
		return nil
	}
	cache, err := c.readDiskCache()
	if err != nil {
		return err
	}
	c.cache, c.cacheInfo = cache, info
	return nil
}

func (c *Client) readDiskCache() (map[string]QuotaInfo, error) {
	cache := make(map[string]QuotaInfo)
	data, err := os.ReadFile(c.cacheFilePath())
	if os.IsNotExist(err) {
		return cache, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("decode quota cache: %w", err)
	}
	if cache == nil {
		cache = make(map[string]QuotaInfo)
	}
	return cache, nil
}

// ClearCached removes one account while preserving concurrent updates by other processes.
func (c *Client) ClearCached(email string) error {
	if email == "" {
		return nil
	}
	return c.updateCache(context.Background(), func(cache map[string]QuotaInfo) { delete(cache, email) })
}

func (c *Client) saveCached(ctx context.Context, email string, info *QuotaInfo) error {
	return c.updateCache(ctx, func(cache map[string]QuotaInfo) { cache[email] = *info })
}

func (c *Client) updateCache(ctx context.Context, update func(map[string]QuotaInfo)) (err error) {
	if c.paths == nil || c.paths.DataDir == "" {
		return nil
	}
	release, err := filelock.Acquire(ctx, filepath.Join(c.paths.DataDir, "quota-cache.lock"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	// Read again while holding the process lock; another process may have saved
	// additional accounts since this client's last snapshot.
	cache, err := c.readDiskCache()
	if err != nil {
		return err
	}
	update(cache)
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	if err := util.WriteFileAtomic(c.cacheFilePath(), data, 0600); err != nil {
		return err
	}
	info, err := os.Stat(c.cacheFilePath())
	if err != nil {
		return err
	}
	c.cache, c.cacheInfo = cache, info
	return nil
}
