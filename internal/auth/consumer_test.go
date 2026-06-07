package auth

import "testing"

func TestResolveConsumer_Hit(t *testing.T) {
	reg := []Consumer{
		{ID: "alice", Groups: []string{"ops"}},
		{ID: "bob", Groups: []string{"dev"}},
	}
	got, ok := ResolveConsumer("bob", reg)
	if !ok {
		t.Fatalf("expected hit")
	}
	if got.ID != "bob" || got.Groups[0] != "dev" {
		t.Fatalf("wrong row: %+v", got)
	}
}

func TestResolveConsumer_Miss(t *testing.T) {
	if _, ok := ResolveConsumer("nobody", []Consumer{{ID: "alice"}}); ok {
		t.Fatalf("want miss")
	}
}

func TestResolveConsumer_EmptyID(t *testing.T) {
	if _, ok := ResolveConsumer("", []Consumer{{ID: ""}}); ok {
		t.Fatalf("empty ID should never resolve (avoids matching unset entries)")
	}
}

func TestResolveConsumer_EmptyRegistry(t *testing.T) {
	if _, ok := ResolveConsumer("alice", nil); ok {
		t.Fatalf("nil registry: want miss")
	}
}

func TestCredentialConsumerID_FallbackToID(t *testing.T) {
	if got := CredentialConsumerID("key-1", ""); got != "key-1" {
		t.Fatalf("empty override: got %q want key-1", got)
	}
}

func TestCredentialConsumerID_OverrideWins(t *testing.T) {
	if got := CredentialConsumerID("key-1", "ci-team"); got != "ci-team" {
		t.Fatalf("override: got %q want ci-team", got)
	}
}

// ---------- MatchACLAny ----------

func TestMatchACLAny_Allow(t *testing.T) {
	cfg := &ACL{Match: ACLMatchConsumerGroup, Allow: []string{"ops", "ci"}}
	if ok, _ := MatchACLAny(cfg, ACLMatchConsumerGroup, []string{"ops"}); !ok {
		t.Errorf("ops in allow: want allowed")
	}
	if ok, _ := MatchACLAny(cfg, ACLMatchConsumerGroup, []string{"ci", "internal"}); !ok {
		t.Errorf("ci-or-internal in allow: want allowed (intersection non-empty)")
	}
	if ok, _ := MatchACLAny(cfg, ACLMatchConsumerGroup, []string{"random"}); ok {
		t.Errorf("random: want denied")
	}
	if ok, _ := MatchACLAny(cfg, ACLMatchConsumerGroup, nil); ok {
		t.Errorf("empty identity set: want denied (Allow non-empty)")
	}
}

func TestMatchACLAny_Deny(t *testing.T) {
	cfg := &ACL{Match: ACLMatchConsumerGroup, Deny: []string{"blocked"}}
	if ok, _ := MatchACLAny(cfg, ACLMatchConsumerGroup, []string{"blocked"}); ok {
		t.Errorf("blocked: want denied")
	}
	if ok, _ := MatchACLAny(cfg, ACLMatchConsumerGroup, []string{"ops", "blocked"}); ok {
		t.Errorf("blocked among groups: want denied (any-match)")
	}
	if ok, _ := MatchACLAny(cfg, ACLMatchConsumerGroup, []string{"ops"}); !ok {
		t.Errorf("ops only, not in deny: want allowed")
	}
}

func TestMatchACLAny_DenyBeatsAllow(t *testing.T) {
	cfg := &ACL{
		Match: ACLMatchConsumerGroup,
		Allow: []string{"shared", "ops"},
		Deny:  []string{"shared", "rotated"},
	}
	// "shared" in both → deny wins.
	if ok, _ := MatchACLAny(cfg, ACLMatchConsumerGroup, []string{"shared"}); ok {
		t.Errorf("shared: want denied (deny-wins)")
	}
	// ops only → allowed.
	if ok, _ := MatchACLAny(cfg, ACLMatchConsumerGroup, []string{"ops"}); !ok {
		t.Errorf("ops: want allowed")
	}
}

func TestMatchACLAny_MismatchedMatch_NoOp(t *testing.T) {
	cfg := &ACL{Match: ACLMatchAPIKeyID, Deny: []string{"x"}}
	// Caller passes consumer-group, ACL is scalar-id → no-op, allowed.
	if ok, _ := MatchACLAny(cfg, ACLMatchConsumerGroup, []string{"x"}); !ok {
		t.Errorf("mismatch: want allowed (no-op)")
	}
}

func TestMatchACLAny_NilOrEmpty(t *testing.T) {
	if ok, _ := MatchACLAny(nil, ACLMatchConsumerGroup, []string{"x"}); !ok {
		t.Errorf("nil cfg: want allowed")
	}
	if ok, _ := MatchACLAny(&ACL{}, ACLMatchConsumerGroup, []string{"x"}); !ok {
		t.Errorf("empty Match: want allowed")
	}
}

func TestACLMatchConsumerIDConstant(t *testing.T) {
	if ACLMatchConsumerID != "consumer-id" {
		t.Errorf("wire format drifted: %s", ACLMatchConsumerID)
	}
	if ACLMatchConsumerGroup != "consumer-group" {
		t.Errorf("wire format drifted: %s", ACLMatchConsumerGroup)
	}
}
