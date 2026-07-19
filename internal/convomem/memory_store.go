package convomem

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

/*
MemoryRecord is one durable per-profile memory item. It mirrors the fields the RAG
memory layer needs (kind, importance, pinned, timestamps, subject) without
importing that package, so MemoryStore stays a generic persistence seam. Subject
is the owning profile; retrieval is always subject-scoped.
*/
type MemoryRecord struct {
	ID           string
	Subject      string
	Kind         string
	Text         string
	Vector       []float32
	Importance   float32
	Pinned       bool
	CreatedAt    string
	LastAccessed string
}

/*
MemoryStore persists per-profile memory in SQLite and returns a profile's
candidates for in-Go scoring. Unlike the JSON-file collection it replaces, a write
is one INSERT and a read touches only one profile's rows — so memory scales without
loading every profile into RAM. A meta table pins the embedder identity/dim so a
mid-life embedder swap fails closed rather than mixing vector spaces.
*/
type MemoryStore struct {
	db *sql.DB
}

const memorySchema = `
CREATE TABLE IF NOT EXISTS memory (
  id            TEXT PRIMARY KEY,
  subject       TEXT    NOT NULL,
  kind          TEXT    NOT NULL,
  text          TEXT    NOT NULL,
  importance    REAL    NOT NULL,
  pinned        INTEGER NOT NULL,
  created_at    TEXT    NOT NULL,
  last_accessed TEXT    NOT NULL,
  dim           INTEGER NOT NULL,
  embedding     BLOB    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memory_subject ON memory(subject);
CREATE TABLE IF NOT EXISTS memory_meta (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
);
`

/* OpenMemoryStore opens (creating if needed) the memory database at path. */
func OpenMemoryStore(path string) (*MemoryStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("convomem: open memory %q: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("convomem: memory pragma: %w", err)
	}
	if _, err := db.Exec(memorySchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("convomem: memory schema: %w", err)
	}
	return &MemoryStore{db: db}, nil
}

/* Close releases the database handle. */
func (m *MemoryStore) Close() error { return m.db.Close() }

/*
Embedder returns the pinned embedder identity and dimension, ok=false when none is
recorded yet (an empty store). Callers enforce that a new write matches this.
*/
func (m *MemoryStore) Embedder(ctx context.Context) (id string, dim int, ok bool, err error) {
	rows, err := m.db.QueryContext(ctx, "SELECT k, v FROM memory_meta WHERE k IN ('embedder_id','dim')")
	if err != nil {
		return "", 0, false, err
	}
	defer rows.Close()
	found := 0
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return "", 0, false, err
		}
		switch k {
		case "embedder_id":
			id = v
			found++
		case "dim":
			if _, err := fmt.Sscanf(v, "%d", &dim); err != nil {
				return "", 0, false, err
			}
			found++
		}
	}
	if err := rows.Err(); err != nil {
		return "", 0, false, err
	}
	return id, dim, found == 2, nil
}

/*
SetEmbedder pins the embedder identity/dim on first use and thereafter enforces
agreement, so private memory is never embedded by a different backend than the one
that wrote the existing vectors.
*/
func (m *MemoryStore) SetEmbedder(ctx context.Context, id string, dim int) error {
	if id == "" || dim <= 0 {
		return fmt.Errorf("convomem: invalid embedder identity %q/%d", id, dim)
	}
	curID, curDim, ok, err := m.Embedder(ctx)
	if err != nil {
		return err
	}
	if ok {
		if curID != id || curDim != dim {
			return fmt.Errorf("convomem: memory embedder %q/%d != requested %q/%d", curID, curDim, id, dim)
		}
		return nil
	}
	_, err = m.db.ExecContext(ctx,
		"INSERT OR REPLACE INTO memory_meta(k,v) VALUES('embedder_id',?),('dim',?)",
		id, fmt.Sprintf("%d", dim),
	)
	return err
}

/* Put inserts or replaces a memory record by ID. */
func (m *MemoryStore) Put(ctx context.Context, r MemoryRecord) error {
	if r.ID == "" || r.Subject == "" {
		return errors.New("convomem: memory record needs ID and Subject")
	}
	if len(r.Vector) == 0 {
		return errors.New("convomem: memory record needs a vector")
	}
	pinned := 0
	if r.Pinned {
		pinned = 1
	}
	_, err := m.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO memory(id, subject, kind, text, importance, pinned, created_at, last_accessed, dim, embedding)
		 VALUES(?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Subject, r.Kind, r.Text, r.Importance, pinned, r.CreatedAt, r.LastAccessed, len(r.Vector), encodeVec(r.Vector),
	)
	if err != nil {
		return fmt.Errorf("convomem: put memory: %w", err)
	}
	return nil
}

/* Delete removes one record scoped to subject, returning whether a row was deleted. */
func (m *MemoryStore) Delete(ctx context.Context, subject, id string) (bool, error) {
	res, err := m.db.ExecContext(ctx, "DELETE FROM memory WHERE id = ? AND subject = ?", id, subject)
	if err != nil {
		return false, fmt.Errorf("convomem: delete memory: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

/*
Candidates returns all of subject's records with vectors, for the caller to score
(cosine + importance + recency). withVectors=false strips embeddings for listing.
*/
func (m *MemoryStore) Candidates(ctx context.Context, subject string, withVectors bool) ([]MemoryRecord, error) {
	cols := "id, subject, kind, text, importance, pinned, created_at, last_accessed, embedding"
	rows, err := m.db.QueryContext(ctx, "SELECT "+cols+" FROM memory WHERE subject = ?", subject)
	if err != nil {
		return nil, fmt.Errorf("convomem: candidates: %w", err)
	}
	defer rows.Close()
	var out []MemoryRecord
	for rows.Next() {
		var r MemoryRecord
		var pinned int
		var blob []byte
		if err := rows.Scan(&r.ID, &r.Subject, &r.Kind, &r.Text, &r.Importance, &pinned, &r.CreatedAt, &r.LastAccessed, &blob); err != nil {
			return nil, err
		}
		r.Pinned = pinned != 0
		if withVectors {
			r.Vector = decodeVec(blob)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

/* Count returns the total number of stored memory rows (used to gate one-time migration). */
func (m *MemoryStore) Count(ctx context.Context) (int, error) {
	var n int
	err := m.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM memory").Scan(&n)
	return n, err
}
