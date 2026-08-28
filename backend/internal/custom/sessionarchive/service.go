package sessionarchive

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/google/uuid"
)

type RuntimeStatus struct {
	Enabled            bool
	ProcessStatus      string
	StorageStatus      string
	DatabaseStatus     string
	ActiveKeyID        string
	Bucket             string
	Prefix             string
	QueueEvents        int
	QueueEventCapacity int
	QueueBytes         int64
	QueueByteCapacity  int64
	EnqueuedTotal      uint64
	DroppedTotal       uint64
	TruncatedTotal     uint64
	StoredTotal        uint64
	FailedTotal        uint64
	StorageFailures    uint64
	ExportFailures     uint64
	PendingBacklog     int64
	GCBacklog          int64
	LastError          string
	LastSuccessAt      time.Time
}

type serviceMetrics struct {
	stored          atomic.Uint64
	failed          atomic.Uint64
	storageFailures atomic.Uint64
	exportFailures  atomic.Uint64
	pendingBacklog  atomic.Int64
	gcBacklog       atomic.Int64
	lastSuccessUnix atomic.Int64
	mu              sync.RWMutex
	lastError       string
}

func (m *serviceMetrics) failure(err error, storage bool) {
	m.failed.Add(1)
	if storage {
		m.storageFailures.Add(1)
	}
	m.mu.Lock()
	if err != nil {
		m.lastError = err.Error()
		if len(m.lastError) > 512 {
			m.lastError = m.lastError[:512]
		}
	}
	m.mu.Unlock()
}

type ServiceOptions struct {
	Config    config.SessionArchiveConfig
	DB        *sql.DB
	BlobStore BlobStore
	TempDir   string
}

type Service struct {
	cfg        config.SessionArchiveConfig
	repository *Repository
	codec      *Codec
	blobStore  BlobStore
	collector  *Collector
	metrics    *serviceMetrics

	started     atomic.Bool
	maintenance context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup

	policyMu    sync.RWMutex
	policyCache map[PolicyIdentity]cachedPolicy
}

type cachedPolicy struct {
	policy    ResolvedPolicy
	expiresAt time.Time
}

func NewService(ctx context.Context, opts ServiceOptions) (*Service, error) {
	service := &Service{cfg: opts.Config, metrics: &serviceMetrics{}, policyCache: make(map[PolicyIdentity]cachedPolicy)}
	service.maintenance, service.cancel = context.WithCancel(context.Background())
	if !opts.Config.Enabled {
		if opts.DB != nil {
			service.repository, _ = NewRepository(opts.DB)
		}
		return service, nil
	}
	keys, err := opts.Config.DecodedEncryptionKeys()
	if err != nil {
		return nil, err
	}
	repo, err := NewRepositoryWithDigestKeys(opts.DB, opts.Config.ActiveKeyID, keys)
	if err != nil {
		return nil, err
	}
	codec, err := NewCodec(keys, opts.Config.ActiveKeyID, defaultChunkSize, opts.Config.PayloadMaxBytes, opts.TempDir)
	if err != nil {
		return nil, err
	}
	store := opts.BlobStore
	if store == nil {
		store, err = NewS3BlobStore(ctx, repository.S3ClientParams{
			Endpoint: opts.Config.S3.Endpoint, Region: opts.Config.S3.Region,
			AccessKeyID: opts.Config.S3.AccessKeyID, SecretAccessKey: opts.Config.S3.SecretAccessKey,
			ForcePathStyle: opts.Config.S3.ForcePathStyle,
		}, opts.Config.S3.Bucket, opts.Config.S3.Prefix)
		if err != nil {
			return nil, err
		}
	}
	service.repository, service.codec, service.blobStore = repo, codec, store
	collector, err := NewCollector(CollectorOptions{
		WorkerCount: opts.Config.WorkerCount, QueueSize: opts.Config.QueueSize,
		QueueMaxBytes: opts.Config.QueueMaxBytes, PayloadMaxBytes: opts.Config.PayloadMaxBytes,
		MaxRetries: 6, RetryBackoff: 50 * time.Millisecond,
		Processor: &archiveProcessor{repository: repo, codec: codec, blobStore: store, prefix: opts.Config.S3.Prefix, mergeWindow: time.Duration(opts.Config.MergeWindowSeconds) * time.Second, metrics: service.metrics, tempDir: opts.TempDir},
	})
	if err != nil {
		return nil, err
	}
	service.collector = collector
	return service, nil
}

