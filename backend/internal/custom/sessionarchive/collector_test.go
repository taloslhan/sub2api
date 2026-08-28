package sessionarchive

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type failureRecordingProcessor struct {
	processCalls atomic.Int64
	recordCalls  atomic.Int64
	processErr   error
	recorded     chan CaptureEvent
}

func (p *failureRecordingProcessor) Process(context.Context, CaptureEvent) error {
	p.processCalls.Add(1)
	return p.processErr
}

func (p *failureRecordingProcessor) RecordFailure(_ context.Context, event CaptureEvent, _ error) error {
	p.recordCalls.Add(1)
	if p.recorded != nil {
		p.recorded <- event
	}
	return nil
}

func TestCaptureSinkPayloadLimitBudgetAndAbort(t *testing.T) {
	collector, err := NewCollector(CollectorOptions{WorkerCount: 1, QueueSize: 1, QueueMaxBytes: 8, PayloadMaxBytes: 6, Processor: ProcessorFunc(func(context.Context, CaptureEvent) error { return nil })})
	require.NoError(t, err)
	collector.accepting.Store(true)
	sink := collector.NewSink(CaptureMeta{})
	_, err = sink.Append([]byte("123456789"))
	require.NoError(t, err)
	require.Equal(t, int64(6), collector.budget.InUse())
	sink.Abort()
	require.Zero(t, collector.budget.InUse())
	sink.Abort()

	sink = collector.NewSink(CaptureMeta{})
	_, _ = sink.Append([]byte("123456"))
	result := sink.Finish()
	require.True(t, result.Accepted)
	event := <-collector.queue
	require.Equal(t, []byte("123456"), event.Observation.StoredPayload)
	collector.budget.Release(event.permitBytes)
	require.Zero(t, collector.budget.InUse())
}

func TestCaptureSinkUsesKnownObservedLengthForTruncatedPrefix(t *testing.T) {
	collector, err := NewCollector(CollectorOptions{WorkerCount: 1, QueueSize: 1, QueueMaxBytes: 8, PayloadMaxBytes: 3, Processor: ProcessorFunc(func(context.Context, CaptureEvent) error { return nil })})
	require.NoError(t, err)
	collector.accepting.Store(true)

	result := collector.TryCaptureBytesObserved(CaptureMeta{}, []byte("abcd"), 10)
	require.True(t, result.Accepted)
	require.True(t, result.Truncated)
	require.Equal(t, int64(10), result.ObservedBytes)
	require.Equal(t, int64(3), result.StoredBytes)
	event := <-collector.queue
	require.Equal(t, int64(10), event.Observation.ObservedBytes)
	require.Empty(t, event.Observation.ObservedSHA256)
	collector.budget.Release(event.permitBytes)
}

func TestCollectorQueueFullReleasesRejectedPermit(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	collector, err := NewCollector(CollectorOptions{
		WorkerCount: 1, QueueSize: 1, QueueMaxBytes: 32, PayloadMaxBytes: 8,
		Processor: ProcessorFunc(func(context.Context, CaptureEvent) error { started <- struct{}{}; <-block; return nil }),
	})
	require.NoError(t, err)
	require.NoError(t, collector.Start(context.Background()))
	require.True(t, collector.TryCaptureBytes(CaptureMeta{}, []byte("first")).Accepted)
	<-started
	require.True(t, collector.TryCaptureBytes(CaptureMeta{}, []byte("second")).Accepted)
	before := collector.budget.InUse()
	result := collector.TryCaptureBytes(CaptureMeta{}, []byte("third"))
	require.False(t, result.Accepted)
	require.Equal(t, "queue_full", result.DroppedReason)
	require.Equal(t, before, collector.budget.InUse())
	close(block)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, collector.Shutdown(ctx))
	require.Zero(t, collector.budget.InUse())
}

