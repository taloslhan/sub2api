package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestReadOpenAIWSClientMessage_ControlCloseFrames(t *testing.T) {
	tests := []struct {
		name          string
		timeout       time.Duration
		timeoutStatus coderws.StatusCode
		timeoutReason string
		cancelCause   error
		wantStatus    coderws.StatusCode
		wantReason    string
	}{
		{
			name:          "inter-turn idle sends normal close",
			timeout:       25 * time.Millisecond,
			timeoutStatus: coderws.StatusNormalClosure,
			timeoutReason: "websocket idle timeout",
			wantStatus:    coderws.StatusNormalClosure,
			wantReason:    "websocket idle timeout",
		},
		{
			name:          "first message timeout sends policy close",
			timeout:       25 * time.Millisecond,
			timeoutStatus: coderws.StatusPolicyViolation,
			timeoutReason: "missing first response.create message",
			wantStatus:    coderws.StatusPolicyViolation,
			wantReason:    "missing first response.create message",
		},
		{
			name:        "lease loss sends retry close",
			cancelCause: ErrOpenAIWSIngressLeaseLost,
			wantStatus:  coderws.StatusTryAgainLater,
			wantReason:  "websocket ingress capacity lease lost; please reconnect",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controlCtx, cancelControl := context.WithCancelCause(context.Background())
			defer cancelControl(context.Canceled)
			serverResult := make(chan error, 1)
			readStarted := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					serverResult <- err
					return
				}
				defer func() { _ = conn.CloseNow() }()
				close(readStarted)
				_, _, err = ReadOpenAIWSClientMessage(
					controlCtx,
					conn,
					tt.timeout,
					tt.timeoutStatus,
					tt.timeoutReason,
				)
				serverResult <- err
			}))
			defer server.Close()

			dialCtx, cancelDial := context.WithTimeout(context.Background(), time.Second)
			clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			cancelDial()
			require.NoError(t, err)
			defer func() { _ = clientConn.CloseNow() }()
			<-readStarted
			if tt.cancelCause != nil {
				cancelControl(tt.cancelCause)
			}

			readCtx, cancelRead := context.WithTimeout(context.Background(), time.Second)
			_, _, err = clientConn.Read(readCtx)
			cancelRead()
			var clientClose coderws.CloseError
			require.ErrorAs(t, err, &clientClose)
			require.Equal(t, tt.wantStatus, clientClose.Code)
			require.Equal(t, tt.wantReason, clientClose.Reason)

			select {
			case serverErr := <-serverResult:
				var closeErr *OpenAIWSClientCloseError
				require.ErrorAs(t, serverErr, &closeErr)
				require.Equal(t, tt.wantStatus, closeErr.StatusCode())
				require.Equal(t, tt.wantReason, closeErr.Reason())
			case <-time.After(time.Second):
				t.Fatal("server read goroutine did not exit after close handshake")
			}
		})
	}
}

func TestReadOpenAIWSClientMessage_ParentCancellationStillJoinsRead(t *testing.T) {
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	serverResult := make(chan error, 1)
	readStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			serverResult <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		close(readStarted)
		_, _, err = ReadOpenAIWSClientMessage(controlCtx, conn, 0, 0, "")
		serverResult <- err
	}))
	defer server.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()
	<-readStarted
	cancelControl(errors.New("server shutting down"))
	readCtx, cancelRead := context.WithTimeout(context.Background(), time.Second)
	_, _, err = clientConn.Read(readCtx)
	cancelRead()
	var clientClose coderws.CloseError
	require.ErrorAs(t, err, &clientClose)
	require.Equal(t, coderws.StatusGoingAway, clientClose.Code)
	require.Equal(t, "websocket request canceled", clientClose.Reason)

	select {
	case <-serverResult:
	case <-time.After(time.Second):
		t.Fatal("server read goroutine leaked after parent cancellation")
	}
}