func (s *Service) Start(ctx context.Context) error {
	if s == nil || !s.cfg.Enabled {
		return nil
	}
	if s.started.Load() {
		return nil
	}
	if err := s.repository.CheckSchema(ctx); err != nil {
		s.metrics.failure(err, false)
		return err
	}
	if err := s.blobStore.SelfCheck(ctx); err != nil {
		s.metrics.failure(err, true)
		return err
	}
	if err := s.collector.Start(ctx); err != nil {
		s.metrics.failure(err, false)
		return err
	}
	if !s.started.CompareAndSwap(false, true) {
		return nil
	}
	s.wg.Add(1)
	go s.maintenanceLoop()
	return nil
}

func (s *Service) Shutdown(ctx context.Context) error {
	if s == nil || !s.cfg.Enabled || !s.started.Load() {
		return nil
	}
	timeout := time.Duration(s.cfg.ShutdownDrainSeconds) * time.Second
	drainCtx := ctx
	var cancel context.CancelFunc
	if _, ok := ctx.Deadline(); !ok {
		drainCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	err := s.collector.Shutdown(drainCtx)
	s.cancel()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		if err == nil {
			err = ctx.Err()
		}
	}
	return err
}

func (s *Service) ResolvePolicy(ctx context.Context, identity PolicyIdentity) ResolvedPolicy {
	if s == nil {
		return ResolvedPolicy{MatchedScope: ScopeGlobal}
	}
	fallback := DefaultResolvedPolicy(s.cfg.PayloadMaxBytes, s.cfg.DefaultRetentionDays)
	if !s.cfg.Enabled || s.repository == nil {
		return fallback
	}
	now := time.Now()
	s.policyMu.RLock()
	cached, ok := s.policyCache[identity]
	s.policyMu.RUnlock()
	if ok && now.Before(cached.expiresAt) {
		return cached.policy
	}
	policies, err := s.repository.PoliciesFor(ctx, identity)
	if err != nil {
		s.metrics.failure(err, false)
		return fallback
	}
	resolved := ResolvePolicy(identity, policies, fallback)
	s.policyMu.Lock()
	s.policyCache[identity] = cachedPolicy{policy: resolved, expiresAt: now.Add(30 * time.Second)}
	s.policyMu.Unlock()
	return resolved
}

func (s *Service) InvalidatePolicyCache() {
	if s == nil {
		return
	}
	s.policyMu.Lock()
	clear(s.policyCache)
	s.policyMu.Unlock()
}

func (s *Service) TryCaptureBytes(meta CaptureMeta, payload []byte) CaptureResult {
	return s.TryCaptureBytesObserved(meta, payload, int64(len(payload)))
}

func (s *Service) TryCaptureBytesObserved(meta CaptureMeta, payload []byte, observedBytes int64) CaptureResult {
	if s == nil || !s.cfg.Enabled || !s.started.Load() || s.collector == nil || !meta.Policy.Enabled {
		return CaptureResult{ObservedBytes: max(observedBytes, int64(len(payload))), DroppedReason: "archive_disabled"}
	}
	return s.collector.TryCaptureBytesObserved(meta, payload, observedBytes)
}

func (s *Service) NewSink(meta CaptureMeta) *CaptureSink {
	if s == nil || !s.cfg.Enabled || !s.started.Load() || !meta.Policy.Enabled {
		return (*Collector)(nil).NewSink(meta)
	}
	return s.collector.NewSink(meta)
}