func TestCollectorShutdownDrainsQueuedWork(t *testing.T) {
	var processed atomic.Int64
	collector, err := NewCollector(CollectorOptions{
		WorkerCount: 2, QueueSize: 64, QueueMaxBytes: 1024, PayloadMaxBytes: 16,
		Processor: ProcessorFunc(func(ctx context.Context, event CaptureEvent) error {
			require.NoError(t, ctx.Err())
			processed.Add(1)
			return nil
		}),
	})
	require.NoError(t, err)
	require.NoError(t, collector.Start(context.Background()))
	for i := 0; i < 40; i++ {
		require.True(t, collector.TryCaptureBytes(CaptureMeta{}, []byte("payload")).Accepted)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, collector.Shutdown(ctx))
	require.Equal(t, int64(40), processed.Load())
	require.Zero(t, collector.budget.InUse())
}

func TestCollectorConcurrentCaptureAndShutdownNoPanicOrLeak(t *testing.T) {
	collector, err := NewCollector(CollectorOptions{WorkerCount: 2, QueueSize: 16, QueueMaxBytes: 4096, PayloadMaxBytes: 32, Processor: ProcessorFunc(func(context.Context, CaptureEvent) error { return nil })})
	require.NoError(t, err)
	require.NoError(t, collector.Start(context.Background()))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				collector.TryCaptureBytes(CaptureMeta{}, []byte("payload"))
			}
		}()
	}
	time.Sleep(time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, collector.Shutdown(ctx))
	wg.Wait()
	require.Eventually(t, func() bool { return collector.budget.InUse() == 0 }, time.Second, time.Millisecond)
}

func TestAllowMetadataExcludesCredentials(t *testing.T) {
	got := AllowMetadata(map[string][]string{
		"Authorization": {"Bearer secret"},
		"Cookie":        {"sid=secret"},
		"X-API-Key":     {"sk-secret"},
		"User-Agent":    {"codex"},
		"Traceparent":   {"00-abc"},
	})
	require.Equal(t, map[string]string{"user-agent": "codex", "traceparent": "00-abc"}, got)
}

func TestCollectorRecordsBodyFailureAfterRetriesExhausted(t *testing.T) {
	processor := &failureRecordingProcessor{processErr: errors.New("storage unavailable"), recorded: make(chan CaptureEvent, 1)}
	collector, err := NewCollector(CollectorOptions{
		WorkerCount: 1, QueueSize: 1, QueueMaxBytes: 64, PayloadMaxBytes: 32,
		MaxRetries: 1, RetryBackoff: time.Millisecond, Processor: processor,
	})
	require.NoError(t, err)
	require.NoError(t, collector.Start(context.Background()))
	require.True(t, collector.TryCaptureBytes(CaptureMeta{}, []byte("body")).Accepted)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, collector.Shutdown(ctx))

	require.Equal(t, int64(2), processor.processCalls.Load())
	require.Equal(t, int64(1), processor.recordCalls.Load())
	require.Equal(t, []byte("body"), (<-processor.recorded).Observation.StoredPayload)
	require.Equal(t, uint64(1), collector.metrics.processFailed.Load())
}

func TestCollectorDoesNotRetryOrRecordFencedCorrelation(t *testing.T) {
	processor := &failureRecordingProcessor{processErr: ErrCorrelationFenced}
	collector, err := NewCollector(CollectorOptions{
		WorkerCount: 1, QueueSize: 1, QueueMaxBytes: 64, PayloadMaxBytes: 32,
		MaxRetries: 5, RetryBackoff: time.Millisecond, Processor: processor,
	})
	require.NoError(t, err)
	require.NoError(t, collector.Start(context.Background()))
	require.True(t, collector.TryCaptureBytes(CaptureMeta{}, []byte("late body")).Accepted)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, collector.Shutdown(ctx))

	require.Equal(t, int64(1), processor.processCalls.Load())
	require.Zero(t, processor.recordCalls.Load())
	require.Equal(t, uint64(1), collector.metrics.droppedPermanent.Load())
	require.Zero(t, collector.metrics.processFailed.Load())
}
