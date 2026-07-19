package store

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"os"
	"strings"
)

const (
	artifactEncryptionScheme = "aes-256-gcm-chunked-v1"
	artifactChunkSize        = 64 << 10
	artifactHeaderSize       = 8 + sha256.Size + 12
	artifactRecordHeaderSize = 5
)

var artifactMagic = [8]byte{'A', 'S', 'A', 'R', 'T', '0', '1', '\n'}

func prepareArtifactKeys(values [][]byte) (map[string][]byte, string, error) {
	if len(values) == 0 {
		return nil, "", nil
	}
	if len(values) > 16 {
		return nil, "", errors.New("research store: artifact keyring exceeds 16 keys")
	}
	keys := make(map[string][]byte, len(values))
	active := ""
	for index, value := range values {
		if len(value) != 32 {
			return nil, "", errors.New("research store: artifact encryption keys must contain 32 bytes")
		}
		digest := sha256.Sum256(value)
		keyID := "sha256:" + hex.EncodeToString(digest[:])
		if _, duplicate := keys[keyID]; duplicate {
			return nil, "", errors.New("research store: duplicate artifact encryption key")
		}
		keys[keyID] = append([]byte(nil), value...)
		if index == 0 {
			active = keyID
		}
	}
	return keys, active, nil
}

func encryptArtifact(destination io.Writer, source io.Reader, limit int64, key []byte, keyID string) (int64, string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return 0, "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return 0, "", err
	}
	keyDigest, err := decodeArtifactKeyID(keyID)
	if err != nil {
		return 0, "", err
	}
	header := make([]byte, artifactHeaderSize)
	copy(header, artifactMagic[:])
	copy(header[len(artifactMagic):], keyDigest)
	baseNonce := header[len(artifactMagic)+sha256.Size:]
	if _, err := rand.Read(baseNonce); err != nil {
		return 0, "", err
	}
	if _, err := destination.Write(header); err != nil {
		return 0, "", err
	}
	reader := bufio.NewReaderSize(io.LimitReader(source, limit+1), artifactChunkSize+1)
	digest := sha256.New()
	var total int64
	var index uint32
	for {
		peeked, peekErr := reader.Peek(artifactChunkSize + 1)
		if peekErr != nil && !errors.Is(peekErr, io.EOF) && !errors.Is(peekErr, bufio.ErrBufferFull) {
			return total, "", fmt.Errorf("research store: read artifact plaintext: %w", peekErr)
		}
		final := errors.Is(peekErr, io.EOF) && len(peeked) <= artifactChunkSize
		length := len(peeked)
		if length > artifactChunkSize {
			length = artifactChunkSize
		}
		if total+int64(length) > limit {
			return total + int64(length), "", ErrArtifactTooLarge
		}
		plaintext := append([]byte(nil), peeked[:length]...)
		if _, err := reader.Discard(length); err != nil {
			return total, "", err
		}
		recordHeader := make([]byte, artifactRecordHeaderSize)
		binary.BigEndian.PutUint32(recordHeader[:4], uint32(length))
		if final {
			recordHeader[4] = 1
		}
		nonce := artifactNonce(baseNonce, index)
		aad := artifactAAD(header, index, recordHeader)
		ciphertext := aead.Seal(nil, nonce, plaintext, aad)
		if _, err := destination.Write(recordHeader); err != nil {
			return total, "", err
		}
		if _, err := destination.Write(ciphertext); err != nil {
			return total, "", err
		}
		_, _ = digest.Write(plaintext)
		total += int64(length)
		if final {
			return total, "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
		}
		if index == math.MaxUint32 {
			return total, "", errors.New("research store: artifact contains too many encryption chunks")
		}
		index++
	}
}

type encryptedArtifactReader struct {
	file          *os.File
	aead          cipher.AEAD
	header        []byte
	baseNonce     []byte
	expectedSize  int64
	expectedID    string
	digest        hash.Hash
	index         uint32
	total         int64
	buffer        []byte
	finalVerified bool
	terminalErr   error
}

func newEncryptedArtifactReader(file *os.File, expectedSize int64, expectedID string, keys map[string][]byte) (*encryptedArtifactReader, string, error) {
	header := make([]byte, artifactHeaderSize)
	if _, err := io.ReadFull(file, header); err != nil || !bytes.Equal(header[:len(artifactMagic)], artifactMagic[:]) {
		return nil, "", errors.New("research store: malformed encrypted artifact header")
	}
	keyID := "sha256:" + hex.EncodeToString(header[len(artifactMagic):len(artifactMagic)+sha256.Size])
	key, ok := keys[keyID]
	if !ok {
		return nil, keyID, errors.New("research store: artifact encryption key unavailable")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, keyID, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, keyID, err
	}
	stat, err := file.Stat()
	if err != nil || !stat.Mode().IsRegular() {
		return nil, keyID, errors.New("research store: encrypted artifact is not a regular file")
	}
	expectedDiskSize, err := encryptedArtifactSize(expectedSize, aead.Overhead())
	if err != nil || stat.Size() != expectedDiskSize {
		return nil, keyID, errors.New("research store: encrypted artifact size mismatch")
	}
	return &encryptedArtifactReader{file: file, aead: aead, header: header, baseNonce: header[len(artifactMagic)+sha256.Size:], expectedSize: expectedSize, expectedID: expectedID, digest: sha256.New()}, keyID, nil
}

