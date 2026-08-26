package service

import (
	"context"
	"errors"
	"time"

	coderws "github.com/coder/websocket"
)

type openAIWSClientReadResult struct {
	messageType coderws.MessageType
	payload     []byte
	err         error
}

const (
	openAIWSIngressPingTimeout = 5 * time.Second
	openAIWSIngressPingReason  = "websocket ingress ping failed; please reconnect"
)

// openAIWSIngressPingDisconnectError marks only ingress ping failures as
// client disconnects while preserving the transport failure in the cause chain.
type openAIWSIngressPingDisconnectError struct {
	err error
}

func (e *openAIWSIngressPingDisconnectError) Error() string {
	if e == nil || e.err == nil {
		return "openai websocket ingress ping failed"
	}
	return "openai websocket ingress ping failed: " + e.err.Error()
}

func (e *openAIWSIngressPingDisconnectError) Unwrap() []error {
	if e == nil {
		return nil
	}
	marker := coderws.CloseError{Code: coderws.StatusGoingAway, Reason: openAIWSIngressPingReason}
	if e.err == nil {
		return []error{marker}
	}
	return []error{
		marker,
		e.err,
	}
}

// ReadOpenAIWSClientMessage keeps one reader alive while control events send
// their close frame, then closes the transport and joins that reader.
func ReadOpenAIWSClientMessage(
	controlCtx context.Context,
	conn *coderws.Conn,
	timeout time.Duration,
	timeoutStatus coderws.StatusCode,
	timeoutReason string,
) (coderws.MessageType, []byte, error) {
	return readOpenAIWSClientMessageWithTimeoutStart(
		controlCtx,
		conn,
		timeout,
		timeoutStatus,
		timeoutReason,
		nil,
		nil,
		0,
	)
}

// readOpenAIWSClientMessageWithTimeoutStart supports readers whose timeout
// starts after a state transition, such as a completed passthrough turn. When
// timeoutActive is nil, a positive timeout starts immediately.
func readOpenAIWSClientMessageWithTimeoutStart(
	controlCtx context.Context,
	conn *coderws.Conn,
	timeout time.Duration,
	timeoutStatus coderws.StatusCode,
	timeoutReason string,
	timeoutStart <-chan struct{},
	timeoutActive func() bool,
	pingInterval time.Duration,
) (coderws.MessageType, []byte, error) {
	if conn == nil {
		return 0, nil, errors.New("openai websocket client connection is nil")
	}
	if controlCtx == nil {
		controlCtx = context.Background()
	}

	readDone := make(chan openAIWSClientReadResult, 1)
	go func() {
		messageType, payload, err := conn.Read(context.Background())
		readDone <- openAIWSClientReadResult{messageType: messageType, payload: payload, err: err}
	}()

	// CAPYBARA-PATCH: A single reader remains active while one cancellable ping
	// at most keeps the downstream transport alive between application turns.
	var timer *time.Timer
	var timeoutCh <-chan time.Time
	var pingTicker *time.Ticker
	var pingCh <-chan time.Time
	startIdleControls := func() {
		if timeoutActive != nil && !timeoutActive() {
			return
		}
		if timeout > 0 {
			if timer == nil {
				timer = time.NewTimer(timeout)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(timeout)
			}
			timeoutCh = timer.C
		}
		if pingInterval > 0 {
			if pingTicker == nil {
				pingTicker = time.NewTicker(pingInterval)
			} else {
				pingTicker.Reset(pingInterval)
			}
			pingCh = pingTicker.C
		}
	}
	if timeoutActive == nil || timeoutActive() {
		startIdleControls()
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
		if pingTicker != nil {
			pingTicker.Stop()
		}
	}()

	var pingCancel context.CancelFunc
	var pingDone <-chan error
	startPing := func() {
		if pingDone != nil || pingInterval <= 0 || (timeoutActive != nil && !timeoutActive()) {
			return
		}
		pingCtx, cancel := context.WithTimeout(context.Background(), openAIWSIngressPingTimeout)
		done := make(chan error, 1)
		pingCancel = cancel
		pingDone = done
		go func() {
			done <- conn.Ping(pingCtx)
		}()
	}
	stopAndJoinPing := func() {
		if pingDone == nil {
			return
		}
		pingCancel()
		<-pingDone
		pingCancel = nil
		pingDone = nil
	}

	closeAndJoin := func(status coderws.StatusCode, reason string, cause error) (coderws.MessageType, []byte, error) {
		_ = conn.Close(status, reason)
		_ = conn.CloseNow()
		<-readDone
		return 0, nil, NewOpenAIWSClientCloseError(status, reason, cause)
	}

	for {
		select {
		case result := <-readDone:
			stopAndJoinPing()
			return result.messageType, result.payload, result.err
		case <-timeoutStart:
			startIdleControls()
		case <-timeoutCh:
			stopAndJoinPing()
			return closeAndJoin(timeoutStatus, timeoutReason, context.DeadlineExceeded)
		case <-pingCh:
			startPing()
		case pingErr := <-pingDone:
			pingCancel()
			pingCancel = nil
			pingDone = nil
			if pingErr == nil {
				if pingTicker != nil {
					pingTicker.Reset(pingInterval)
				}
				continue
			}
			// Prefer already-observable business/control events over a concurrent
			// keepalive failure so ping cannot discard a valid client frame or
			// overwrite an ingress lease-loss/idle-timeout close code.
			select {
			case result := <-readDone:
				return result.messageType, result.payload, result.err
			case <-timeoutCh:
				return closeAndJoin(timeoutStatus, timeoutReason, context.DeadlineExceeded)
			case <-controlCtx.Done():
				cause := context.Cause(controlCtx)
				if errors.Is(cause, ErrOpenAIWSIngressLeaseLost) {
					return closeAndJoin(coderws.StatusTryAgainLater, "websocket ingress capacity lease lost; please reconnect", cause)
				}
				return closeAndJoin(coderws.StatusGoingAway, "websocket request canceled", cause)
			default:
			}
			return closeAndJoin(
				coderws.StatusGoingAway,
				openAIWSIngressPingReason,
				&openAIWSIngressPingDisconnectError{err: pingErr},
			)
		case <-controlCtx.Done():
			stopAndJoinPing()
			cause := context.Cause(controlCtx)
			if errors.Is(cause, ErrOpenAIWSIngressLeaseLost) {
				return closeAndJoin(
					coderws.StatusTryAgainLater,
					"websocket ingress capacity lease lost; please reconnect",
					cause,
				)
			}
			return closeAndJoin(coderws.StatusGoingAway, "websocket request canceled", cause)
		}
	}
}