func (s *Service) TryCapture(event CaptureEvent) CaptureResult {
	if s == nil || !s.cfg.Enabled || !s.started.Load() || !event.Meta.Policy.Enabled {
		return CaptureResult{ObservedBytes: event.Observation.ObservedBytes, DroppedReason: "archive_disabled"}
	}
	if len(event.Observation.StoredPayload) > 0 || event.Observation.StoredBytes > 0 {
		return CaptureResult{ObservedBytes: event.Observation.ObservedBytes, DroppedReason: "unbudgeted_payload"}
	}
	return s.collector.TryCapture(event)
}

func (s *Service) Status() RuntimeStatus {
	status := RuntimeStatus{Enabled: s != nil && s.cfg.Enabled, ProcessStatus: "disabled", StorageStatus: "unconfigured", DatabaseStatus: "unconfigured"}
	if s == nil {
		return status
	}
	status.ActiveKeyID, status.Bucket, status.Prefix = s.cfg.ActiveKeyID, s.cfg.S3.Bucket, s.cfg.S3.Prefix
	if !s.cfg.Enabled {
		return status
	}
	status.ProcessStatus, status.StorageStatus, status.DatabaseStatus = "starting", "configured", "configured"
	if s.collector != nil {
		status.QueueEvents, status.QueueEventCapacity = len(s.collector.queue), cap(s.collector.queue)
		status.QueueBytes, status.QueueByteCapacity = s.collector.budget.InUse(), s.collector.budget.Capacity()
		status.EnqueuedTotal = s.collector.metrics.accepted.Load()
		status.DroppedTotal = s.collector.metrics.droppedFull.Load() + s.collector.metrics.droppedStopped.Load() + s.collector.metrics.droppedBudget.Load() + s.collector.metrics.droppedPermanent.Load()
		status.TruncatedTotal = s.collector.metrics.truncated.Load()
		status.FailedTotal = s.collector.metrics.processFailed.Load() + s.metrics.failed.Load()
	}
	status.StoredTotal = s.metrics.stored.Load()
	status.StorageFailures = s.metrics.storageFailures.Load()
	status.ExportFailures = s.metrics.exportFailures.Load()
	status.PendingBacklog, status.GCBacklog = s.metrics.pendingBacklog.Load(), s.metrics.gcBacklog.Load()
	s.metrics.mu.RLock()
	status.LastError = s.metrics.lastError
	s.metrics.mu.RUnlock()
	if unix := s.metrics.lastSuccessUnix.Load(); unix > 0 {
		status.LastSuccessAt = time.Unix(unix, 0).UTC()
	}
	if s.started.Load() {
		status.ProcessStatus, status.StorageStatus, status.DatabaseStatus = "running", "ready", "ready"
	}
	if status.LastError != "" && s.started.Load() {
		status.ProcessStatus = "degraded"
	}
	return status
}

func (s *Service) OpsMetric(metricType string) (float64, bool) {
	status := s.Status()
	switch metricType {
	case "session_archive_queue_dropped":
		return float64(status.DroppedTotal), true
	case "session_archive_storage_failures":
		return float64(status.StorageFailures), true
	case "session_archive_pending_backlog":
		return float64(status.PendingBacklog), true
	case "session_archive_gc_backlog":
		return float64(status.GCBacklog), true
	default:
		return 0, false
	}
}

func (s *Service) maintenanceLoop() {
	defer s.wg.Done()
	interval := time.Duration(s.cfg.MaintenanceIntervalSecs) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.runMaintenance()
		case <-s.maintenance.Done():
			return
		}
	}
}

