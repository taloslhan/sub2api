package sessionarchive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrCollectorStopped = errors.New("session archive collector stopped")
	ErrQueueFull        = errors.New("session archive collector queue full")
)

type ByteBudget struct {
	capacity int64
	inUse    atomic.Int64
}

func NewByteBudget(capacity int64) *ByteBudget {
	if capacity < 0 {
		capacity = 0
	}
	return &ByteBudget{capacity: capacity}
}

func (b *ByteBudget) TryAcquire(n int64) bool {
	if b == nil || n < 0 {
		return false
	}
	if n == 0 {
		return true
	}
	for {
		current := b.inUse.Load()
		if n > b.capacity-current {
			return false
		}
		if b.inUse.CompareAndSwap(current, current+n) {
			return true
		}
	}
}

func (b *ByteBudget) Release(n int64) {
	if b == nil || n <= 0 {
		return
	}
	remaining := b.inUse.Add(-n)
	if remaining < 0 {
		b.inUse.Store(0)
		panic("sessionarchive: byte budget released more than acquired")
	}
}

func (b *ByteBudget) InUse() int64    { return b.inUse.Load() }
func (b *ByteBudget) Capacity() int64 { return b.capacity }

type Processor interface {
	Process(context.Context, CaptureEvent) error
}

type FailureRecorder interface {
	RecordFailure(context.Context, CaptureEvent, error) error
}

type ProcessorFunc func(context.Context, CaptureEvent) error

func (f ProcessorFunc) Process(ctx context.Context, event CaptureEvent) error { return f(ctx, event) }

type collectorMetrics struct {
	accepted         atomic.Uint64
	processed        atomic.Uint64
	processFailed    atomic.Uint64
	retried          atomic.Uint64
	droppedFull      atomic.Uint64
	droppedStopped   atomic.Uint64
	droppedBudget    atomic.Uint64
	droppedPermanent atomic.Uint64
	truncated        atomic.Uint64
}

type CollectorOptions struct {
	WorkerCount     int
	QueueSize       int
	QueueMaxBytes   int64
	PayloadMaxBytes int64
	MaxRetries      int
	RetryBackoff    time.Duration
	Processor       Processor
}

type Collector struct {
	opts      CollectorOptions
	budget    *ByteBudget
	queue     chan CaptureEvent
	metrics   *collectorMetrics
	ctx       context.Context
	cancel    context.CancelFunc
	started   atomic.Bool
	accepting atomic.Bool
	startOnce sync.Once
	stopOnce  sync.Once
	enqueueMu sync.RWMutex
	wg        sync.WaitGroup
}

