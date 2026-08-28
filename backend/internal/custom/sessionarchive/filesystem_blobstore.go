package sessionarchive

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
)

// FilesystemBlobStore confines every object operation to a dedicated os.Root.
// The root descriptor removes path/symlink escape races from normal file I/O.
type FilesystemBlobStore struct {
	root *os.Root
}

func NewFilesystemBlobStore(rootPath string) (*FilesystemBlobStore, error) {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return nil, errors.New("filesystem archive root is required")
	}
	if err := os.MkdirAll(rootPath, 0o700); err != nil {
		return nil, fmt.Errorf("create filesystem archive root: %w", err)
	}
	if err := os.Chmod(rootPath, 0o700); err != nil {
		return nil, fmt.Errorf("secure filesystem archive root: %w", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open filesystem archive root: %w", err)
	}
	return &FilesystemBlobStore{root: root}, nil
}

func (s *FilesystemBlobStore) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	if err := validatePortableBlobKey(key); err != nil {
		return err
	}
	if body == nil || size < 0 {
		return errors.New("invalid blob body or size")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if info, err := s.root.Stat(key); err == nil {
		return validateExistingFilesystemObject(info, size)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat filesystem archive object: %w", err)
	}

	dir := path.Dir(key)
	if err := s.root.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create filesystem archive object directory: %w", err)
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	tempKey := path.Join(dir, "."+path.Base(key)+".tmp-"+hex.EncodeToString(random))
	temp, err := s.root.OpenFile(tempKey, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create filesystem archive temporary object: %w", err)
	}
	keepTemp := true
	defer func() {
		_ = temp.Close()
		if keepTemp {
			_ = s.root.Remove(tempKey)
		}
	}()

	contextBody := &contextReader{ctx: ctx, reader: body}
	written, copyErr := io.CopyN(temp, contextBody, size)
	if copyErr != nil || written != size {
		if copyErr == nil {
			copyErr = io.ErrUnexpectedEOF
		}
		return fmt.Errorf("write filesystem archive object: %w", copyErr)
	}
	var extra [1]byte
	if n, readErr := contextBody.Read(extra[:]); n != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		if readErr == nil {
			readErr = errors.New("blob body exceeds declared size")
		}
		return fmt.Errorf("validate filesystem archive object size: %w", readErr)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync filesystem archive object: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close filesystem archive object: %w", err)
	}
	if err := s.root.Link(tempKey, key); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("publish filesystem archive object: %w", err)
		}
		info, statErr := s.root.Stat(key)
		if statErr != nil {
			return fmt.Errorf("verify concurrent filesystem archive object: %w", statErr)
		}
		if err := validateExistingFilesystemObject(info, size); err != nil {
			return err
		}
	}
	if err := s.root.Remove(tempKey); err != nil {
		return fmt.Errorf("remove filesystem archive temporary object: %w", err)
	}
	keepTemp = false
	directory, err := s.root.Open(dir)
	if err != nil {
		return fmt.Errorf("open filesystem archive directory for sync: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return fmt.Errorf("sync filesystem archive directory: %w", syncErr)
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

func validateExistingFilesystemObject(info fs.FileInfo, size int64) error {
	if !info.Mode().IsRegular() {
		return errors.New("filesystem archive object is not a regular file")
	}
	if info.Size() != size {
		return errors.New("filesystem archive object size conflicts with existing object")
	}
	return nil
}

func (s *FilesystemBlobStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := validatePortableBlobKey(key); err != nil {
		return nil, err
	}
	file, err := s.root.Open(key)
	if err != nil {
		return nil, fmt.Errorf("open filesystem archive object: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("filesystem archive object is not a regular file")
	}
	return &filesystemContextReadCloser{ctx: ctx, ReadCloser: file}, nil
}

func (s *FilesystemBlobStore) Delete(ctx context.Context, key string) error {
	if err := validatePortableBlobKey(key); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := s.root.Lstat(key)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("filesystem archive object is not a regular file")
	}
	if err := s.root.Remove(key); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("delete filesystem archive object: %w", err)
	}
	return nil
}

func (s *FilesystemBlobStore) List(ctx context.Context, prefix, cursor string, limit int32) (BlobListPage, error) {
	prefix = strings.TrimSuffix(strings.TrimSpace(prefix), "/")
	if prefix != "" {
		if err := validatePortableBlobKey(prefix); err != nil {
			return BlobListPage{}, err
		}
	}
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	last, err := decodeKeysetCursor(cursor)
	if err != nil {
		return BlobListPage{}, err
	}
	start := prefix
	if start == "" {
		start = "."
	}
	keys := make([]string, 0, limit+1)
	err = fs.WalkDir(s.root.FS(), start, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) && name == start {
				return fs.SkipDir
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type().IsRegular() && name > last {
			keys = append(keys, name)
			if len(keys) > int(limit) {
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return BlobListPage{}, fmt.Errorf("list filesystem archive objects: %w", err)
	}
	sort.Strings(keys)
	page := BlobListPage{}
	if len(keys) > int(limit) {
		page.Keys = keys[:limit]
		page.NextCursor = encodeKeysetCursor(page.Keys[len(page.Keys)-1])
	} else {
		page.Keys = keys
	}
	return page, nil
}

func (s *FilesystemBlobStore) SelfCheck(ctx context.Context) error {
	return selfCheckBlobStore(ctx, s, "self-check")
}

func (s *FilesystemBlobStore) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	return s.root.Close()
}

func validatePortableBlobKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" || key == "." || strings.HasPrefix(key, "/") || strings.Contains(key, "\\") || strings.ContainsRune(key, '\x00') || path.Clean(key) != key {
		return errors.New("invalid archive object key")
	}
	for _, component := range strings.Split(key, "/") {
		if component == "" || component == "." || component == ".." {
			return errors.New("invalid archive object key")
		}
	}
	return nil
}

func encodeKeysetCursor(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key))
}

func decodeKeysetCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(raw) == 0 {
		return "", errors.New("invalid archive list cursor")
	}
	return string(raw), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

type filesystemContextReadCloser struct {
	ctx context.Context
	io.ReadCloser
}

func (r *filesystemContextReadCloser) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.ReadCloser.Read(buffer)
}

func selfCheckBlobStore(ctx context.Context, store BlobStore, namespace string) error {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	key := strings.TrimSuffix(namespace, "/") + "/" + hex.EncodeToString(random)
	payload := []byte("session-archive-private-self-check")
	if err := store.Put(ctx, key, bytes.NewReader(payload), int64(len(payload))); err != nil {
		return err
	}
	reader, err := store.Get(ctx, key)
	if err != nil {
		_ = store.Delete(context.Background(), key)
		return err
	}
	got, readErr := io.ReadAll(io.LimitReader(reader, int64(len(payload)+1)))
	closeErr := reader.Close()
	deleteErr := store.Delete(ctx, key)
	if readErr != nil || closeErr != nil || deleteErr != nil || !bytes.Equal(got, payload) {
		return errors.New("private archive object store write/read/delete self-check failed")
	}
	return nil
}
