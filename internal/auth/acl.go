// Per-route ACL gate, executed AFTER authentication succeeds.
//
// Why not nginx-level `if` blocks? The auth identity is set during
// nginx's access phase via auth_request_set, but `if` is evaluated in
// the rewrite phase — too early. Folding the check into the dashboard
// auth handlers (which already know the matched identity) is the
// simpler, race-free fix.
package auth

const (
	ACLMatchAPIKeyID      = "apikey-id"
	ACLMatchHMACID        = "hmac-id"
	ACLMatchSubject       = "subject"
	ACLMatchMTLSCN        = "mtls-cn"
	ACLMatchConsumerID    = "consumer-id"
	ACLMatchConsumerGroup = "consumer-group"
)

// ACL mirrors config.ACL. Kept here so internal/auth doesn't import
// internal/config (cycle).
type ACL struct {
	Match string
	Allow []string
	Deny  []string
}

// MatchACL reports whether an identity is allowed by the rule.
//
//   - cfg nil OR cfg.Match != wantMatch → no-op, allowed=true.
//   - identity in Deny → blocked (deny wins).
//   - Allow non-empty: identity must be listed.
//   - Allow empty + identity not in Deny → allowed.
//
// The second return is a short reason for logging/debugging.
func MatchACL(cfg *ACL, wantMatch, identity string) (bool, string) {
	if cfg == nil || cfg.Match == "" {
		return true, "no acl"
	}
	if cfg.Match != wantMatch {
		// ACL was configured for a different auth method on this API —
		// silently no-op for the current handler.
		return true, "acl match=" + cfg.Match + " skipped for " + wantMatch
	}
	for _, d := range cfg.Deny {
		if d == identity {
			return false, "denied: " + identity
		}
	}
	if len(cfg.Allow) == 0 {
		return true, "no allow constraint"
	}
	for _, a := range cfg.Allow {
		if a == identity {
			return true, "allowed: " + identity
		}
	}
	return false, "not in allow-list: " + identity
}

// MatchACLAny is the set-semantics version of MatchACL: the caller's
// "identity" is a SET of values (e.g. the consumer's groups), and the
// rule's Allow/Deny lists are matched by intersection.
//
//   - cfg nil OR cfg.Match != wantMatch → no-op, allowed=true.
//   - ANY element of identities appears in Deny → blocked (deny wins).
//   - Allow non-empty: at least one element of identities must appear
//     in Allow.
//   - Allow empty + no Deny hit → allowed.
//
// Used for ACLMatchConsumerGroup — a request's consumer may belong to
// multiple groups; matching is "intersect with Allow / Deny".
func MatchACLAny(cfg *ACL, wantMatch string, identities []string) (bool, string) {
	if cfg == nil || cfg.Match == "" {
		return true, "no acl"
	}
	if cfg.Match != wantMatch {
		return true, "acl match=" + cfg.Match + " skipped for " + wantMatch
	}
	for _, d := range cfg.Deny {
		for _, id := range identities {
			if d == id {
				return false, "denied via group: " + id
			}
		}
	}
	if len(cfg.Allow) == 0 {
		return true, "no allow constraint"
	}
	for _, a := range cfg.Allow {
		for _, id := range identities {
			if a == id {
				return true, "allowed via group: " + id
			}
		}
	}
	return false, "no identity in allow-list"
}
