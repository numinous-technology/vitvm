package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The image cache holds reassembled machine images under .vit/cache, keyed by
// their hash, so resuming a step a second time, or forking it many times,
// reassembles it once. Backends map the memory file copy-on-write and copy the
// disk, so cached files are never modified. The cache is bounded (VIT_CACHE_GIB,
// default 16); the least recently used images go first.

func (e *Engine) cacheDir() string { return filepath.Join(e.repo.root, "cache") }

func cacheLimit() int64 {
	if g, err := strconv.Atoi(os.Getenv("VIT_CACHE_GIB")); err == nil && g > 0 {
		return int64(g) << 30
	}
	return 16 << 30
}

// cachedImage returns a path to the materialised object hash: a chunked image
// (memory, disk) or a plain blob (state).
func (e *Engine) cachedImage(hash, ext string) (string, error) {
	dir := e.cacheDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, hash+ext)
	if _, err := os.Stat(p); err == nil {
		now := time.Now()
		os.Chtimes(p, now, now)
		return p, nil
	}
	var err error
	if _, merr := e.repo.CAS().ReadManifest(hash); merr == nil {
		err = e.repo.CAS().GetChunkedTo(hash, p)
	} else {
		err = e.writeBlobTo(hash, p)
	}
	if err != nil {
		return "", err
	}
	e.evictCache(p)
	return p, nil
}

// evictCache removes the least recently used images until the cache fits,
// never the one just made.
func (e *Engine) evictCache(keep string) {
	ents, err := os.ReadDir(e.cacheDir())
	if err != nil {
		return
	}
	type item struct {
		path  string
		used  int64 // bytes on disk
		mtime time.Time
	}
	var items []item
	var total int64
	for _, en := range ents {
		fi, err := en.Info()
		if err != nil || fi.IsDir() {
			continue
		}
		p := filepath.Join(e.cacheDir(), en.Name())
		items = append(items, item{p, diskUsage(p, fi), fi.ModTime()})
		total += diskUsage(p, fi)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mtime.Before(items[j].mtime) })
	for _, it := range items {
		if total <= cacheLimit() {
			return
		}
		if it.path == keep {
			continue
		}
		os.Remove(it.path)
		total -= it.used
	}
}

// baseHash stores a read-only base disk once and returns its manifest hash.
// A base that is already a cached image is named by its file name; any other
// file is identified by device, inode, size and modification time, and hashed
// only the first time it is seen.
func (e *Engine) baseHash(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if filepath.Dir(real) == e.cacheDir() && filepath.Ext(real) == ".base" {
		return strings.TrimSuffix(filepath.Base(real), ".base"), nil
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	id := fileIdentity(real, fi)
	known := map[string]string{}
	idx := filepath.Join(e.repo.root, "bases.json")
	if b, err := os.ReadFile(idx); err == nil {
		json.Unmarshal(b, &known)
	}
	if h, ok := known[id]; ok && e.repo.CAS().Has(h) {
		return h, nil
	}
	h, _, err := e.repo.CAS().PutChunked(real)
	if err != nil {
		return "", err
	}
	known[id] = h
	b, _ := json.MarshalIndent(known, "", "  ")
	return h, os.WriteFile(idx, b, 0o644)
}