func (s *Service) runMaintenance() {
	ctx, cancel := context.WithTimeout(s.maintenance, time.Duration(s.cfg.MaintenanceIntervalSecs)*time.Second)
	defer cancel()
	if _, err := s.repository.RecoverStalePending(ctx); err != nil {
		s.metrics.failure(err, true)
	}
	if _, err := s.repository.RecoverStaleDeleting(ctx, 5*time.Minute); err != nil {
		s.metrics.failure(err, true)
	}
	if _, err := s.repository.ScheduleOrphanReadyBlobs(ctx, orphanReadyBlobAge, s.cfg.CleanupBatchSize); err != nil {
		s.metrics.failure(err, true)
	}
	if _, err := s.repository.DeleteExpiredCorrelationFences(ctx, s.cfg.CleanupBatchSize); err != nil {
		s.metrics.failure(err, false)
	}
	if _, err := s.repository.DeleteExpiredSessions(ctx, s.cfg.CleanupBatchSize, time.Duration(s.cfg.GCGraceSeconds)*time.Second); err != nil {
		s.metrics.failure(err, false)
	}
	if _, err := s.repository.ProcessDeletionJob(ctx, s.cfg.CleanupBatchSize, time.Duration(s.cfg.GCGraceSeconds)*time.Second); err != nil {
		s.metrics.failure(err, false)
	}
	blobs, err := s.repository.ClaimGCBlobs(ctx, s.cfg.CleanupBatchSize)
	if err != nil {
		s.metrics.failure(err, true)
	} else {
		for _, blob := range blobs {
			deleteErr := s.blobStore.Delete(ctx, blob.ObjectKey)
			if finishErr := s.repository.FinishGCBlob(ctx, blob.ID, deleteErr, time.Minute); finishErr != nil {
				s.metrics.failure(finishErr, true)
			}
			if deleteErr != nil {
				s.metrics.failure(deleteErr, true)
			}
		}
	}
	pending, gc, err := s.repository.BlobBacklogs(ctx)
	if err != nil {
		s.metrics.failure(err, false)
		return
	}
	s.metrics.pendingBacklog.Store(pending)
	s.metrics.gcBacklog.Store(gc)
}

type archiveProcessor struct {
	repository  *Repository
	codec       *Codec
	blobStore   BlobStore
	prefix      string
	mergeWindow time.Duration
	metrics     *serviceMetrics
	tempDir     string
}

type blobReservationRepository interface {
	ReserveBlob(context.Context, EncodingInfo, string, string, time.Duration) (BlobRecord, bool, error)
	MarkBlobReady(context.Context, int64, string) error
	MarkBlobFailed(context.Context, int64, string, error) error
}

const (
	blobUploadLease      = 2 * time.Minute
	blobReadyWaitLimit   = blobUploadLease + time.Second
	blobReadyPollInitial = 10 * time.Millisecond
	blobReadyPollMaximum = 250 * time.Millisecond
)

func (p *archiveProcessor) Process(ctx context.Context, event CaptureEvent) error {
	ids, err := p.repository.EnsureProjection(ctx, event, p.mergeWindow)
	if err != nil {
		if !errors.Is(err, ErrCorrelationFenced) {
			p.metrics.failure(err, false)
		}
		return err
	}
	if event.Meta.Purpose == "" {
		p.metrics.lastSuccessUnix.Store(time.Now().Unix())
		return nil
	}
	if event.Observation.StoredBytes == 0 {
		err = p.repository.AddBlobRef(ctx, ids, event, nil)
		if err != nil {
			p.metrics.failure(err, false)
		}
		return err
	}
	tmp, err := os.CreateTemp(p.tempDir, "session-archive-encode-*")
	if err != nil {
		p.metrics.failure(err, true)
		return err
	}
	name := tmp.Name()
	defer func() { _ = tmp.Close(); _ = os.Remove(name) }()
	info, err := p.codec.Encode(bytes.NewReader(event.Observation.StoredPayload), tmp)
	if err != nil {
		p.metrics.failure(err, true)
		return err
	}
	if event.Observation.StoredSHA256 != "" && info.StoredPlaintextSHA256 != event.Observation.StoredSHA256 {
		err = errors.New("captured payload hash changed before storage")
		p.metrics.failure(err, true)
		return err
	}
	objectKey, err := CASObjectKey(p.prefix, info)
	if err != nil {
		p.metrics.failure(err, true)
		return err
	}
	blob, err := storeCASBlob(ctx, p.repository, p.blobStore, tmp, info, objectKey, blobUploadLease, blobReadyWaitLimit)
	if err != nil {
		p.metrics.failure(err, true)
		return err
	}
	err = p.repository.AddBlobRef(ctx, ids, event, &blob.ID)
	if err != nil {
		p.metrics.failure(err, false)
		return err
	}
	p.metrics.stored.Add(1)
	p.metrics.lastSuccessUnix.Store(time.Now().Unix())
	return nil
}

