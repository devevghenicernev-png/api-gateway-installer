package webhook

import "testing"

func TestVerifyAny_GitHubAndBitbucket(t *testing.T) {
	secret := []byte("topsecret")
	body := []byte(`{"event":"push"}`)
	header := Sign(secret, body) // sha256=<hex>
	for _, sch := range []SignatureScheme{SchemeGitHub, SchemeBitbucket, ""} {
		if !VerifyAny(sch, secret, body, header) {
			t.Errorf("scheme=%s should verify github-style header", sch)
		}
	}
	if VerifyAny(SchemeGitHub, secret, body, "sha256=deadbeef") {
		t.Errorf("github-style should reject wrong sig")
	}
}

func TestVerifyAny_GitLab(t *testing.T) {
	secret := []byte("token-from-gitlab")
	body := []byte(`{}`)
	if !VerifyAny(SchemeGitLab, secret, body, "token-from-gitlab") {
		t.Errorf("gitlab: identical token should pass")
	}
	if VerifyAny(SchemeGitLab, secret, body, "other") {
		t.Errorf("gitlab: different token should fail")
	}
}

func TestVerifyAny_GenericHMAC(t *testing.T) {
	secret := []byte("k")
	body := []byte("payload")
	header := SignWith(SchemeGenericHMAC, secret, body)
	if !VerifyAny(SchemeGenericHMAC, secret, body, header) {
		t.Errorf("generic: round-trip should verify")
	}
	if VerifyAny(SchemeGenericHMAC, secret, body, "not-hex") {
		t.Errorf("generic: non-hex should fail")
	}
	if VerifyAny(SchemeGenericHMAC, secret, body, "deadbeef") {
		t.Errorf("generic: wrong digest should fail")
	}
}

func TestVerifyAny_UnknownScheme(t *testing.T) {
	if VerifyAny("nope", []byte("k"), []byte("b"), "anything") {
		t.Errorf("unknown scheme should fail closed")
	}
}

func TestSignWith_RoundTrip(t *testing.T) {
	secret := []byte("shh")
	body := []byte("hi")
	for _, sch := range []SignatureScheme{SchemeGitHub, SchemeBitbucket, SchemeGitLab, SchemeGenericHMAC} {
		h := SignWith(sch, secret, body)
		if h == "" {
			t.Errorf("scheme=%s: SignWith returned empty", sch)
			continue
		}
		if !VerifyAny(sch, secret, body, h) {
			t.Errorf("scheme=%s: round trip failed (header=%q)", sch, h)
		}
	}
}
