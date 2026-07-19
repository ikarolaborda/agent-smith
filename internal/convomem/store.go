/*
Package convomem is a prototype persistent conversation memory: it stores every
turn (role + text + embedding) in a pure-Go SQLite database and recalls the most
semantically-relevant past turns for a profile on demand. It lets a conversation
grow unbounded while each prompt stays small — the agent injects only the recalled
slice instead of the whole transcript.

sqlite-vec (a C extension) cannot load into modernc.org/sqlite (the app's no-cgo
SQLite), so similarity is computed in Go: candidate embeddings for a profile are
loaded and ranked by cosine. Brute force is sub-millisecond at conversation scale;
a true ANN index (sqlite-vec via cgo, or ncruces+wasm) is a later, separate step.
*/
package convomem

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

/* Turn is one stored conversation message. Score is set only by Recall. */
type Turn struct {
	ID        int64
	Profile   string
	Role      string
	Content   string
	CreatedAt int64
	Score     float64
}

/*
Store persists turns and their embeddings in SQLite and ranks recall candidates by
cosine similarity in Go. dim is the embedding dimension the store was opened with;
rows of a different dimension are ignored on recall so a mid-life embedder swap
fails safe rather than returning garbage neighbors.
*/
type Store struct {
	db  *sql.DB
	dim int
}

const schema = `
CREATE TABLE IF NOT EXISTS turns (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  profile    TEXT    NOT NULL,
  role       TEXT    NOT NULL,
  content    TEXT    NOT NULL,
  created_at INTEGER NOT NULL,
  dim        INTEGER NOT NULL,
  embedding  BLOB    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_turns_profile ON turns(profile);
`

/*
Open opens (creating if needed) the SQLite database at path and ensures the
schema. path may be a file or ":memory:". dim must match the embedder that will
supply vectors.
*/
func Open(path string, dim int) (*Store, error) {
	if dim <= 0 {
		return nil, fmt.Errorf("convomem: dim must be positive, got %d", dim)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("convomem: open %q: %w", path, err)
	}
	/* WAL keeps writes from blocking the concurrent reads a chat server does. */
	if _, err := db.Exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("convomem: pragma: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("convomem: schema: %w", err)
	}
	return &Store{db: db, dim: dim}, nil
}

/* Close releases the database handle. */
func (s *Store) Close() error { return s.db.Close() }

/*
Add stores one turn with its embedding. A zero-length or wrong-dimension embedding
is rejected so a bad vector never poisons recall. profile scopes the memory to one
user; empty profile is a valid shared bucket.
*/
func (s *Store) Add(ctx context.Context, profile, role, content string, embedding []float32) (int64, error) {
	if len(embedding) != s.dim {
		return 0, fmt.Errorf("convomem: embedding dim %d != store dim %d", len(embedding), s.dim)
	}
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO turns(profile, role, content, created_at, dim, embedding) VALUES(?,?,?,?,?,?)",
		profile, role, content, time.Now().Unix(), s.dim, encodeVec(embedding),
	)
	if err != nil {
		return 0, fmt.Errorf("convomem: insert: %w", err)
	}
	return res.LastInsertId()
}

/*
Recall returns the k turns for profile most similar to query by cosine, highest
first. It loads the profile's candidate rows and ranks them in Go. k<=0 defaults
to 5. Only rows matching the store's dimension are considered.
*/
func (s *Store) Recall(ctx context.Context, profile string, query []float32, k int) ([]Turn, error) {
	if len(query) != s.dim {
		return nil, fmt.Errorf("convomem: query dim %d != store dim %d", len(query), s.dim)
	}
	if k <= 0 {
		k = 5
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, role, content, created_at, embedding FROM turns WHERE profile = ? AND dim = ?",
		profile, s.dim,
	)
	if err != nil {
		return nil, fmt.Errorf("convomem: query: %w", err)
	}
	defer rows.Close()

	qNorm := norm(query)
	var scored []Turn
	for rows.Next() {
		var t Turn
		var blob []byte
		if err := rows.Scan(&t.ID, &t.Role, &t.Content, &t.CreatedAt, &blob); err != nil {
			return nil, fmt.Errorf("convomem: scan: %w", err)
		}
		vec := decodeVec(blob)
		if len(vec) != s.dim {
			continue
		}
		t.Profile = profile
		t.Score = cosine(query, qNorm, vec)
		scored = append(scored, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].ID > scored[j].ID // ties: prefer the more recent turn
	})
	if len(scored) > k {
		scored = scored[:k]
	}
	return scored, nil
}

/* encodeVec serializes a float32 slice as little-endian bytes for BLOB storage. */
func encodeVec(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

/* decodeVec is the inverse of encodeVec; a malformed length yields an empty slice. */
func decodeVec(b []byte) []float32 {
	if len(b)%4 != 0 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

func norm(v []float32) float64 {
	var s float64
	for _, f := range v {
		s += float64(f) * float64(f)
	}
	return math.Sqrt(s)
}

/* cosine returns the cosine similarity of a (with precomputed norm aNorm) and b. */
func cosine(a []float32, aNorm float64, b []float32) float64 {
	if aNorm == 0 {
		return 0
	}
	bNorm := norm(b)
	if bNorm == 0 {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot / (aNorm * bNorm)
}