// CAPYBARA-PATCH: ingress 轮次间 ping 的真实 WebSocket 并发回归。
func TestReadOpenAIWSClientMessage_InterTurnPingKeepsReadingBusinessFrames(t *testing.T) {
	tests := []struct {
		name          string
		pingInterval  time.Duration
		wantPingCount int
	}{
		{name: "disabled preserves old behavior", pingInterval: 0, wantPingCount: 0},
		{name: "enabled survives multiple cycles", pingInterval: 15 * time.Millisecond, wantPingCount: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverResult := make(chan openAIWSClientReadResult, 1)
			readStarted := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					serverResult <- openAIWSClientReadResult{err: err}
					return
				}
				defer func() { _ = conn.CloseNow() }()
				close(readStarted)
				msgType, payload, err := readOpenAIWSClientMessageWithTimeoutStart(
					context.Background(), conn, 500*time.Millisecond,
					coderws.StatusNormalClosure, "websocket idle timeout", nil, nil, tt.pingInterval,
				)
				serverResult <- openAIWSClientReadResult{messageType: msgType, payload: payload, err: err}
			}))
			defer server.Close()

			var pingCount atomic.Int32
			pingSeen := make(chan struct{}, 8)
			dialCtx, cancelDial := context.WithTimeout(context.Background(), time.Second)
			clientConn, _, err := coderws.Dial(
				dialCtx,
				"ws"+strings.TrimPrefix(server.URL, "http"),
				&coderws.DialOptions{OnPingReceived: func(context.Context, []byte) bool {
					pingCount.Add(1)
					select {
					case pingSeen <- struct{}{}:
					default:
					}
					return true
				}},
			)
			cancelDial()
			require.NoError(t, err)
			defer func() { _ = clientConn.CloseNow() }()
			<-readStarted

			clientReadDone := make(chan error, 1)
			go func() {
				_, _, readErr := clientConn.Read(context.Background())
				clientReadDone <- readErr
			}()
			for i := 0; i < tt.wantPingCount; i++ {
				select {
				case <-pingSeen:
				case <-time.After(time.Second):
					t.Fatal("timed out waiting for ingress ping")
				}
			}
			if tt.wantPingCount == 0 {
				time.Sleep(60 * time.Millisecond)
			}

			writeCtx, cancelWrite := context.WithTimeout(context.Background(), time.Second)
			err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create"}`))
			cancelWrite()
			require.NoError(t, err)

			select {
			case result := <-serverResult:
				require.NoError(t, result.err)
				require.Equal(t, coderws.MessageText, result.messageType)
				require.JSONEq(t, `{"type":"response.create"}`, string(result.payload))
			case <-time.After(time.Second):
				t.Fatal("server did not return the business frame")
			}
			require.GreaterOrEqual(t, int(pingCount.Load()), tt.wantPingCount)
			_ = clientConn.CloseNow()
			select {
			case <-clientReadDone:
			case <-time.After(time.Second):
				t.Fatal("client reader did not exit")
			}
		})
	}
}

func TestReadOpenAIWSClientMessage_BusinessFrameCancelsInFlightPing(t *testing.T) {
	serverResult := make(chan openAIWSClientReadResult, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			serverResult <- openAIWSClientReadResult{err: err}
			return
		}
		defer func() { _ = conn.CloseNow() }()
		msgType, payload, err := readOpenAIWSClientMessageWithTimeoutStart(
			context.Background(), conn, 0, 0, "", nil, nil, 10*time.Millisecond,
		)
		serverResult <- openAIWSClientReadResult{messageType: msgType, payload: payload, err: err}
	}))
	defer server.Close()

	var pingCount atomic.Int32
	pingSeen := make(chan struct{}, 1)
	dialCtx, cancelDial := context.WithTimeout(context.Background(), time.Second)
	clientConn, _, err := coderws.Dial(
		dialCtx,
		"ws"+strings.TrimPrefix(server.URL, "http"),
		&coderws.DialOptions{OnPingReceived: func(context.Context, []byte) bool {
			pingCount.Add(1)
			select {
			case pingSeen <- struct{}{}:
			default:
			}
			return false
		}},
	)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	clientReadDone := make(chan error, 1)
	go func() {
		_, _, readErr := clientConn.Read(context.Background())
		clientReadDone <- readErr
	}()
	select {
	case <-pingSeen:
	case <-time.After(time.Second):
		t.Fatal("ping did not start")
	}
	time.Sleep(60 * time.Millisecond)
	require.Equal(t, int32(1), pingCount.Load(), "a blocked ping must not overlap another ping")

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte("next turn"))
	cancelWrite()
	require.NoError(t, err)
	select {
	case result := <-serverResult:
		require.NoError(t, result.err)
		require.Equal(t, []byte("next turn"), result.payload)
	case <-time.After(time.Second):
		t.Fatal("business frame did not cancel and join the in-flight ping")
	}
	_ = clientConn.CloseNow()
	<-clientReadDone
}

