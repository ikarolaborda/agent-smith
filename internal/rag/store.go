package rag

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

/* CollectionVersion is the schema version persisted in each collection file. */
const CollectionVersion = 2

/* CollectionKindDocs identifies a static documentation corpus. */
const CollectionKindDocs = "docs"

/*
CollectionKindMemory identifies a writable per-profile memory store. Memory
collections have a different lifecycle: chunks are appended at runtime,
filtered by Subject at retrieval, and rendered in a quoted untrusted-user
section of the system prompt.
*/
const CollectionKindMemory = "memory"

/*
Collection is a named set of embedded chunks. EmbedderID + Dim are invariants
that gate ingest and search: a collection ingested by one embedder cannot be
extended or searched by another with a different identity. Kind distinguishes
"docs" (read-only at chat time) from "memory" (writable). Legacy collection
files without Kind load as "docs".
*/
type Collection struct {
	Version    int       `json:"version"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind,omitempty"`
	EmbedderID string    `json:"embedder_id"`
	Dim        int       `json:"dim"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Chunks     []Chunk   `json:"chunks"`
}

/*
Store persists Collections to a directory as one JSON file per collection
(<dir>/<name>.json). It is safe for concurrent reads and serializes writes
through an RWMutex.
*/
type Store struct {
	mu  sync.RWMutex
	dir string
}

/* NewStore returns a Store rooted at dir, creating the directory if missing. */
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("rag: mkdir %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

/* Path returns the on-disk path for the named collection. */
func (s *Store) Path(name string) string {
	return filepath.Join(s.dir, name+".json")
}

/* List returns the names of all collections currently on disk. */
func (s *Store) List() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		out = append(out, name[:len(name)-len(".json")])
	}
	sort.Strings(out)
	return out, nil
}

/* Load reads a single collection from disk; returns nil, fs.ErrNotExist if absent. */
func (s *Store) Load(name string) (*Collection, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	raw, err := os.ReadFile(s.Path(name))
	if err != nil {
		return nil, err
	}
	var c Collection
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("rag: decode %s: %w", name, err)
	}
	if c.Kind == "" {
		c.Kind = CollectionKindDocs
	}
	return &c, nil
}

/*
Save atomically persists the collection: it writes to a temp file in the
same directory, then renames over the destination. UpdatedAt is set to now.
*/
func (s *Store) Save(c *Collection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c == nil {
		return errors.New("rag: nil collection")
	}
	if c.Version == 0 {
		c.Version = CollectionVersion
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	c.UpdatedAt = time.Now().UTC()

	buf, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("rag: marshal: %w", err)
	}
	dst := s.Path(c.Name)
	tmp, err := os.CreateTemp(s.dir, c.Name+".*.json.tmp")
	if err != nil {
		return fmt.Errorf("rag: temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(buf); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rag: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rag: close: %w", err)
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rag: rename: %w", err)
	}
	return nil
}

/*
LoadAll reads every collection in the directory into memory. Returns an
empty slice (not nil) when the directory exists but is empty.
*/
func (s *Store) LoadAll() ([]*Collection, error) {
	names, err := s.List()
	if err != nil {
		return nil, err
	}
	out := make([]*Collection, 0, len(names))
	for _, name := range names {
		c, err := s.Load(name)
		if err != nil {
			return nil, fmt.Errorf("rag: load %s: %w", name, err)
		}
		out = append(out, c)
	}
	return out, nil
}
