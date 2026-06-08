package openapi

import "testing"

func TestExport_EmptyAPIs(t *testing.T) {
	s := Export(nil, "Acme", "1.0", "https://api.example.com")
	if s.OpenAPI != "3.0.3" {
		t.Errorf("openapi version: %s", s.OpenAPI)
	}
	if s.Info.Title != "Acme" || s.Info.Version != "1.0" {
		t.Errorf("info: %+v", s.Info)
	}
	if len(s.Paths) != 0 {
		t.Errorf("paths should be empty: %v", s.Paths)
	}
	if len(s.Servers) != 1 || s.Servers[0].URL != "https://api.example.com" {
		t.Errorf("servers: %+v", s.Servers)
	}
}

func TestExport_OneAPIEachMethod(t *testing.T) {
	apis := []API{
		{Name: "billing", Path: "/api/billing", Enabled: true},
	}
	s := Export(apis, "T", "1", "")
	p, ok := s.Paths["/api/billing"]
	if !ok {
		t.Fatalf("path missing: %+v", s.Paths)
	}
	for _, op := range []*Operation{p.Get, p.Post, p.Put, p.Delete, p.Patch} {
		if op == nil {
			t.Fatalf("missing method op")
		}
		if op.Responses["200"].Description == "" {
			t.Errorf("no 200 response")
		}
	}
}

func TestExport_SkipsDisabledAndRetired(t *testing.T) {
	apis := []API{
		{Name: "live", Path: "/live", Enabled: true},
		{Name: "off", Path: "/off", Enabled: false},
		{Name: "dead", Path: "/dead", Enabled: true, Retired: true},
	}
	s := Export(apis, "T", "1", "")
	if _, ok := s.Paths["/live"]; !ok {
		t.Errorf("live missing")
	}
	if _, ok := s.Paths["/off"]; ok {
		t.Errorf("disabled API should not export")
	}
	if _, ok := s.Paths["/dead"]; ok {
		t.Errorf("retired API should not export")
	}
}

func TestExport_DefaultsPath(t *testing.T) {
	apis := []API{{Name: "billing", Enabled: true}}
	s := Export(apis, "T", "1", "")
	if _, ok := s.Paths["/api/billing"]; !ok {
		t.Errorf("default path /api/<name> missing: %+v", s.Paths)
	}
}

func TestExport_SecuritySchemes(t *testing.T) {
	apis := []API{
		{Name: "k", Path: "/k", Enabled: true, HasAPIKey: true, APIKeyName: "X-K"},
		{Name: "j", Path: "/j", Enabled: true, HasJWT: true},
		{Name: "m", Path: "/m", Enabled: true, HasMTLS: true},
	}
	s := Export(apis, "T", "1", "")
	if s.Components == nil {
		t.Fatalf("components nil")
	}
	if s.Components.SecuritySchemes["apigw_apikey"].Name != "X-K" {
		t.Errorf("apikey header name not propagated")
	}
	if s.Components.SecuritySchemes["apigw_jwt"].Scheme != "bearer" {
		t.Errorf("jwt scheme")
	}
	if s.Components.SecuritySchemes["apigw_mtls"].Type != "mutualTLS" {
		t.Errorf("mtls type")
	}
	// Each operation in /k must list apigw_apikey under security.
	op := s.Paths["/k"].Get
	if len(op.Security) != 1 {
		t.Fatalf("security on /k: %+v", op.Security)
	}
	if _, ok := op.Security[0]["apigw_apikey"]; !ok {
		t.Errorf("apikey security ref missing")
	}
}

func TestExport_DeprecatedMarked(t *testing.T) {
	apis := []API{{Name: "d", Path: "/d", Enabled: true, Deprecated: true}}
	s := Export(apis, "T", "1", "")
	if !s.Paths["/d"].Get.Deprecated {
		t.Errorf("deprecated flag not propagated")
	}
}

func TestExport_TagOrderStable(t *testing.T) {
	apis := []API{
		{Name: "zeta", Path: "/z", Enabled: true},
		{Name: "alpha", Path: "/a", Enabled: true},
	}
	s := Export(apis, "T", "1", "")
	if s.Tags[0].Name != "alpha" || s.Tags[1].Name != "zeta" {
		t.Errorf("tags not sorted: %+v", s.Tags)
	}
}