func NewCollector(opts CollectorOptions) (*Collector, error) {
	if opts.WorkerCount < 1 || opts.QueueSize < 1 || opts.QueueMaxBytes < 1 || opts.PayloadMaxBytes < 1 || opts.PayloadMaxBytes > opts.QueueMaxBytes {
		return nil, errors.New("invalid collector limits")
	}
	if opts.Processor == nil {
		return nil, errors.New("collector processor is required")
	}
	if opts.MaxRetries < 0 {
		return nil, errors.New("collector max retries must be non-negative")
	}
	if opts.RetryBackoff <= 0 {
		opts.RetryBackoff = 25 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Collector{
		opts: opts, budget: NewByteBudget(opts.QueueMaxBytes), queue: make(chan CaptureEvent, opts.QueueSize),
		metrics: &collectorMetrics{}, ctx: ctx, cancel: cancel,
	}, nil
}

func (c *Collector) Start(_ context.Context) error {
	if c == nil {
		return errors.New("nil collector")
	}
	c.startOnce.Do(func() {
		c.started.Store(true)
		c.accepting.Store(true)
		for i := 0; i < c.opts.WorkerCount; i++ {
			c.wg.Add(1)
			go c.worker()
		}
	})
	return nil
}

func (c *Collector) NewSink(meta CaptureMeta) *CaptureSink {
	if c == nil {
		return &CaptureSink{meta: cloneMeta(meta), observedHash: sha256.New(), dropped: "collector_stopped"}
	}
	maxBytes := c.opts.PayloadMaxBytes
	if meta.Policy.PayloadMaxBytes > 0 && meta.Policy.PayloadMaxBytes < maxBytes {
		maxBytes = meta.Policy.PayloadMaxBytes
	}
	return &CaptureSink{collector: c, meta: cloneMeta(meta), maxBytes: maxBytes, observedHash: sha256.New()}
}

func (c *Collector) TryCaptureBytes(meta CaptureMeta, payload []byte) CaptureResult {
	return c.TryCaptureBytesObserved(meta, payload, int64(len(payload)))
}

func (c *Collector) TryCaptureBytesObserved(meta CaptureMeta, payload []byte, observedBytes int64) CaptureResult {
	if c == nil {
		return CaptureResult{ObservedBytes: max(observedBytes, int64(len(payload))), DroppedReason: "collector_stopped"}
	}
	sink := c.NewSink(meta)
	_, _ = sink.Append(payload)
	sink.SetObservedBytes(observedBytes)
	return sink.Finish()
}

func (c *Collector) TryCapture(event CaptureEvent) CaptureResult {
	if len(event.Observation.StoredPayload) > 0 || event.Observation.StoredBytes > 0 || event.permitBytes > 0 {
		return CaptureResult{ObservedBytes: event.Observation.ObservedBytes, DroppedReason: "unbudgeted_payload"}
	}
	if event.Meta.OccurredAt.IsZero() {
		event.Meta.OccurredAt = time.Now().UTC()
	}
	event.Meta = cloneMeta(event.Meta)
	return c.enqueue(event)
}

func (c *Collector) enqueue(event CaptureEvent) CaptureResult {
	result := CaptureResult{
		Truncated: event.Observation.Truncated, DroppedReason: event.Observation.DroppedReason,
		ObservedBytes: event.Observation.ObservedBytes, StoredBytes: event.Observation.StoredBytes,
	}
	if c == nil {
		result.DroppedReason = "collector_stopped"
		return result
	}
	c.enqueueMu.RLock()
	defer c.enqueueMu.RUnlock()
	if !c.accepting.Load() {
		c.budget.Release(event.permitBytes)
		c.metrics.droppedStopped.Add(1)
		result.DroppedReason = "collector_stopped"
		return result
	}
	select {
	case c.queue <- event:
		c.metrics.accepted.Add(1)
		result.Accepted = true
		return result
	default:
		c.budget.Release(event.permitBytes)
		c.metrics.droppedFull.Add(1)
		result.DroppedReason = "queue_full"
		return result
	}
}

func (c *Collector) worker() {
	defer c.wg.Done()
	for event := range c.queue {
		c.process(event)
	}
}

func (c *Collector) process(event CaptureEvent) {
	defer c.budget.Release(event.permitBytes)
	var err error
	for attempt := 0; attempt <= c.opts.MaxRetries; attempt++ {
		err = c.opts.Processor.Process(c.ctx, event)
		if err == nil {
			c.metrics.processed.Add(1)
			return
		}
		if errors.Is(err, ErrCorrelationFenced) {
			c.metrics.droppedPermanent.Add(1)
			return
		}
		if attempt < c.opts.MaxRetries {
			c.metrics.retried.Add(1)
			timer := time.NewTimer(c.opts.RetryBackoff << attempt)
			select {
			case <-timer.C:
			case <-c.ctx.Done():
				timer.Stop()
			}
		}
	}
	c.metrics.processFailed.Add(1)
	if event.Observation.StoredBytes <= 0 && len(event.Observation.StoredPayload) == 0 {
		return
	}
	if recorder, ok := c.opts.Processor.(FailureRecorder); ok {
		failureCtx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 2*time.Second)
		_ = recorder.RecordFailure(failureCtx, event, err)
		cancel()
	}
}

func (c *Collector) Shutdown(ctx context.Context) error {
	if c == nil || !c.started.Load() {
		return nil
	}
	c.stopOnce.Do(func() {
		c.enqueueMu.Lock()
		c.accepting.Store(false)
		close(c.queue)
		c.enqueueMu.Unlock()
	})
	done := make(chan struct{})
	go func() { c.wg.Wait(); close(done) }()
	select {
	case <-done:
		c.cancel()
		return nil
	case <-ctx.Done():
		c.cancel()
		return ctx.Err()
	}
}

type CaptureSink struct {
	mu           sync.Mutex
	collector    *Collector
	meta         CaptureMeta
	maxBytes     int64
	stored       []byte
	observedHash hash.Hash
	observed     int64
	permit       int64
	truncated    bool
	dropped      string
	finished     bool
}

