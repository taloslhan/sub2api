package sessionarchive

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

const (
	StorageBackendS3         = "s3"
	StorageBackendFilesystem = "filesystem"
	StorageBackendPostgreSQL = "postgresql"
)

var ErrStorageBackendUnavailable = errors.New("storage backend unavailable")

type StorageBackendUnavailableError struct {
	Backend string
}

func (e *StorageBackendUnavailableError) Error() string {
	backend := strings.TrimSpace(e.Backend)
	if backend == "" {
		backend = "unknown"
	}
	return fmt.Sprintf("storage_backend_unavailable: %s", backend)
}

func (e *StorageBackendUnavailableError) Unwrap() error { return ErrStorageBackendUnavailable }

type StoreEntry struct {
	Backend   string
	Store     BlobStore
	Namespace string
	Location  string
	Ready     bool
	LastError string
}

type StoreRegistry struct {
	mu      sync.RWMutex
	active  string
	entries map[string]*StoreEntry
}

func NewStoreRegistry(active string, entries ...StoreEntry) (*StoreRegistry, error) {
	active = normalizeStorageBackend(active)
	registry := &StoreRegistry{active: active, entries: make(map[string]*StoreEntry, len(entries))}
	for _, source := range entries {
		entry := source
		entry.Backend = normalizeStorageBackend(entry.Backend)
		entry.Namespace = strings.Trim(strings.TrimSpace(entry.Namespace), "/")
		if entry.Backend == "" || entry.Store == nil || entry.Namespace == "" {
			return nil, errors.New("invalid session archive store registry entry")
		}
		if _, exists := registry.entries[entry.Backend]; exists {
			return nil, fmt.Errorf("duplicate session archive store backend %s", entry.Backend)
		}
		registry.entries[entry.Backend] = &entry
	}
	if _, exists := registry.entries[active]; !exists {
		return nil, &StorageBackendUnavailableError{Backend: active}
	}
	return registry, nil
}

func normalizeStorageBackend(backend string) string {
	return strings.ToLower(strings.TrimSpace(backend))
}

func (r *StoreRegistry) Active() (StoreEntry, error) {
	if r == nil {
		return StoreEntry{}, &StorageBackendUnavailableError{}
	}
	r.mu.RLock()
	entry := r.entries[r.active]
	if entry == nil {
		r.mu.RUnlock()
		return StoreEntry{}, &StorageBackendUnavailableError{Backend: r.active}
	}
	copy := *entry
	r.mu.RUnlock()
	return copy, nil
}

func (r *StoreRegistry) Resolve(backend string) (StoreEntry, error) {
	backend = normalizeStorageBackend(backend)
	if r == nil {
		return StoreEntry{}, &StorageBackendUnavailableError{Backend: backend}
	}
	r.mu.RLock()
	entry, ok := r.entries[backend]
	if !ok || !entry.Ready {
		r.mu.RUnlock()
		return StoreEntry{}, &StorageBackendUnavailableError{Backend: backend}
	}
	copy := *entry
	r.mu.RUnlock()
	return copy, nil
}

func (r *StoreRegistry) SetHealth(backend string, ready bool, cause error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[normalizeStorageBackend(backend)]
	if entry == nil {
		return
	}
	entry.Ready = ready
	entry.LastError = ""
	if cause != nil {
		entry.LastError = cause.Error()
		if len(entry.LastError) > 256 {
			entry.LastError = entry.LastError[:256]
		}
	}
}

func (r *StoreRegistry) Entries() []StoreEntry {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	entries := make([]StoreEntry, 0, len(r.entries))
	for _, source := range r.entries {
		entries = append(entries, *source)
	}
	r.mu.RUnlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Backend < entries[j].Backend })
	return entries
}

func (r *StoreRegistry) ReadyBackends() []string {
	entries := r.Entries()
	backends := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Ready {
			backends = append(backends, entry.Backend)
		}
	}
	return backends
}

func (r *StoreRegistry) Close() error {
	var joined error
	for _, entry := range r.Entries() {
		if closer, ok := entry.Store.(io.Closer); ok {
			joined = errors.Join(joined, closer.Close())
		}
	}
	return joined
}
