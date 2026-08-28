package sessionarchive

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

type EventKind string

const (
	EventTurnAccepted EventKind = "turn_accepted"
	EventRawRequest   EventKind = "raw_request"
	EventAttempt      EventKind = "attempt"
	EventResponse     EventKind = "response"
	EventTool         EventKind = "tool"
	EventAttachment   EventKind = "attachment"
	EventTerminal     EventKind = "terminal"
)

type BlobPurpose string

const (
	PurposeRawRequest      BlobPurpose = "raw_request"
	PurposeUpstreamRequest BlobPurpose = "upstream_request"
	PurposeResponse        BlobPurpose = "response_events"
	PurposeErrorBody       BlobPurpose = "error_body"
	PurposeTool            BlobPurpose = "tool"
	PurposeAttachment      BlobPurpose = "inline_attachment"
)

type CaptureMeta struct {
	Kind                   EventKind
	Purpose                BlobPurpose
	OccurredAt             time.Time
	TenantID               int64
	UserID                 int64
	APIKeyID               int64
	GroupID                int64
	Protocol               string
	Client                 string
	Endpoint               string
	Model                  string
	StableSessionID        string
	ProtocolTurnID         string
	CorrelationRequestID   string
	BillingRequestID       string
	ClientRequestID        string
	UpstreamRequestID      string
	AttemptNo              int
	TurnSequenceNo         int
	SequenceNo             int64
	AccountID              int64
	TransformType          string
	Direction              string
	ContentType            string
	Status                 string
	ErrorClass             string
	ErrorCode              string
	ClientDisconnected     bool
	FinalAttempt           bool
	Duration               time.Duration
	UpstreamStatus         int
	CaptureCoverage        string
	NormalizedMessageChain []string
	Policy                 ResolvedPolicy
	// Metadata 必须已经过 AllowMetadata 过滤；Collector 会再次复制，避免调用方后续修改。
	Metadata map[string]string
}

type Observation struct {
	StoredPayload  []byte
	ObservedSHA256 string
	StoredSHA256   string
	ObservedBytes  int64
	StoredBytes    int64
	Truncated      bool
	DroppedReason  string
}

type CaptureEvent struct {
	Meta        CaptureMeta
	Observation Observation
	permitBytes int64
}

type CaptureResult struct {
	Accepted      bool
	Truncated     bool
	DroppedReason string
	ObservedBytes int64
	StoredBytes   int64
}

type Session struct {
	ID              int64           `json:"id"`
	TenantID        int64           `json:"tenant_id"`
	UserID          int64           `json:"user_id,omitempty"`
	APIKeyID        int64           `json:"api_key_id,omitempty"`
	GroupID         int64           `json:"group_id,omitempty"`
	Protocol        string          `json:"protocol"`
	Client          string          `json:"client"`
	FirstModel      string          `json:"first_model"`
	LastModel       string          `json:"last_model"`
	Status          string          `json:"status"`
	CaptureCoverage string          `json:"capture_coverage"`
	MergeMethod     string          `json:"merge_method"`
	Policy          json.RawMessage `json:"policy_snapshot"`
	CreatedAt       time.Time       `json:"created_at"`
	LastActiveAt    time.Time       `json:"last_active_at"`
	ExpiresAt       time.Time       `json:"expires_at"`
	HasTruncated    bool            `json:"has_truncated"`
}

