package tenant

import (
	"errors"
	"testing"
)

func TestValidateID(t *testing.T) {
	cases := map[string]bool{
		"acme":      true,
		"big-corp":  true,
		"a":         false, // too short
		"-leading":  false,
		"trailing-": false,
		"UPPER":     false,
		"foo_bar":   false, // underscore not allowed
		"42":        true,  // 2 chars min is fine
		"42a":       true,
		"x":         false, // 1 char too short
	}
	for in, ok := range cases {
		err := ValidateID(in)
		gotOK := err == nil
		if gotOK != ok {
			t.Errorf("ValidateID(%q): got %v want %v (err=%v)", in, gotOK, ok, err)
		}
	}
}

func TestAddGetList(t *testing.T) {
	r := NewRegistry()
	if err := r.Add(Tenant{ID: "acme", Name: "Acme Corp", Enabled: true}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := r.Get("acme"); got == nil || got.Name != "Acme Corp" {
		t.Errorf("Get: %+v", got)
	}
	if got := r.Get("acme"); got.PathPrefix != "/t/acme" {
		t.Errorf("default PathPrefix not set; got %q", got.PathPrefix)
	}
	if len(r.List()) != 1 {
		t.Errorf("expected 1 tenant; got %d", len(r.List()))
	}
}

func TestAddDuplicate(t *testing.T) {
	r := NewRegistry()
	_ = r.Add(Tenant{ID: "acme", Enabled: true})
	if err := r.Add(Tenant{ID: "acme"}); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("expected ErrAlreadyExists; got %v", err)
	}
}

func TestIsAdmin(t *testing.T) {
	r := NewRegistry()
	_ = r.Add(Tenant{ID: "acme", Admins: []string{"alice"}, Enabled: true})
	if !r.IsAdmin("acme", "alice") {
		t.Errorf("alice should be admin of acme")
	}
	if r.IsAdmin("acme", "bob") {
		t.Errorf("bob should NOT be admin of acme")
	}
}

func TestRemove(t *testing.T) {
	r := NewRegistry()
	_ = r.Add(Tenant{ID: "acme", Enabled: true})
	if err := r.Remove("acme"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if r.Get("acme") != nil {
		t.Errorf("acme should be gone after Remove")
	}
	if err := r.Remove("nonexistent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound; got %v", err)
	}
}
