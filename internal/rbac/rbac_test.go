package rbac

import (
	"errors"
	"testing"
)

func TestBuiltinViewerAllowsList(t *testing.T) {
	e := NewEngine(true, nil)
	e.LoadConfig(nil, []Assignment{{User: "alice", Roles: []string{"viewer"}}})
	if err := e.Check(Identity{User: "alice"}, "api.list", ""); err != nil {
		t.Errorf("viewer should allow api.list; got %v", err)
	}
}

func TestBuiltinViewerDeniesApply(t *testing.T) {
	e := NewEngine(true, nil)
	e.LoadConfig(nil, []Assignment{{User: "alice", Roles: []string{"viewer"}}})
	if err := e.Check(Identity{User: "alice"}, "deploy.apply", ""); !errors.Is(err, ErrDenied) {
		t.Errorf("viewer should deny deploy.apply; got %v", err)
	}
}

func TestOwnerHasEverything(t *testing.T) {
	e := NewEngine(true, nil)
	e.LoadConfig(nil, []Assignment{{User: "root", Roles: []string{"owner"}}})
	for _, p := range []Permission{"api.add", "deploy.apply", "secret.rotate", "anything.weird"} {
		if err := e.Check(Identity{User: "root"}, p, ""); err != nil {
			t.Errorf("owner should allow %s; got %v", p, err)
		}
	}
}

func TestWildcardSuffix(t *testing.T) {
	e := NewEngine(true, nil)
	e.LoadConfig(
		[]Role{{Name: "deployer", Permissions: []Permission{"deploy.*"}}},
		[]Assignment{{User: "bob", Roles: []string{"deployer"}}},
	)
	if err := e.Check(Identity{User: "bob"}, "deploy.apply", ""); err != nil {
		t.Errorf("deploy.* should match deploy.apply; got %v", err)
	}
	if err := e.Check(Identity{User: "bob"}, "api.add", ""); !errors.Is(err, ErrDenied) {
		t.Errorf("deploy.* should not match api.add; got %v", err)
	}
}

func TestGroupMembership(t *testing.T) {
	e := NewEngine(true, nil)
	e.LoadConfig(nil, []Assignment{{Group: "ops", Roles: []string{"operator"}}})
	if err := e.Check(Identity{User: "carol", Groups: []string{"ops"}}, "deploy.apply", ""); err != nil {
		t.Errorf("group→role should grant deploy.apply; got %v", err)
	}
}

func TestEnforceOffNeverDenies(t *testing.T) {
	denials := 0
	e := NewEngine(false, func(actor, action, resource, result, reason string) {
		if result == "denied" {
			denials++
		}
	})
	e.LoadConfig(nil, nil) // no users → everyone denied
	if err := e.Check(Identity{User: "ghost"}, "deploy.apply", ""); err != nil {
		t.Errorf("enforce=false should allow; got %v", err)
	}
	if denials != 1 {
		t.Errorf("expected denial to be audited even when allowed; got %d", denials)
	}
}

func TestAuditOnAllow(t *testing.T) {
	var actor, result string
	e := NewEngine(true, func(a, _, _, res, _ string) { actor = a; result = res })
	e.LoadConfig(nil, []Assignment{{User: "alice", Roles: []string{"viewer"}}})
	_ = e.Check(Identity{User: "alice"}, "api.list", "")
	if actor != "alice" || result != "ok" {
		t.Errorf("audit on allow: actor=%s result=%s", actor, result)
	}
}
