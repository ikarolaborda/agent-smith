/*
gguf.go is a minimal, read-only GGUF metadata reader used to discover a model's
native (trained) context length so the tuner can cap its dynamic context ladder.
It deliberately parses only what that needs: the header, then the key/value
section until "general.architecture" and "<arch>.context_length" are known.

The value is a CEILING HINT, never a gate: every failure path returns 0
("unknown"), which callers treat as "keep the conservative default ceiling".
The fit gate remains the final admission authority regardless of what this
reader reports. Layout follows the GGUF spec (ggml-org/ggml docs/gguf.md):
little-endian, magic "GGUF", version 2/3, u64 tensor and kv counts, then
length-prefixed keys with a typed value each.
*/
package llamacpp

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

const (
	ggufMagic = "GGUF"

	ggufTypeUint8   = 0
	ggufTypeInt8    = 1
	ggufTypeUint16  = 2
	ggufTypeInt16   = 3
	ggufTypeUint32  = 4
	ggufTypeInt32   = 5
	ggufTypeFloat32 = 6
	ggufTypeBool    = 7
	ggufTypeString  = 8
	ggufTypeArray   = 9
	ggufTypeUint64  = 10
	ggufTypeInt64   = 11
	ggufTypeFloat64 = 12
)

/*
Sanity bounds so a corrupt or hostile file cannot make the reader allocate or
loop unboundedly: real models carry a few dozen KV pairs and keys under a
hundred bytes; tokenizer strings can be long but never near these caps.
*/
const (
	ggufMaxKVCount    = 1 << 16
	ggufMaxStringLen  = 64 << 20
	ggufMaxArrayLen   = 1 << 26
	ggufMaxArrayDepth = 4
)

type ggufReader struct {
	r *bufio.Reader
}