type SessionSummary struct {
	ID              int64      `json:"id"`
	UserID          *int64     `json:"user_id,omitempty"`
	Username        string     `json:"username,omitempty"`
	UserEmail       string     `json:"user_email,omitempty"`
	APIKeyID        *int64     `json:"api_key_id,omitempty"`
	APIKeyName      string     `json:"api_key_name,omitempty"`
	GroupID         *int64     `json:"group_id,omitempty"`
	GroupName       string     `json:"group_name,omitempty"`
	Protocol        string     `json:"protocol"`
	Client          string     `json:"client,omitempty"`
	FirstModel      string     `json:"first_model,omitempty"`
	LastModel       string     `json:"last_model,omitempty"`
	Status          string     `json:"status"`
	CaptureCoverage string     `json:"capture_coverage,omitempty"`
	MergeMethod     string     `json:"merge_method,omitempty"`
	TurnCount       int64      `json:"turn_count"`
	RequestCount    int64      `json:"request_count"`
	HasTruncated    bool       `json:"has_truncated"`
	CreatedAt       time.Time  `json:"created_at"`
	LastActivityAt  time.Time  `json:"last_activity_at"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
}

type SessionPage struct {
	Items    []SessionSummary `json:"items"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"page_size"`
	Pages    int64            `json:"pages"`
}

type Turn struct {
	ID                 int64      `json:"id"`
	SessionID          int64      `json:"session_id"`
	SequenceNo         int        `json:"sequence_no"`
	ProtocolTurnID     string     `json:"protocol_turn_id"`
	MessageChainDigest string     `json:"message_chain_digest"`
	Status             string     `json:"status"`
	StartedAt          time.Time  `json:"started_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}

type Request struct {
	ID                   int64             `json:"id"`
	TurnID               int64             `json:"turn_id"`
	CorrelationRequestID string            `json:"correlation_request_id"`
	BillingRequestID     string            `json:"billing_request_id"`
	ClientRequestID      string            `json:"client_request_id"`
	UpstreamRequestID    string            `json:"upstream_request_id"`
	Endpoint             string            `json:"endpoint"`
	Model                string            `json:"model"`
	Status               string            `json:"status"`
	ErrorClass           string            `json:"error_class"`
	ErrorCode            string            `json:"error_code"`
	ClientDisconnected   bool              `json:"client_disconnected"`
	HasTruncated         bool              `json:"has_truncated"`
	Metadata             map[string]string `json:"metadata,omitempty"`
	StartedAt            time.Time         `json:"started_at"`
	CompletedAt          *time.Time        `json:"completed_at,omitempty"`
}

type Attempt struct {
	ID                int64      `json:"id"`
	RequestID         int64      `json:"request_id"`
	AttemptNo         int        `json:"attempt_no"`
	AccountID         int64      `json:"account_id,omitempty"`
	TransformType     string     `json:"transform_type"`
	UpstreamRequestID string     `json:"upstream_request_id"`
	UpstreamStatus    int        `json:"upstream_status,omitempty"`
	Status            string     `json:"status"`
	ErrorClass        string     `json:"error_class"`
	ErrorCode         string     `json:"error_code"`
	DurationMS        int64      `json:"duration_ms"`
	Final             bool       `json:"is_final"`
	StartedAt         time.Time  `json:"started_at"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
}

type BlobRef struct {
	ID                int64       `json:"id"`
	OwnerType         string      `json:"owner_type"`
	OwnerID           int64       `json:"owner_id"`
	Purpose           BlobPurpose `json:"purpose"`
	Direction         string      `json:"direction,omitempty"`
	ContentType       string      `json:"content_type"`
	ObservedSHA256    string      `json:"observed_sha256,omitempty"`
	ObservedBytes     int64       `json:"observed_bytes"`
	StoredBytes       int64       `json:"stored_bytes"`
	Truncated         bool        `json:"truncated"`
	DroppedReason     string      `json:"dropped_reason,omitempty"`
	SequenceNo        int64       `json:"sequence_no"`
	OccurredAt        time.Time   `json:"occurred_at"`
	Available         bool        `json:"available"`
	StorageBackend    string      `json:"storage_backend,omitempty"`
	UnavailableReason string      `json:"unavailable_reason,omitempty"`
}

type SessionDetail struct {
	Session  Session   `json:"session"`
	Turns    []Turn    `json:"turns"`
	Requests []Request `json:"requests"`
	Attempts []Attempt `json:"attempts"`
	BlobRefs []BlobRef `json:"blob_refs"`
}

type SessionFilter struct {
	TenantID             int64     `json:"tenant_id"`
	CorrelationRequestID string    `json:"correlation_request_id,omitempty"`
	UserID               int64     `json:"user_id,omitempty"`
	APIKeyID             int64     `json:"api_key_id,omitempty"`
	GroupID              int64     `json:"group_id,omitempty"`
	Model                string    `json:"model,omitempty"`
	Client               string    `json:"client,omitempty"`
	Status               string    `json:"status,omitempty"`
	Protocol             string    `json:"protocol,omitempty"`
	From                 time.Time `json:"from,omitempty"`
	To                   time.Time `json:"to,omitempty"`
	Limit                int       `json:"limit,omitempty"`
	BeforeID             int64     `json:"before_id,omitempty"`
}

func DigestStableID(key []byte, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

// HashMessageChainItems 将规范化消息逐项散列，PostgreSQL 只保存顺序摘要而不保存 prompt 文本。
func HashMessageChainItems(chain []string) []string {
	hashes := make([]string, len(chain))
	for i, item := range chain {
		sum := sha256.Sum256([]byte(item))
		hashes[i] = hex.EncodeToString(sum[:])
	}
	return hashes
}

func DigestMessageChain(chain []string) string {
	h := sha256.New()
	for _, item := range chain {
		_, _ = h.Write([]byte(item))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
