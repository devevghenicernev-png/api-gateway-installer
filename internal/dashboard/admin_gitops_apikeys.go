// /api/admin/gitops — singleton GET/PUT
// /api/admin/apis/<name>/keys, /api/admin/apis/<name>/keys/<id> — nested
// CRUD over an API's APIKey credentials.

package dashboard

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

// adminGitOpsHandler serves /api/admin/gitops. GET returns the current
// settings block; PUT replaces it wholesale (singleton — there's only
// ever one). The reconciler picks up the new repo URL / token at the next
// 30-second poll; no daemon restart needed.
func (s *Server) adminGitOpsHandler(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.ConfigFn()
	if err != nil {
		adminWriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ident := s.Sec.Identify(r)
	if err := s.ensureCSRF(r, ident); err != nil {
		adminWriteJSONError(w, Status(err), "csrf: "+err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		if _, err := s.Sec.Guard(r, Action{Permission: "gitops.show"}, nil); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		writeJSONWithETag(w, http.StatusOK, cfg.GitOps)
	case http.MethodPut:
		if err := checkIfMatch(r, cfg.GitOps); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		var updated config.GitOps
		if err := s.Sec.ReadBody(r, &updated); err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		before := gitopsToMap(cfg.GitOps)
		after := gitopsToMap(updated)
		if _, err := s.Sec.Guard(r, Action{
			Permission: "gitops.edit",
			Resource:   "gitops",
			Before:     before,
			After:      after,
		}, func() error {
			cfg.GitOps = updated
			return cfg.Save()
		}); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		writeJSONWithETag(w, http.StatusOK, updated)
	default:
		w.Header().Set("Allow", "GET, PUT")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// adminAPIKeysHandler serves /api/admin/apis/<name>/keys.
//
// GET  — list this API's keys (Secret is REDACTED in the response;
//
//	operators cannot fetch a secret after creation by design).
//
// POST — add a key. Body: {id, secret?, owner?, rps?, scopes?, expires_at?}.
//
//	When Secret is empty the server mints 32 bytes of crypto/rand.
//	The CREATED 201 response is the ONLY time the secret is sent
//	back — capture it then or rotate later.
func (s *Server) adminAPIKeysHandler(w http.ResponseWriter, r *http.Request) {
	// Path: /api/admin/apis/<name>/keys
	apiName, _, ok := parseAPIKeyPath(r.URL.Path)
	if !ok {
		adminWriteJSONError(w, http.StatusNotFound, "expected /api/admin/apis/<name>/keys[/<id>]")
		return
	}
	cfg, err := s.ConfigFn()
	if err != nil {
		adminWriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ident := s.Sec.Identify(r)
	if err := s.ensureCSRF(r, ident); err != nil {
		adminWriteJSONError(w, Status(err), "csrf: "+err.Error())
		return
	}
	api := cfg.FindAPI(apiName)
	if api == nil {
		adminWriteJSONError(w, http.StatusNotFound, "no such api")
		return
	}
	if api.APIKey == nil {
		api.APIKey = &config.APIKeyAuth{}
	}
	switch r.Method {
	case http.MethodGet:
		if _, err := s.Sec.Guard(r, Action{Permission: "auth.list", Resource: "apikey/" + apiName}, nil); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		out := make([]config.APIKey, len(api.APIKey.Keys))
		copy(out, api.APIKey.Keys)
		for i := range out {
			out[i].Secret = "redacted"
		}
		pageOpts := parsePage(r)
		out = applyPage(w, out, pageOpts)
		adminWriteJSON(w, http.StatusOK, out)
	case http.MethodPost:
		var k config.APIKey
		if err := s.Sec.ReadBody(r, &k); err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		if err := validateAPIName(k.ID); err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, "key id: "+err.Error())
			return
		}
		for _, existing := range api.APIKey.Keys {
			if existing.ID == k.ID {
				adminWriteJSONError(w, http.StatusConflict, fmt.Sprintf("key %q already exists", k.ID))
				return
			}
		}
		if k.Secret == "" {
			secret, err := mintAPIKeySecret()
			if err != nil {
				adminWriteJSONError(w, http.StatusInternalServerError, err.Error())
				return
			}
			k.Secret = secret
		}
		if _, err := s.Sec.Guard(r, Action{
			Permission: "auth.edit",
			Resource:   "apikey/" + apiName + "/" + k.ID,
			After:      map[string]any{"id": k.ID, "owner": k.Owner, "scopes": k.Scopes},
		}, func() error {
			api.APIKey.Keys = append(api.APIKey.Keys, k)
			return cfg.Save()
		}); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		// Echo back WITH the secret — first and only chance to capture it.
		adminWriteJSON(w, http.StatusCreated, k)
	default:
		w.Header().Set("Allow", "GET, POST")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// adminAPIKeyHandler serves /api/admin/apis/<name>/keys/<id>.
//
// DELETE removes the key (revocation). GET reports just metadata
// (secret never echoed).
func (s *Server) adminAPIKeyHandler(w http.ResponseWriter, r *http.Request) {
	apiName, keyID, ok := parseAPIKeyPath(r.URL.Path)
	if !ok || keyID == "" {
		adminWriteJSONError(w, http.StatusNotFound, "expected /api/admin/apis/<name>/keys/<id>")
		return
	}
	cfg, err := s.ConfigFn()
	if err != nil {
		adminWriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ident := s.Sec.Identify(r)
	if err := s.ensureCSRF(r, ident); err != nil {
		adminWriteJSONError(w, Status(err), "csrf: "+err.Error())
		return
	}
	api := cfg.FindAPI(apiName)
	if api == nil || api.APIKey == nil {
		adminWriteJSONError(w, http.StatusNotFound, "no such api or no keys configured")
		return
	}
	idx := -1
	for i, k := range api.APIKey.Keys {
		if k.ID == keyID {
			idx = i
			break
		}
	}
	if idx < 0 {
		adminWriteJSONError(w, http.StatusNotFound, "no such key")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if _, err := s.Sec.Guard(r, Action{Permission: "auth.show", Resource: "apikey/" + apiName + "/" + keyID}, nil); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		clone := api.APIKey.Keys[idx]
		clone.Secret = "redacted"
		adminWriteJSON(w, http.StatusOK, clone)
	case http.MethodDelete:
		before := map[string]any{"id": keyID, "owner": api.APIKey.Keys[idx].Owner}
		if _, err := s.Sec.Guard(r, Action{
			Permission: "auth.edit",
			Resource:   "apikey/" + apiName + "/" + keyID,
			Before:     before,
			Dangerous:  true,
		}, func() error {
			api.APIKey.Keys = append(api.APIKey.Keys[:idx], api.APIKey.Keys[idx+1:]...)
			return cfg.Save()
		}); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, DELETE")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// parseAPIKeyPath splits "/api/admin/apis/<name>/keys[/<id>]" into its
// three pieces. Returns (name, id, true) on success — `id` is "" for the
// collection endpoint, populated for the single-key endpoint.
func parseAPIKeyPath(path string) (string, string, bool) {
	const prefix = "/api/admin/apis/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.Split(rest, "/")
	// expect ["<name>", "keys"] or ["<name>", "keys", "<id>"]
	if len(parts) < 2 || parts[1] != "keys" {
		return "", "", false
	}
	if len(parts) == 2 {
		return parts[0], "", true
	}
	return parts[0], parts[2], true
}

func gitopsToMap(g config.GitOps) map[string]any {
	b, _ := json.Marshal(g)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// mintAPIKeySecret generates a 32-byte URL-safe secret.
func mintAPIKeySecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
