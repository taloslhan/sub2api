package sessionarchive

import (
	"context"
	"errors"
	"time"
)

type PolicyState string
type PolicyScope string

const (
	PolicyInherit PolicyState = "inherit"
	PolicyOn      PolicyState = "on"
	PolicyOff     PolicyState = "off"

	ScopeGlobal PolicyScope = "global"
	ScopeGroup  PolicyScope = "group"
	ScopeUser   PolicyScope = "user"
	ScopeAPIKey PolicyScope = "api_key"
)

type Policy struct {
	ID                     int64       `json:"id"`
	ScopeType              PolicyScope `json:"scope_type"`
	ScopeID                int64       `json:"scope_id"`
	State                  PolicyState `json:"state"`
	CaptureRawRequest      bool        `json:"capture_raw_request"`
	CaptureUpstreamRequest bool        `json:"capture_upstream_request"`
	CaptureResponse        bool        `json:"capture_response"`
	CaptureTools           bool        `json:"capture_tools"`
	CaptureAttachments     bool        `json:"capture_attachments"`
	PayloadMaxBytes        int64       `json:"payload_max_bytes"`
	RetentionDays          int         `json:"retention_days"`
	CreatedAt              time.Time   `json:"created_at"`
	UpdatedAt              time.Time   `json:"updated_at"`
}

type PolicyIdentity struct {
	GroupID  int64
	UserID   int64
	APIKeyID int64
}

type ResolvedPolicy struct {
	Enabled                bool        `json:"enabled"`
	MatchedScope           PolicyScope `json:"matched_scope"`
	MatchedScopeID         int64       `json:"matched_scope_id"`
	CaptureRawRequest      bool        `json:"capture_raw_request"`
	CaptureUpstreamRequest bool        `json:"capture_upstream_request"`
	CaptureResponse        bool        `json:"capture_response"`
	CaptureTools           bool        `json:"capture_tools"`
	CaptureAttachments     bool        `json:"capture_attachments"`
	PayloadMaxBytes        int64       `json:"payload_max_bytes"`
	RetentionDays          int         `json:"retention_days"`
}

func DefaultResolvedPolicy(payloadMaxBytes int64, retentionDays int) ResolvedPolicy {
	return ResolvedPolicy{
		MatchedScope:       ScopeGlobal,
		CaptureRawRequest:  true,
		CaptureResponse:    true,
		CaptureTools:       true,
		CaptureAttachments: true,
		PayloadMaxBytes:    payloadMaxBytes,
		RetentionDays:      retentionDays,
	}
}

func ResolvePolicy(identity PolicyIdentity, policies []Policy, fallback ResolvedPolicy) ResolvedPolicy {
	byScope := make(map[PolicyScope]Policy, len(policies))
	for _, policy := range policies {
		switch policy.ScopeType {
		case ScopeAPIKey:
			if identity.APIKeyID > 0 && policy.ScopeID == identity.APIKeyID {
				byScope[ScopeAPIKey] = policy
			}
		case ScopeUser:
			if identity.UserID > 0 && policy.ScopeID == identity.UserID {
				byScope[ScopeUser] = policy
			}
		case ScopeGroup:
			if identity.GroupID > 0 && policy.ScopeID == identity.GroupID {
				byScope[ScopeGroup] = policy
			}
		case ScopeGlobal:
			if policy.ScopeID == 0 {
				byScope[ScopeGlobal] = policy
			}
		}
	}
	for _, scope := range []PolicyScope{ScopeAPIKey, ScopeUser, ScopeGroup, ScopeGlobal} {
		policy, ok := byScope[scope]
		if !ok || policy.State == PolicyInherit {
			continue
		}
		return ResolvedPolicy{
			Enabled:                policy.State == PolicyOn,
			MatchedScope:           policy.ScopeType,
			MatchedScopeID:         policy.ScopeID,
			CaptureRawRequest:      policy.CaptureRawRequest,
			CaptureUpstreamRequest: policy.CaptureUpstreamRequest,
			CaptureResponse:        policy.CaptureResponse,
			CaptureTools:           policy.CaptureTools,
			CaptureAttachments:     policy.CaptureAttachments,
			PayloadMaxBytes:        policy.PayloadMaxBytes,
			RetentionDays:          policy.RetentionDays,
		}
	}
	// 未命中显式策略时必须 fail-closed，不能沿用一个启用的调用方 fallback。
	fallback.Enabled = false
	fallback.MatchedScope = ScopeGlobal
	fallback.MatchedScopeID = 0
	return fallback
}

type PolicyStore interface {
	PoliciesFor(context.Context, PolicyIdentity) ([]Policy, error)
}

type PolicyResolver struct {
	store    PolicyStore
	fallback ResolvedPolicy
	onError  func()
}

func NewPolicyResolver(store PolicyStore, fallback ResolvedPolicy, onError func()) *PolicyResolver {
	return &PolicyResolver{store: store, fallback: fallback, onError: onError}
}

func (r *PolicyResolver) Resolve(ctx context.Context, identity PolicyIdentity) ResolvedPolicy {
	if r == nil {
		return ResolvedPolicy{MatchedScope: ScopeGlobal}
	}
	if r.store == nil {
		return ResolvePolicy(identity, nil, r.fallback)
	}
	policies, err := r.store.PoliciesFor(ctx, identity)
	if err != nil {
		if r.onError != nil {
			r.onError()
		}
		return ResolvePolicy(identity, nil, r.fallback)
	}
	return ResolvePolicy(identity, policies, r.fallback)
}

func ValidatePolicy(policy Policy, maxPayloadBytes int64) error {
	if policy.State != PolicyInherit && policy.State != PolicyOn && policy.State != PolicyOff {
		return errors.New("invalid policy state")
	}
	if policy.PayloadMaxBytes < 1 || policy.PayloadMaxBytes > maxPayloadBytes {
		return errors.New("invalid policy payload limit")
	}
	if policy.RetentionDays < 1 || policy.RetentionDays > 3650 {
		return errors.New("invalid policy retention days")
	}
	return nil
}
