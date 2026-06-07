package dashboard

import (
	"reflect"
	"sort"
	"testing"

	"github.com/devevghenicernev-png/apigw/internal/config"
	"github.com/devevghenicernev-png/apigw/internal/rbac"
)

func TestBuildTeamIndex(t *testing.T) {
	teams := []config.Team{
		{Name: "ops", Members: []string{"alice", "bob"}},
		{Name: "ci", Members: []string{"alice"}},
	}
	idx := buildTeamIndex(teams)
	if !setEq(idx["alice"], []string{"ops", "ci"}) {
		t.Errorf("alice teams: %v", idx["alice"])
	}
	if !setEq(idx["bob"], []string{"ops"}) {
		t.Errorf("bob teams: %v", idx["bob"])
	}
}

func TestBuildTeamIndex_Empty(t *testing.T) {
	if idx := buildTeamIndex(nil); idx != nil {
		t.Errorf("nil input should yield nil")
	}
	if idx := buildTeamIndex([]config.Team{}); idx != nil {
		t.Errorf("empty input should yield nil")
	}
}

func TestExpandTeams_AppendsTeamGroups(t *testing.T) {
	s := &Security{teamsByUser: map[string][]string{
		"alice": {"ops", "ci"},
	}}
	got := s.expandTeams(rbac.Identity{User: "alice", Groups: []string{"saml-eng"}})
	if got.User != "alice" {
		t.Fatalf("user: %s", got.User)
	}
	want := []string{"saml-eng", "team:ops", "team:ci"}
	sort.Strings(got.Groups)
	sort.Strings(want)
	if !reflect.DeepEqual(got.Groups, want) {
		t.Errorf("groups: %v want %v", got.Groups, want)
	}
}

func TestExpandTeams_NoTeamsNoOp(t *testing.T) {
	s := &Security{}
	in := rbac.Identity{User: "alice", Groups: []string{"x"}}
	got := s.expandTeams(in)
	if !reflect.DeepEqual(got, in) {
		t.Errorf("no teams should be no-op")
	}
}

func TestExpandTeams_NilSafe(t *testing.T) {
	var s *Security
	got := s.expandTeams(rbac.Identity{User: "alice"})
	if got.User != "alice" || len(got.Groups) != 0 {
		t.Errorf("nil Security: %+v", got)
	}
}

func TestExpandTeams_UserNotInAnyTeam(t *testing.T) {
	s := &Security{teamsByUser: map[string][]string{"bob": {"ops"}}}
	in := rbac.Identity{User: "alice"}
	got := s.expandTeams(in)
	if !reflect.DeepEqual(got, in) {
		t.Errorf("unmatched user: %+v", got)
	}
}

func setEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	return reflect.DeepEqual(ac, bc)
}
