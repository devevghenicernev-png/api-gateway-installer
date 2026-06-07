package auth

import "testing"

func TestMatchACL_NilOrEmptyMatch_AllowsAll(t *testing.T) {
	ok, _ := MatchACL(nil, ACLMatchAPIKeyID, "anyone")
	if !ok {
		t.Fatalf("nil ACL: want allowed")
	}
	ok, _ = MatchACL(&ACL{}, ACLMatchAPIKeyID, "anyone")
	if !ok {
		t.Fatalf("empty Match: want allowed")
	}
}

func TestMatchACL_MatchMismatch_NoOp(t *testing.T) {
	cfg := &ACL{Match: ACLMatchSubject, Deny: []string{"alice"}}
	// Caller is APIKey handler, ACL is configured for subject — should be no-op.
	if ok, _ := MatchACL(cfg, ACLMatchAPIKeyID, "alice"); !ok {
		t.Fatalf("mismatched match: want allowed (no-op)")
	}
}

func TestMatchACL_DenyOnly(t *testing.T) {
	cfg := &ACL{Match: ACLMatchAPIKeyID, Deny: []string{"stolen", "rotated"}}
	for _, id := range []string{"stolen", "rotated"} {
		if ok, _ := MatchACL(cfg, ACLMatchAPIKeyID, id); ok {
			t.Errorf("%s: want denied", id)
		}
	}
	if ok, _ := MatchACL(cfg, ACLMatchAPIKeyID, "ci-bot"); !ok {
		t.Errorf("ci-bot: want allowed (not in deny, no allow constraint)")
	}
}

func TestMatchACL_AllowOnly_DefaultDeny(t *testing.T) {
	cfg := &ACL{Match: ACLMatchAPIKeyID, Allow: []string{"ci-bot", "ops"}}
	for _, id := range []string{"ci-bot", "ops"} {
		if ok, _ := MatchACL(cfg, ACLMatchAPIKeyID, id); !ok {
			t.Errorf("%s: want allowed", id)
		}
	}
	for _, id := range []string{"random", "anonymous", ""} {
		if ok, _ := MatchACL(cfg, ACLMatchAPIKeyID, id); ok {
			t.Errorf("%s: want denied (not in allow-list)", id)
		}
	}
}

func TestMatchACL_DenyBeatsAllow(t *testing.T) {
	cfg := &ACL{
		Match: ACLMatchAPIKeyID,
		Allow: []string{"ci-bot", "shared"},
		Deny:  []string{"shared", "rotated"},
	}
	// "shared" is in both → deny wins.
	if ok, why := MatchACL(cfg, ACLMatchAPIKeyID, "shared"); ok {
		t.Errorf("shared: want denied (deny-wins), got allowed (%s)", why)
	}
	// "ci-bot" only in allow → allowed.
	if ok, _ := MatchACL(cfg, ACLMatchAPIKeyID, "ci-bot"); !ok {
		t.Errorf("ci-bot: want allowed")
	}
	// "rotated" only in deny → denied (and Allow is non-empty so it'd also fail).
	if ok, _ := MatchACL(cfg, ACLMatchAPIKeyID, "rotated"); ok {
		t.Errorf("rotated: want denied")
	}
	// "random" in neither, but Allow is non-empty → default deny.
	if ok, _ := MatchACL(cfg, ACLMatchAPIKeyID, "random"); ok {
		t.Errorf("random: want denied (Allow non-empty)")
	}
}

func TestMatchACL_AllMatchTypes(t *testing.T) {
	cases := []struct {
		match string
	}{
		{ACLMatchAPIKeyID},
		{ACLMatchHMACID},
		{ACLMatchSubject},
		{ACLMatchMTLSCN},
	}
	for _, c := range cases {
		cfg := &ACL{Match: c.match, Deny: []string{"baddie"}}
		// Matching wantMatch with a denied identity.
		if ok, _ := MatchACL(cfg, c.match, "baddie"); ok {
			t.Errorf("match=%s baddie: want denied", c.match)
		}
		if ok, _ := MatchACL(cfg, c.match, "goodie"); !ok {
			t.Errorf("match=%s goodie: want allowed", c.match)
		}
	}
}

func TestMatchACL_EmptyIdentity_StillEvaluated(t *testing.T) {
	// An auth method that didn't produce an identity (e.g. JWT with no sub)
	// should fail an Allow-list that doesn't include "".
	cfg := &ACL{Match: ACLMatchSubject, Allow: []string{"alice"}}
	if ok, _ := MatchACL(cfg, ACLMatchSubject, ""); ok {
		t.Fatalf("empty identity under Allow=[alice]: want denied")
	}
	// But under deny-only, empty identity isn't in Deny → pass.
	cfg = &ACL{Match: ACLMatchSubject, Deny: []string{"baddie"}}
	if ok, _ := MatchACL(cfg, ACLMatchSubject, ""); !ok {
		t.Fatalf("empty identity under deny-only: want allowed")
	}
}
