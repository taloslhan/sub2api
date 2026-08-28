package sessionarchive

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testPolicy(scope PolicyScope, id int64, state PolicyState) Policy {
	return Policy{ScopeType: scope, ScopeID: id, State: state, CaptureRawRequest: true, CaptureResponse: true, PayloadMaxBytes: 1024, RetentionDays: 30}
}

func TestResolvePolicyPrecedenceAndDefaultClosed(t *testing.T) {
	fallback := DefaultResolvedPolicy(2048, 30)
	identity := PolicyIdentity{GroupID: 1, UserID: 2, APIKeyID: 3}
	policies := []Policy{testPolicy(ScopeGlobal, 0, PolicyOn), testPolicy(ScopeGroup, 1, PolicyOff), testPolicy(ScopeUser, 2, PolicyOn), testPolicy(ScopeAPIKey, 3, PolicyInherit)}
	resolved := ResolvePolicy(identity, policies, fallback)
	require.True(t, resolved.Enabled)
	require.Equal(t, ScopeUser, resolved.MatchedScope)

	policies = append(policies, testPolicy(ScopeAPIKey, 3, PolicyOff))
	resolved = ResolvePolicy(identity, policies, fallback)
	require.False(t, resolved.Enabled)
	require.Equal(t, ScopeAPIKey, resolved.MatchedScope)
	require.False(t, ResolvePolicy(identity, nil, fallback).Enabled)
}

func TestPolicyResolverNilAndStoreFailureFailClosed(t *testing.T) {
	var nilResolver *PolicyResolver
	require.False(t, nilResolver.Resolve(context.Background(), PolicyIdentity{}).Enabled)
	called := 0
	resolver := NewPolicyResolver(failingPolicyStore{}, DefaultResolvedPolicy(1024, 30), func() { called++ })
	require.False(t, resolver.Resolve(context.Background(), PolicyIdentity{}).Enabled)
	require.Equal(t, 1, called)
}

type failingPolicyStore struct{}

func (failingPolicyStore) PoliciesFor(context.Context, PolicyIdentity) ([]Policy, error) {
	return nil, context.Canceled
}

func TestChooseMergeCandidate(t *testing.T) {
	now := time.Now()
	identity := MergeIdentity{TenantID: 1, UserID: 2, APIKeyID: 3, Protocol: "responses"}
	other := identity
	other.APIKeyID = 4
	candidates := []MergeCandidate{
		{SessionID: 10, Identity: identity, StableIDDigest: "stable", MessageChain: []string{"a"}, LastActiveAt: now.Add(-24 * time.Hour)},
		{SessionID: 20, Identity: other, MessageChain: []string{"a"}, LastActiveAt: now},
	}
	require.Equal(t, MergeDecision{SessionID: 10, Method: "stable_id", Matched: true}, ChooseMergeCandidate(identity, "stable", []string{"new"}, now, 5*time.Minute, candidates))
	require.Equal(t, MergeDecision{SessionID: 10, Method: "derived_prefix", Matched: true}, ChooseMergeCandidate(identity, "", []string{"a", "b"}, now, 48*time.Hour, candidates))
	candidates[0].LastActiveAt = now
	candidates = append(candidates, MergeCandidate{SessionID: 11, Identity: identity, MessageChain: []string{"a"}, LastActiveAt: now})
	require.Equal(t, "ambiguous", ChooseMergeCandidate(identity, "", []string{"a", "b"}, now, 5*time.Minute, candidates).Method)
	require.Equal(t, "derived_new", ChooseMergeCandidate(identity, "", []string{"different"}, now, 5*time.Minute, candidates).Method)
}

func TestStableIDDigestUsesPersistentHMAC(t *testing.T) {
	keyA := []byte("0123456789abcdef0123456789abcdef")
	keyB := []byte("abcdef0123456789abcdef0123456789")
	a := DigestStableID(keyA, "thread_123")
	require.Len(t, a, 64)
	require.Equal(t, a, DigestStableID(keyA, "thread_123"))
	require.NotEqual(t, a, DigestStableID(keyB, "thread_123"))
}

func TestHashMessageChainItemsDoesNotRetainPlaintext(t *testing.T) {
	hashes := HashMessageChainItems([]string{"secret prompt", "assistant answer"})
	require.Len(t, hashes, 2)
	require.NotContains(t, hashes[0], "secret")
	require.Len(t, hashes[0], 64)
}
