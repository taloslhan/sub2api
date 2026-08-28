package sessionarchive

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestRedisTicketStoreConsumesAtomically(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	store, err := NewRedisTicketStore(client, "archive:ticket:")
	require.NoError(t, err)
	ticket := ExportTicket{ID: "random-capability", AdminID: 42, Format: "archive", ExpiresAt: time.Now().Add(time.Minute)}
	require.NoError(t, store.Put(context.Background(), ticket, time.Minute))
	got, err := store.Consume(context.Background(), ticket.ID)
	require.NoError(t, err)
	require.Equal(t, ticket.ID, got.ID)
	_, err = store.Consume(context.Background(), ticket.ID)
	require.ErrorContains(t, err, "already consumed")
}

type fakeTicketStore struct {
	ticket   ExportTicket
	consumed int
}

func (s *fakeTicketStore) Put(context.Context, ExportTicket, time.Duration) error { return nil }
func (s *fakeTicketStore) Consume(context.Context, string) (ExportTicket, error) {
	s.consumed++
	if s.consumed > 1 {
		return ExportTicket{}, errors.New("replayed")
	}
	return s.ticket, nil
}

func TestDownloadRequiresLimiterAndDoesNotAuditTicketCapability(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &Service{metrics: &serviceMetrics{}}
	store := &fakeTicketStore{ticket: ExportTicket{ID: "secret-ticket", AdminID: 7, Format: "archive", ExpiresAt: time.Now().Add(time.Minute)}}
	var auditTarget string
	handler, err := NewHandler(HandlerOptions{
		Service: service, Tickets: store, DownloadLimiter: func(*gin.Context) bool { return true },
		RequiredAudit: func(_ context.Context, _, target string, _ map[string]any) error {
			auditTarget = target
			return nil
		},
	})
	require.NoError(t, err)
	router := gin.New()
	router.GET("/session-archive/download/:ticket", handler.Download)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/session-archive/download/secret-ticket", nil)
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "admin:7", auditTarget)
	require.NotContains(t, auditTarget, "secret-ticket")

	blockedStore := &fakeTicketStore{ticket: store.ticket}
	blocked, err := NewHandler(HandlerOptions{Service: service, Tickets: blockedStore, RequiredAudit: func(context.Context, string, string, map[string]any) error { return nil }})
	require.NoError(t, err)
	router = gin.New()
	router.GET("/session-archive/download/:ticket", blocked.Download)
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusTooManyRequests, recorder.Code)
	require.Zero(t, blockedStore.consumed, "rate limiting must run before ticket consumption")
}
