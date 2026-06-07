// Consumer registry — maps the per-credential ID resolved by an auth
// method to a Consumer with an optional set of Groups. The dashboard
// handler surfaces both via X-Apigw-Consumer-Id and
// X-Apigw-Consumer-Groups headers, and ACLs with match=consumer-id /
// consumer-group filter against the resolved identity.
//
// Why decouple "credential ID" from "consumer ID"? In real
// deployments a single team rotates its API keys — what stays
// stable is the consumer ("ci-team"), not the key. Credentials get
// a ConsumerID pointing at the consumer; the consumer points at the
// groups; group-level policy applies uniformly across the team's
// rotated keys.
package auth

// Consumer mirrors config.Consumer so internal/auth doesn't import
// internal/config (the existing pattern — see APIKeyConfig,
// HMACConfig, MTLSConfig).
type Consumer struct {
	ID     string
	Name   string
	Groups []string
}

// ResolveConsumer looks up a consumer by ID in the registry. Returns
// (nil, false) when not found — the caller can decide to still
// surface the bare consumer-id header without group membership, or
// fail closed.
func ResolveConsumer(id string, registry []Consumer) (*Consumer, bool) {
	if id == "" {
		return nil, false
	}
	for i := range registry {
		if registry[i].ID == id {
			return &registry[i], true
		}
	}
	return nil, false
}

// CredentialConsumerID picks the consumer ID for an authenticated
// credential. Empty `consumerOverride` falls back to `credentialID`
// (the 1:1 mapping default — most credentials don't bother setting
// ConsumerID).
func CredentialConsumerID(credentialID, consumerOverride string) string {
	if consumerOverride != "" {
		return consumerOverride
	}
	return credentialID
}