func (g ggufReader) u32() (uint32, error) {
	var buf [4]byte
	if _, err := io.ReadFull(g.r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(buf[:]), nil
}

func (g ggufReader) u64() (uint64, error) {
	var buf [8]byte
	if _, err := io.ReadFull(g.r, buf[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(buf[:]), nil
}

func (g ggufReader) str() (string, error) {
	n, err := g.u64()
	if err != nil {
		return "", err
	}
	if n > ggufMaxStringLen {
		return "", fmt.Errorf("llamacpp: gguf string length %d exceeds sanity bound", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(g.r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func (g ggufReader) skip(n uint64) error {
	_, err := io.CopyN(io.Discard, g.r, int64(n))
	return err
}

/*
uintValue reads a value of the given type and reports it as a uint64 when the
type is an unsigned or signed non-negative integer; ok=false means the value
was consumed but is not usable as a count (float, bool, string, array).
*/
func (g ggufReader) uintValue(typ uint32) (uint64, bool, error) {
	switch typ {
	case ggufTypeUint8, ggufTypeInt8, ggufTypeBool:
		var b [1]byte
		if _, err := io.ReadFull(g.r, b[:]); err != nil {
			return 0, false, err
		}
		if typ == ggufTypeUint8 {
			return uint64(b[0]), true, nil
		}
		return 0, false, nil
	case ggufTypeUint16, ggufTypeInt16:
		var b [2]byte
		if _, err := io.ReadFull(g.r, b[:]); err != nil {
			return 0, false, err
		}
		if typ == ggufTypeUint16 {
			return uint64(binary.LittleEndian.Uint16(b[:])), true, nil
		}
		return 0, false, nil
	case ggufTypeUint32, ggufTypeInt32, ggufTypeFloat32:
		v, err := g.u32()
		if err != nil {
			return 0, false, err
		}
		if typ == ggufTypeUint32 {
			return uint64(v), true, nil
		}
		return 0, false, nil
	case ggufTypeUint64, ggufTypeInt64, ggufTypeFloat64:
		v, err := g.u64()
		if err != nil {
			return 0, false, err
		}
		if typ == ggufTypeUint64 {
			return v, true, nil
		}
		return 0, false, nil
	case ggufTypeString:
		_, err := g.str()
		return 0, false, err
	case ggufTypeArray:
		return 0, false, g.skipArray(0)
	default:
		return 0, false, fmt.Errorf("llamacpp: unknown gguf value type %d", typ)
	}
}

func (g ggufReader) skipArray(depth int) error {
	if depth >= ggufMaxArrayDepth {
		return errors.New("llamacpp: gguf array nesting exceeds sanity bound")
	}
	elemType, err := g.u32()
	if err != nil {
		return err
	}
	count, err := g.u64()
	if err != nil {
		return err
	}
	if count > ggufMaxArrayLen {
		return fmt.Errorf("llamacpp: gguf array length %d exceeds sanity bound", count)
	}
	switch elemType {
	case ggufTypeUint8, ggufTypeInt8, ggufTypeBool:
		return g.skip(count)
	case ggufTypeUint16, ggufTypeInt16:
		return g.skip(count * 2)
	case ggufTypeUint32, ggufTypeInt32, ggufTypeFloat32:
		return g.skip(count * 4)
	case ggufTypeUint64, ggufTypeInt64, ggufTypeFloat64:
		return g.skip(count * 8)
	case ggufTypeString:
		for i := uint64(0); i < count; i++ {
			if _, err := g.str(); err != nil {
				return err
			}
		}
		return nil
	case ggufTypeArray:
		for i := uint64(0); i < count; i++ {
			if err := g.skipArray(depth + 1); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("llamacpp: unknown gguf array element type %d", elemType)
	}
}

/*
ReadGGUFContextLength reports the model's native context length recorded in a
local GGUF file, or 0 when it cannot be determined. It scans the KV section for
"general.architecture" and the matching "<arch>.context_length", returning as
soon as both are known so the large tokenizer arrays that follow are never
touched. When the architecture key is absent it falls back to a sole
"*.context_length" entry, and refuses to guess if several disagree.
*/
func ReadGGUFContextLength(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	g := ggufReader{r: bufio.NewReaderSize(f, 1<<20)}

	var magic [4]byte
	if _, err := io.ReadFull(g.r, magic[:]); err != nil {
		return 0, fmt.Errorf("llamacpp: read gguf magic: %w", err)
	}
	if string(magic[:]) != ggufMagic {
		return 0, fmt.Errorf("llamacpp: %q is not a GGUF file", path)
	}
	version, err := g.u32()
	if err != nil {
		return 0, err
	}
	if version < 2 || version > 3 {
		return 0, fmt.Errorf("llamacpp: unsupported gguf version %d", version)
	}
	if _, err := g.u64(); err != nil { /* tensor count, unused */
		return 0, err
	}
	kvCount, err := g.u64()
	if err != nil {
		return 0, err
	}
	if kvCount > ggufMaxKVCount {
		return 0, fmt.Errorf("llamacpp: gguf kv count %d exceeds sanity bound", kvCount)
	}

	arch := ""
	ctxByPrefix := map[string]uint64{}
	for i := uint64(0); i < kvCount; i++ {
		key, err := g.str()
		if err != nil {
			return 0, err
		}
		typ, err := g.u32()
		if err != nil {
			return 0, err
		}
		switch {
		case key == "general.architecture" && typ == ggufTypeString:
			if arch, err = g.str(); err != nil {
				return 0, err
			}
		case strings.HasSuffix(key, ".context_length"):
			v, ok, err := g.uintValue(typ)
			if err != nil {
				return 0, err
			}
			if ok {
				ctxByPrefix[strings.TrimSuffix(key, ".context_length")] = v
			}
		default:
			if _, _, err := g.uintValue(typ); err != nil {
				return 0, err
			}
		}
		if arch != "" {
			if v, ok := ctxByPrefix[arch]; ok {
				return clampCtx(v), nil
			}
		}
	}
	if arch != "" {
		return 0, nil /* architecture known but carries no context_length */
	}
	if len(ctxByPrefix) == 1 {
		for _, v := range ctxByPrefix {
			return clampCtx(v), nil
		}
	}
	return 0, nil
}

func clampCtx(v uint64) int {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	return int(v)
}

/*
NativeContextFromPlan resolves the native context length for the model a plan
describes. The committed local file wins over the remote hint whenever it is
already present (every run after the first download), because the file being
served is the ground truth and remote metadata can describe a different or
re-uploaded conversion. Falls back to the manifest's Hugging Face hint, then 0.
*/
func NativeContextFromPlan(plan DownloadPlan) int {
	if plan.CacheDir != "" && len(plan.Manifest.ModelArtifacts) > 0 {
		path := filepath.Join(plan.CacheDir, plan.Manifest.ModelArtifacts[0].Filename)
		if n, err := ReadGGUFContextLength(path); err == nil && n > 0 {
			return n
		}
	}
	return plan.Manifest.ContextLength
}