func (reader *encryptedArtifactReader) Read(destination []byte) (int, error) {
	if len(destination) == 0 {
		return 0, nil
	}
	for len(reader.buffer) == 0 {
		if reader.terminalErr != nil {
			return 0, reader.terminalErr
		}
		if reader.finalVerified {
			return 0, io.EOF
		}
		if err := reader.readChunk(); err != nil {
			reader.terminalErr = err
			return 0, err
		}
	}
	count := copy(destination, reader.buffer)
	reader.buffer = reader.buffer[count:]
	return count, nil
}

func (reader *encryptedArtifactReader) readChunk() error {
	var recordHeader [artifactRecordHeaderSize]byte
	if _, err := io.ReadFull(reader.file, recordHeader[:]); err != nil {
		return errors.New("research store: truncated encrypted artifact")
	}
	length := int64(binary.BigEndian.Uint32(recordHeader[:4]))
	final := recordHeader[4] == 1
	if recordHeader[4] > 1 || length > artifactChunkSize || (!final && length != artifactChunkSize) || reader.total+length > reader.expectedSize {
		return errors.New("research store: invalid encrypted artifact record")
	}
	ciphertext := make([]byte, int(length)+reader.aead.Overhead())
	if _, err := io.ReadFull(reader.file, ciphertext); err != nil {
		return errors.New("research store: truncated encrypted artifact record")
	}
	nonce := artifactNonce(reader.baseNonce, reader.index)
	plaintext, err := reader.aead.Open(nil, nonce, ciphertext, artifactAAD(reader.header, reader.index, recordHeader[:]))
	if err != nil {
		return errors.New("research store: artifact authentication failed")
	}
	reader.total += int64(len(plaintext))
	_, _ = reader.digest.Write(plaintext)
	if final {
		if reader.total != reader.expectedSize {
			return errors.New("research store: encrypted artifact plaintext size mismatch")
		}
		actual := "sha256:" + hex.EncodeToString(reader.digest.Sum(nil))
		if actual != reader.expectedID {
			return errors.New("research store: artifact content hash mismatch")
		}
		reader.finalVerified = true
	} else {
		if reader.total >= reader.expectedSize || reader.index == math.MaxUint32 {
			return errors.New("research store: invalid encrypted artifact final record")
		}
		reader.index++
	}
	reader.buffer = plaintext
	return nil
}

func (reader *encryptedArtifactReader) Close() error { return reader.file.Close() }

func encryptedArtifactSize(plaintextSize int64, overhead int) (int64, error) {
	if plaintextSize < 0 {
		return 0, errors.New("research store: negative artifact size")
	}
	chunks := int64(1)
	if plaintextSize > 0 {
		chunks = plaintextSize / artifactChunkSize
		if plaintextSize%artifactChunkSize != 0 {
			chunks++
		}
	}
	perChunk := int64(artifactRecordHeaderSize + overhead)
	if chunks > (math.MaxInt64-int64(artifactHeaderSize)-plaintextSize)/perChunk {
		return 0, errors.New("research store: encrypted artifact size overflow")
	}
	return int64(artifactHeaderSize) + plaintextSize + chunks*perChunk, nil
}

func artifactNonce(base []byte, index uint32) []byte {
	nonce := append([]byte(nil), base...)
	randomSuffix := binary.BigEndian.Uint32(nonce[len(nonce)-4:])
	binary.BigEndian.PutUint32(nonce[len(nonce)-4:], randomSuffix^index)
	return nonce
}

func artifactAAD(header []byte, index uint32, recordHeader []byte) []byte {
	aad := make([]byte, 0, len(header)+4+len(recordHeader))
	aad = append(aad, header...)
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], index)
	aad = append(aad, encoded[:]...)
	aad = append(aad, recordHeader...)
	return aad
}

func decodeArtifactKeyID(keyID string) ([]byte, error) {
	if !strings.HasPrefix(keyID, "sha256:") {
		return nil, errors.New("research store: invalid artifact key identity")
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(keyID, "sha256:"))
	if err != nil || len(decoded) != sha256.Size {
		return nil, errors.New("research store: invalid artifact key identity")
	}
	return decoded, nil
}
