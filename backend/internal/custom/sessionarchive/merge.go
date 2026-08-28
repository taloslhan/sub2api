package sessionarchive

import "time"

type MergeIdentity struct {
	TenantID int64
	UserID   int64
	APIKeyID int64
	Protocol string
}

type MergeCandidate struct {
	SessionID      int64
	Identity       MergeIdentity
	StableIDDigest string
	MessageChain   []string
	LastActiveAt   time.Time
}

type MergeDecision struct {
	SessionID int64
	Method    string
	Matched   bool
}

// ChooseMergeCandidate 优先采用稳定标识；否则只接受同隔离域、时间窗内唯一的严格前缀延伸。
func ChooseMergeCandidate(identity MergeIdentity, stableIDDigest string, incoming []string, now time.Time, window time.Duration, candidates []MergeCandidate) MergeDecision {
	if stableIDDigest != "" {
		for _, candidate := range candidates {
			if candidate.Identity == identity && candidate.StableIDDigest == stableIDDigest {
				return MergeDecision{SessionID: candidate.SessionID, Method: "stable_id", Matched: true}
			}
		}
		return MergeDecision{Method: "stable_new"}
	}

	var match int64
	count := 0
	for _, candidate := range candidates {
		if candidate.Identity != identity || now.Sub(candidate.LastActiveAt) < 0 || now.Sub(candidate.LastActiveAt) > window {
			continue
		}
		if len(candidate.MessageChain) >= len(incoming) {
			continue
		}
		prefix := true
		for i := range candidate.MessageChain {
			if candidate.MessageChain[i] != incoming[i] {
				prefix = false
				break
			}
		}
		if prefix {
			match = candidate.SessionID
			count++
		}
	}
	if count == 1 {
		return MergeDecision{SessionID: match, Method: "derived_prefix", Matched: true}
	}
	if count > 1 {
		return MergeDecision{Method: "ambiguous"}
	}
	return MergeDecision{Method: "derived_new"}
}
