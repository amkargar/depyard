package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// FSBlobStore stores immutable binary blobs on disk addressed by lowercase hex SHA-256 digest.
type FSBlobStore struct {
	rootDir string
	tmpDir  string
	locks   keyLocker
}

func NewFSBlobStore(dataDir string) (*FSBlobStore, error) {
	blobsDir := filepath.Join(dataDir, "blobs", "sha256")
	tmpDir := filepath.Join(dataDir, "tmp")
	if err := os.MkdirAll(blobsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create blobs dir: %w", err)
	}
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, fmt.Errorf("create tmp dir: %w", err)
	}
	return &FSBlobStore{
		rootDir: blobsDir,
		tmpDir:  tmpDir,
	}, nil
}

// WriteResult describes a stored blob after streaming write and digest computation.
type WriteResult struct {
	Digest       string
	SizeBytes    int64
	Deduplicated bool
}

// Write streams from r to a temporary file, computes its SHA-256 digest, verifies expectedDigest
// (if non-empty) and maxBytes (if > 0), fsyncs, and atomically commits the blob to its sharded path.
func (s *FSBlobStore) Write(r io.Reader, expectedDigest string, maxBytes int64) (WriteResult, error) {
	expectedDigest = strings.ToLower(strings.TrimSpace(expectedDigest))
	if expectedDigest != "" && !IsValidSHA256(expectedDigest) {
		return WriteResult{}, ErrInvalidDigest
	}

	tmpFile, err := os.CreateTemp(s.tmpDir, "blob-*.part")
	if err != nil {
		return WriteResult{}, fmt.Errorf("create temp blob: %w", err)
	}
	tmpPath := tmpFile.Name()
	cleanup := true
	defer func() {
		_ = tmpFile.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	hasher := sha256.New()
	var src io.Reader = r
	if maxBytes > 0 {
		src = io.LimitReader(r, maxBytes+1)
	}

	written, err := io.Copy(io.MultiWriter(tmpFile, hasher), src)
	if err != nil {
		return WriteResult{}, fmt.Errorf("stream blob write: %w", err)
	}
	if maxBytes > 0 && written > maxBytes {
		return WriteResult{}, ErrSizeExceeded
	}

	digest := hex.EncodeToString(hasher.Sum(nil))
	if expectedDigest != "" && digest != expectedDigest {
		return WriteResult{}, fmt.Errorf("%w: expected %s, got %s", ErrDigestMismatch, expectedDigest, digest)
	}

	if err := tmpFile.Sync(); err != nil {
		return WriteResult{}, fmt.Errorf("fsync temp blob: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return WriteResult{}, fmt.Errorf("close temp blob: %w", err)
	}

	unlock := s.locks.Lock(digest)
	defer unlock()

	finalPath, err := s.blobPath(digest)
	if err != nil {
		return WriteResult{}, err
	}

	// Content deduplication: if the blob already exists with matching size, discard temp file.
	if info, statErr := os.Stat(finalPath); statErr == nil && info.Size() == written {
		return WriteResult{
			Digest:       digest,
			SizeBytes:    written,
			Deduplicated: true,
		}, nil
	}

	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return WriteResult{}, fmt.Errorf("create shard dir: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return WriteResult{}, fmt.Errorf("atomic commit blob: %w", err)
	}
	cleanup = false

	return WriteResult{
		Digest:       digest,
		SizeBytes:    written,
		Deduplicated: false,
	}, nil
}

// Open returns a streaming read-only file handle for the given SHA-256 digest.
func (s *FSBlobStore) Open(digest string) (*os.File, int64, error) {
	p, err := s.blobPath(digest)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, ErrBlobNotFound
		}
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, info.Size(), nil
}

// Verify streams the stored blob from disk and validates that its content matches its digest.
func (s *FSBlobStore) Verify(digest string) error {
	f, _, err := s.Open(digest)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if actual != strings.ToLower(digest) {
		return fmt.Errorf("%w: expected %s, got %s", ErrDigestMismatch, digest, actual)
	}
	return nil
}

// Remove deletes the physical blob file from disk. Callers MUST verify zero references first under lock.
func (s *FSBlobStore) Remove(digest string) error {
	p, err := s.blobPath(digest)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// LockDigest acquires the per-digest lock to synchronize deletion with concurrent writes.
func (s *FSBlobStore) LockDigest(digest string) func() {
	return s.locks.Lock(strings.ToLower(strings.TrimSpace(digest)))
}

func (s *FSBlobStore) blobPath(digest string) (string, error) {
	d := strings.ToLower(strings.TrimSpace(digest))
	if !IsValidSHA256(d) {
		return "", ErrInvalidDigest
	}
	return filepath.Join(s.rootDir, d[:2], d[2:4], d), nil
}

// IsValidSHA256 returns true if s is a 64-character hex string.
func IsValidSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// keyLocker provides lightweight per-key mutual exclusion without unbounded memory growth.
type keyLocker struct {
	mu    sync.Mutex
	locks map[string]*refLock
}

type refLock struct {
	mu   sync.Mutex
	refs int
}

func (k *keyLocker) Lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = make(map[string]*refLock)
	}
	rl, ok := k.locks[key]
	if !ok {
		rl = &refLock{}
		k.locks[key] = rl
	}
	rl.refs++
	k.mu.Unlock()

	rl.mu.Lock()
	return func() {
		rl.mu.Unlock()
		k.mu.Lock()
		rl.refs--
		if rl.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}
