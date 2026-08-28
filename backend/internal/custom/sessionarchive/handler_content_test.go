package sessionarchive

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGetRequestContentAuditFailurePreventsRepositoryAndStoreAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repository, err := NewRepository(db)
	require.NoError(t, err)
	handler, err := NewHandler(HandlerOptions{
		Service: &Service{repository: repository, metrics: &serviceMetrics{}},
		RequiredAudit: func(context.Context, string, string, map[string]any) error {
			return errors.New("audit unavailable")
		},
	})
	require.NoError(t, err)
	router := gin.New()
	router.GET("/requests/:id/content", handler.GetRequestContent)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/requests/91/content?kind=response", nil)

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.NoError(t, mock.ExpectationsWereMet(), "audit failure must happen before the repository or blob store is touched")
}

func TestAttachmentContentKindAndBinaryTransportEncoding(t *testing.T) {
	require.Equal(t, "attachment", contentKind(PurposeAttachment))
	pngHeader := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

	encoded, encoding := encodeContentForTransport("image/png", pngHeader)

	require.Equal(t, "base64", encoding)
	require.Equal(t, base64.StdEncoding.EncodeToString(pngHeader), encoded)
}

func TestContentPartPayloadKeepsEachReferenceAndEncodingIndependent(t *testing.T) {
	occurredAt := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	jsonPart := contentPartPayload(ContentRecord{Ref: BlobRef{
		ID: 41, OwnerType: "attempt", OwnerID: 9, SequenceNo: 2,
		Direction: "client_to_upstream", ContentType: "application/json",
		ObservedBytes: 11, StoredBytes: 11, Available: true, OccurredAt: occurredAt,
	}}, []byte(`{"attempt":2}`))
	binaryPart := contentPartPayload(ContentRecord{Ref: BlobRef{
		ID: 42, OwnerType: "request", OwnerID: 7, SequenceNo: 3,
		Direction: "client_to_gateway", ContentType: "image/png",
		ObservedBytes: 4, StoredBytes: 4, Truncated: true, Available: true, OccurredAt: occurredAt,
	}}, []byte{0x89, 'P', 'N', 'G'})

	require.Equal(t, int64(41), jsonPart["ref_id"])
	require.Equal(t, "attempt", jsonPart["owner_type"])
	require.Equal(t, int64(2), jsonPart["sequence_no"])
	require.Equal(t, map[string]any{"attempt": float64(2)}, jsonPart["value"])
	require.NotContains(t, jsonPart, "base64")

	require.Equal(t, int64(42), binaryPart["ref_id"])
	require.Equal(t, "base64", binaryPart["encoding"])
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G'}), binaryPart["base64"])
	require.Equal(t, true, binaryPart["truncated"])
}

func TestTextualContentIsNotBase64Encoded(t *testing.T) {
	for _, testCase := range []struct {
		contentType string
		content     string
	}{
		{contentType: "application/json; charset=utf-8", content: `{"ok":true}`},
		{contentType: "text/event-stream", content: "data: done\n\n"},
		{contentType: "", content: "plain utf-8"},
	} {
		encoded, encoding := encodeContentForTransport(testCase.contentType, []byte(testCase.content))
		require.Empty(t, encoding)
		require.Equal(t, testCase.content, encoded)
	}
}