func (s *CaptureSink) Append(chunk []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return 0, errors.New("capture sink already finished")
	}
	s.observed += int64(len(chunk))
	_, _ = s.observedHash.Write(chunk)
	if len(chunk) == 0 || s.dropped != "" || int64(len(s.stored)) >= s.maxBytes {
		if len(chunk) > 0 && int64(len(s.stored)) >= s.maxBytes {
			s.truncated = true
		}
		return len(chunk), nil
	}
	keep := int64(len(chunk))
	if remaining := s.maxBytes - int64(len(s.stored)); keep > remaining {
		keep = remaining
		s.truncated = true
	}
	if keep > 0 {
		if s.collector == nil || !s.collector.accepting.Load() {
			s.truncated = len(chunk) > 0
			s.dropped = "collector_stopped"
			return len(chunk), nil
		}
		if !s.collector.budget.TryAcquire(keep) {
			s.truncated = true
			s.dropped = "memory_budget"
			s.collector.metrics.droppedBudget.Add(1)
			return len(chunk), nil
		}
		s.stored = append(s.stored, chunk[:keep]...)
		s.permit += keep
	}
	if keep < int64(len(chunk)) {
		s.truncated = true
	}
	return len(chunk), nil
}

// SetObservedBytes 在调用方只持有截断前缀、但能从可信传输元数据取得完整长度时，
// 保存真实观测字节数。完整正文不可得时不伪造 observed hash。
func (s *CaptureSink) SetObservedBytes(observedBytes int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished || observedBytes <= s.observed {
		return
	}
	s.observed = observedBytes
	s.observedHash = nil
	s.truncated = true
}

func (s *CaptureSink) Finish() CaptureResult {
	s.mu.Lock()
	if s.finished {
		result := CaptureResult{DroppedReason: "already_finished", ObservedBytes: s.observed, StoredBytes: int64(len(s.stored)), Truncated: s.truncated}
		s.mu.Unlock()
		return result
	}
	s.finished = true
	observedHash := ""
	if s.observedHash != nil {
		observedHash = hex.EncodeToString(s.observedHash.Sum(nil))
	}
	storedSum := sha256.Sum256(s.stored)
	event := CaptureEvent{
		Meta: s.meta,
		Observation: Observation{
			StoredPayload: s.stored, ObservedSHA256: observedHash,
			StoredSHA256: hex.EncodeToString(storedSum[:]), ObservedBytes: s.observed,
			StoredBytes: int64(len(s.stored)), Truncated: s.truncated, DroppedReason: s.dropped,
		},
		permitBytes: s.permit,
	}
	if event.Meta.OccurredAt.IsZero() {
		event.Meta.OccurredAt = time.Now().UTC()
	}
	s.stored = nil
	s.permit = 0 // 所有权转移给 event/Collector。
	s.mu.Unlock()
	if s.collector == nil {
		return CaptureResult{Truncated: event.Observation.Truncated, DroppedReason: "collector_stopped", ObservedBytes: event.Observation.ObservedBytes, StoredBytes: event.Observation.StoredBytes}
	}
	if event.Observation.Truncated {
		s.collector.metrics.truncated.Add(1)
	}
	return s.collector.enqueue(event)
}

// Abort 放弃尚未入队的 sink 并释放已持有许可；可安全重复调用。
func (s *CaptureSink) Abort() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	s.finished = true
	permit := s.permit
	s.permit = 0
	s.stored = nil
	collector := s.collector
	s.mu.Unlock()
	if collector != nil {
		collector.budget.Release(permit)
	}
}

func cloneMeta(meta CaptureMeta) CaptureMeta {
	meta.NormalizedMessageChain = append([]string(nil), meta.NormalizedMessageChain...)
	if meta.Metadata != nil {
		copyMetadata := make(map[string]string, len(meta.Metadata))
		for key, value := range meta.Metadata {
			copyMetadata[key] = value
		}
		meta.Metadata = copyMetadata
	}
	return meta
}

var allowedMetadataKeys = map[string]struct{}{
	"user-agent": {}, "x-request-id": {}, "traceparent": {}, "tracestate": {},
	"openai-organization": {}, "openai-project": {}, "anthropic-version": {}, "anthropic-beta": {},
}

// AllowMetadata 仅复制显式白名单；Authorization/Cookie/API Key 等凭据没有旁路。
func AllowMetadata(input map[string][]string) map[string]string {
	output := make(map[string]string)
	for key, values := range input {
		key = normalizeHeaderKey(key)
		if _, ok := allowedMetadataKeys[key]; !ok || len(values) == 0 {
			continue
		}
		output[key] = values[0]
	}
	return output
}

func normalizeHeaderKey(value string) string {
	result := make([]byte, len(value))
	for i := range value {
		char := value[i]
		if char >= 'A' && char <= 'Z' {
			char += 'a' - 'A'
		}
		result[i] = char
	}
	return string(result)
}