func TestReadOpenAIWSClientMessage_SuccessfulPingDoesNotResetIdleTimeout(t *testing.T) {
	serverResult := make(chan error, 1)
	startedAt := time.Now()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			serverResult <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		_, _, err = readOpenAIWSClientMessageWithTimeoutStart(
			context.Background(), conn, 120*time.Millisecond,
			coderws.StatusNormalClosure, "websocket idle timeout", nil, nil, 20*time.Millisecond,
		)
		serverResult <- err
	}))
	defer server.Close()

	var pingCount atomic.Int32
	dialCtx, cancelDial := context.WithTimeout(context.Background(), time.Second)
	clientConn, _, err := coderws.Dial(
		dialCtx,
		"ws"+strings.TrimPrefix(server.URL, "http"),
		&coderws.DialOptions{OnPingReceived: func(context.Context, []byte) bool {
			pingCount.Add(1)
			return true
		}},
	)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	clientReadCtx, cancelClientRead := context.WithTimeout(context.Background(), openAIWSIngressPingTimeout+2*time.Second)
	_, _, err = clientConn.Read(clientReadCtx)
	cancelClientRead()
	var websocketClose coderws.CloseError
	require.ErrorAs(t, err, &websocketClose)
	require.Equal(t, coderws.StatusNormalClosure, websocketClose.Code)
	require.Less(t, time.Since(startedAt), time.Second)
	require.GreaterOrEqual(t, pingCount.Load(), int32(3))

	serverErr := <-serverResult
	var closeErr *OpenAIWSClientCloseError
	require.ErrorAs(t, serverErr, &closeErr)
	require.Equal(t, coderws.StatusNormalClosure, closeErr.StatusCode())
	require.Equal(t, coderws.StatusCode(-1), coderws.CloseStatus(serverErr))
	require.False(t, isOpenAIWSClientDisconnectError(serverErr))
}

func TestReadOpenAIWSClientMessage_IdleTimeoutCancelsInFlightPing(t *testing.T) {
	serverResult := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			serverResult <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		_, _, err = readOpenAIWSClientMessageWithTimeoutStart(
			context.Background(), conn, 100*time.Millisecond,
			coderws.StatusNormalClosure, "websocket idle timeout", nil, nil, 10*time.Millisecond,
		)
		serverResult <- err
	}))
	defer server.Close()

	pingSeen := make(chan struct{}, 1)
	dialCtx, cancelDial := context.WithTimeout(context.Background(), time.Second)
	clientConn, _, err := coderws.Dial(
		dialCtx,
		"ws"+strings.TrimPrefix(server.URL, "http"),
		&coderws.DialOptions{OnPingReceived: func(context.Context, []byte) bool {
			select {
			case pingSeen <- struct{}{}:
			default:
			}
			return false
		}},
	)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	clientReadDone := make(chan error, 1)
	go func() {
		_, _, readErr := clientConn.Read(context.Background())
		clientReadDone <- readErr
	}()
	select {
	case <-pingSeen:
	case <-time.After(time.Second):
		t.Fatal("ping did not start")
	}

	select {
	case serverErr := <-serverResult:
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, serverErr, &closeErr)
		require.Equal(t, coderws.StatusNormalClosure, closeErr.StatusCode())
		require.Equal(t, "websocket idle timeout", closeErr.Reason())
		require.Equal(t, coderws.StatusCode(-1), coderws.CloseStatus(serverErr))
		require.False(t, isOpenAIWSClientDisconnectError(serverErr))
	case <-time.After(time.Second):
		t.Fatal("idle timeout waited for the ping deadline instead of canceling it")
	}
	<-clientReadDone
}