func (p *archiveProcessor) RecordFailure(ctx context.Context, event CaptureEvent, _ error) error {
	if event.Observation.StoredBytes <= 0 && len(event.Observation.StoredPayload) == 0 {
		return nil
	}
	ids, err := p.repository.EnsureProjection(ctx, event, p.mergeWindow)
	if err != nil {
		if errors.Is(err, ErrCorrelationFenced) {
			return nil
		}
		return err
	}
	event = storageFailureEvent(event)
	return p.repository.AddStorageFailureRef(ctx, ids, event)
}

func storageFailureEvent(event CaptureEvent) CaptureEvent {
	event.Observation.StoredPayload = nil
	event.Observation.StoredSHA256 = ""
	event.Observation.StoredBytes = 0
	event.Observation.DroppedReason = "storage_failed"
	return event
}

func storeCASBlob(ctx context.Context, repository blobReservationRepository, blobStore BlobStore, body io.ReadSeeker, info EncodingInfo, objectKey string, lease, waitLimit time.Duration) (BlobRecord, error) {
	if repository == nil || blobStore == nil || body == nil || lease <= 0 || waitLimit <= 0 {
		return BlobRecord{}, errors.New("invalid CAS blob storage dependencies")
	}
	ownerToken := uuid.NewString()
	deadline := time.Now().Add(waitLimit)
	pollInterval := blobReadyPollInitial
	for {
		blob, owner, err := repository.ReserveBlob(ctx, info, objectKey, ownerToken, lease)
		if err != nil {
			return BlobRecord{}, err
		}
		if owner {
			if blob.ObjectKey == "" {
				err := fmt.Errorf("reserved blob %d has no object key", blob.ID)
				_ = repository.MarkBlobFailed(ctx, blob.ID, ownerToken, err)
				return BlobRecord{}, err
			}
			if _, err := body.Seek(0, 0); err != nil {
				_ = repository.MarkBlobFailed(ctx, blob.ID, ownerToken, err)
				return BlobRecord{}, err
			}
			if err := blobStore.Put(ctx, blob.ObjectKey, body, info.CiphertextBytes); err != nil {
				_ = repository.MarkBlobFailed(ctx, blob.ID, ownerToken, err)
				return BlobRecord{}, err
			}
			if err := repository.MarkBlobReady(ctx, blob.ID, ownerToken); err != nil {
				_ = repository.MarkBlobFailed(ctx, blob.ID, ownerToken, err)
				return BlobRecord{}, err
			}
			blob.Status = "ready"
			return blob, nil
		}
		switch blob.Status {
		case "ready":
			return blob, nil
		case "pending", "failed", "gc_pending":
			// ReserveBlob atomically takes over failed/gc_pending rows and
			// pending rows whose upload lease has expired. A non-owner only
			// waits while the current owner still has a valid lease.
		default:
			return BlobRecord{}, fmt.Errorf("blob %d is unavailable: %s", blob.ID, blob.Status)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return BlobRecord{}, fmt.Errorf("timed out waiting for pending blob %d", blob.ID)
		}
		wait := pollInterval
		if wait > remaining {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return BlobRecord{}, ctx.Err()
		case <-timer.C:
		}
		if pollInterval < blobReadyPollMaximum {
			pollInterval *= 2
			if pollInterval > blobReadyPollMaximum {
				pollInterval = blobReadyPollMaximum
			}
		}
	}
}

func (s *Service) Repository() *Repository { return s.repository }
func (s *Service) Codec() *Codec           { return s.codec }
func (s *Service) BlobStore() BlobStore    { return s.blobStore }
