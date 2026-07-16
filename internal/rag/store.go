package rag

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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

var collectionNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

/*
validateCollectionName keeps collection identifiers as single safe filename
components. Dots are useful in human-readable slugs, but a double-dot sequence
is rejected so a name can never acquire traversal semantics on any platform.
*/
func validateCollectionName(name string) error {
	if !collectionNameRE.MatchString(name) || strings.Contains(name, "..") {
		return fmt.Errorf("rag: invalid collection name %q; use 1-128 ASCII letters, digits, '.', '_', or '-' without '..'", name)
	}
	return nil
}

/* NewStore returns a Store rooted at dir, creating the directory if missing. */
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("rag: mkdir %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

/* Path validates name and returns its on-disk collection path. */
func (s *Store) Path(name string) (string, error) {
	if err := validateCollectionName(name); err != nil {
		return "", err
	}
	return filepath.Join(s.dir, name+".json"), nil
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
	path, err := s.Path(name)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Collection
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("rag: decode %s: %w", name, err)
	}
	if c.Name == "" {
		/* Older hand-authored collections may omit name; bind them to the safe filename. */
		c.Name = name
	} else if c.Name != name {
		return nil, fmt.Errorf("rag: collection file %q declares mismatched name %q", name, c.Name)
	}
	if err := validateCollectionName(c.Name); err != nil {
		return nil, err
	}
	if c.Kind == "" {
		if c.Name == MemoryCollectionName || name == MemoryCollectionName {
			c.Kind = CollectionKindMemory
		} else {
			c.Kind = CollectionKindDocs
		}
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
	if err := validateCollectionName(c.Name); err != nil {
		return err
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
	dst, err := s.Path(c.Name)
	if err != nil {
		return err
	}
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