func TestReadOpenAIWSClientMessage_CancelDuringPingPreservesControlCause(t *testing.T) {
	controlCtx, cancelControl := context.WithCancelCause(context.Background())
	cancelCause := errors.New("ingress shutting down")
	serverResult := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			serverResult <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		_, _, err = readOpenAIWSClientMessageWithTimeoutStart(
			controlCtx, conn, 0, 0, "", nil, nil, 10*time.Millisecond,
		)
		serverResult <- err
	}))
	defer server.Close()

	pingSeen := make(chan struct{}, 1)
	dialCtx, cancelDial := context.WithTimeout(context.Background(), time.Second)
	clientConn, _, err := coderws.Dial(
		dialCtx,
		"ws"+strings.TrimPrefix(server.URL, "http"),
		&coderws.DialOptions{OnPingReceived: func(context.Context, []byte) bool {
			pingSeen <- struct{}{}
			return false
		}},
	)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	clientReadDone := make(chan error, 1)
	go func() {
		_, _, readErr := clientConn.Read(context.Background())
		clientReadDone <- readErr
	}()
	select {
	case <-pingSeen:
	case <-time.After(time.Second):
		t.Fatal("ping did not start")
	}
	cancelControl(cancelCause)

	select {
	case serverErr := <-serverResult:
		require.ErrorIs(t, serverErr, cancelCause)
		require.Equal(t, coderws.StatusCode(-1), coderws.CloseStatus(serverErr))
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, serverErr, &closeErr)
		require.Equal(t, "websocket request canceled", closeErr.Reason())
	case <-time.After(time.Second):
		t.Fatal("control cancellation did not cancel and join the ping")
	}
	<-clientReadDone
}

func TestReadOpenAIWSClientMessage_PingTimeoutIsClientDisconnect(t *testing.T) {
	serverResult := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			serverResult <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		_, _, err = readOpenAIWSClientMessageWithTimeoutStart(
			context.Background(), conn, 0, 0, "", nil, nil, 10*time.Millisecond,
		)
		serverResult <- err
	}))
	defer server.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), time.Second)
	clientConn, _, err := coderws.Dial(
		dialCtx,
		"ws"+strings.TrimPrefix(server.URL, "http"),
		&coderws.DialOptions{OnPingReceived: func(context.Context, []byte) bool { return false }},
	)
	cancelDial()
	require.NoError(t, err)
	defer func() { _ = clientConn.CloseNow() }()

	clientReadCtx, cancelClientRead := context.WithTimeout(context.Background(), openAIWSIngressPingTimeout+2*time.Second)
	_, _, err = clientConn.Read(clientReadCtx)
	cancelClientRead()
	var websocketClose coderws.CloseError
	require.ErrorAs(t, err, &websocketClose)
	require.Equal(t, coderws.StatusGoingAway, websocketClose.Code)
	require.Equal(t, openAIWSIngressPingReason, websocketClose.Reason)

	select {
	case serverErr := <-serverResult:
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, serverErr, &closeErr)
		require.Equal(t, coderws.StatusGoingAway, closeErr.StatusCode())
		var pingErr *openAIWSIngressPingDisconnectError
		require.ErrorAs(t, serverErr, &pingErr)
		require.ErrorIs(t, serverErr, context.DeadlineExceeded)
		require.Equal(t, coderws.StatusGoingAway, coderws.CloseStatus(serverErr))
		require.True(t, isOpenAIWSClientDisconnectError(serverErr))
	case <-time.After(openAIWSIngressPingTimeout + 2*time.Second):
		t.Fatal("ping timeout did not close and join the client reader")
	}
}
