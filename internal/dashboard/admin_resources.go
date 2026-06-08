// Admin endpoints for resources that didn't have CRUD in the first pass:
// streams (TCP/UDP proxies) and consumers (auth credential aggregates).
//
// Pattern intentionally mirrors adminAPIsHandler / adminAPIHandler so a
// future code generator (or just code review by eye) can verify symmetry:
//
//	GET    /api/admin/<r>          → list
//	POST   /api/admin/<r>          → create
//	GET    /api/admin/<r>/<id>     → show
//	PUT    /api/admin/<r>/<id>     → update
//	DELETE /api/admin/<r>/<id>     → delete
//
// Each write is guarded through Server.Sec.Guard() so the audit chain,
// approvals queue, and RBAC checks behave exactly as they do for /apis.

package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/devevghenicernev-png/apigw/internal/config"
)

// ---------- /api/admin/streams ----------

func (s *Server) adminStreamsHandler(w http.ResponseWriter, r *http.Request) {
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
		if _, err := s.Sec.Guard(r, Action{Permission: "stream.list"}, nil); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		adminWriteJSON(w, http.StatusOK, cfg.Streams)
	case http.MethodPost:
		var st config.Stream
		if err := s.Sec.ReadBody(r, &st); err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		if err := validateStream(st); err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if findStream(cfg, st.Name) != nil {
			adminWriteJSONError(w, http.StatusConflict, fmt.Sprintf("stream %q already exists", st.Name))
			return
		}
		action := Action{
			Permission: "stream.add",
			Resource:   "stream/" + st.Name,
			After:      streamToMap(st),
		}
		if _, err := s.Sec.Guard(r, action, func() error {
			cfg.Streams = append(cfg.Streams, st)
			return cfg.Save()
		}); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		adminWriteJSON(w, http.StatusCreated, st)
	default:
		w.Header().Set("Allow", "GET, POST")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) adminStreamHandler(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/admin/streams/")
	if name == "" {
		adminWriteJSONError(w, http.StatusBadRequest, "stream name required")
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
	idx := streamIndex(cfg, name)
	if idx < 0 {
		adminWriteJSONError(w, http.StatusNotFound, "no such stream")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if _, err := s.Sec.Guard(r, Action{Permission: "stream.show", Resource: "stream/" + name}, nil); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		adminWriteJSON(w, http.StatusOK, cfg.Streams[idx])
	case http.MethodPut:
		var updated config.Stream
		if err := s.Sec.ReadBody(r, &updated); err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		updated.Name = name
		if err := validateStream(updated); err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		before := streamToMap(cfg.Streams[idx])
		after := streamToMap(updated)
		if _, err := s.Sec.Guard(r, Action{
			Permission: "stream.edit",
			Resource:   "stream/" + name,
			Before:     before,
			After:      after,
		}, func() error {
			cfg.Streams[idx] = updated
			return cfg.Save()
		}); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		adminWriteJSON(w, http.StatusOK, updated)
	case http.MethodDelete:
		before := streamToMap(cfg.Streams[idx])
		if _, err := s.Sec.Guard(r, Action{
			Permission: "stream.remove",
			Resource:   "stream/" + name,
			Before:     before,
			Dangerous:  true,
		}, func() error {
			cfg.Streams = append(cfg.Streams[:idx], cfg.Streams[idx+1:]...)
			return cfg.Save()
		}); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ---------- /api/admin/consumers ----------

func (s *Server) adminConsumersHandler(w http.ResponseWriter, r *http.Request) {
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
		if _, err := s.Sec.Guard(r, Action{Permission: "consumer.list"}, nil); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		adminWriteJSON(w, http.StatusOK, cfg.Security.Consumers)
	case http.MethodPost:
		var cn config.Consumer
		if err := s.Sec.ReadBody(r, &cn); err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		if err := validateAPIName(cn.ID); err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, "consumer.id: "+err.Error())
			return
		}
		if findConsumer(cfg, cn.ID) != nil {
			adminWriteJSONError(w, http.StatusConflict, fmt.Sprintf("consumer %q already exists", cn.ID))
			return
		}
		action := Action{
			Permission: "consumer.add",
			Resource:   "consumer/" + cn.ID,
			After:      consumerToMap(cn),
		}
		if _, err := s.Sec.Guard(r, action, func() error {
			cfg.Security.Consumers = append(cfg.Security.Consumers, cn)
			return cfg.Save()
		}); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		adminWriteJSON(w, http.StatusCreated, cn)
	default:
		w.Header().Set("Allow", "GET, POST")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) adminConsumerHandler(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/admin/consumers/")
	if id == "" {
		adminWriteJSONError(w, http.StatusBadRequest, "consumer id required")
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
	idx := consumerIndex(cfg, id)
	if idx < 0 {
		adminWriteJSONError(w, http.StatusNotFound, "no such consumer")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if _, err := s.Sec.Guard(r, Action{Permission: "consumer.show", Resource: "consumer/" + id}, nil); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		adminWriteJSON(w, http.StatusOK, cfg.Security.Consumers[idx])
	case http.MethodPut:
		var updated config.Consumer
		if err := s.Sec.ReadBody(r, &updated); err != nil {
			adminWriteJSONError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		updated.ID = id
		before := consumerToMap(cfg.Security.Consumers[idx])
		after := consumerToMap(updated)
		if _, err := s.Sec.Guard(r, Action{
			Permission: "consumer.edit",
			Resource:   "consumer/" + id,
			Before:     before,
			After:      after,
		}, func() error {
			cfg.Security.Consumers[idx] = updated
			return cfg.Save()
		}); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		adminWriteJSON(w, http.StatusOK, updated)
	case http.MethodDelete:
		before := consumerToMap(cfg.Security.Consumers[idx])
		if _, err := s.Sec.Guard(r, Action{
			Permission: "consumer.remove",
			Resource:   "consumer/" + id,
			Before:     before,
			Dangerous:  true,
		}, func() error {
			cfg.Security.Consumers = append(cfg.Security.Consumers[:idx], cfg.Security.Consumers[idx+1:]...)
			return cfg.Save()
		}); err != nil {
			adminWriteJSONError(w, Status(err), err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		adminWriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ---------- helpers ----------

func validateStream(s config.Stream) error {
	if err := validateAPIName(s.Name); err != nil {
		return err
	}
	switch strings.ToLower(s.Protocol) {
	case "tcp", "udp":
	default:
		return fmt.Errorf("%w: protocol must be tcp or udp (got %q)", ErrBadRequest, s.Protocol)
	}
	if s.ListenPort < 1 || s.ListenPort > 65535 {
		return fmt.Errorf("%w: listen_port out of range", ErrBadRequest)
	}
	if len(s.Upstreams) == 0 {
		return fmt.Errorf("%w: upstreams required", ErrBadRequest)
	}
	return nil
}

func findStream(cfg *config.Config, name string) *config.Stream {
	if i := streamIndex(cfg, name); i >= 0 {
		return &cfg.Streams[i]
	}
	return nil
}

func streamIndex(cfg *config.Config, name string) int {
	for i, st := range cfg.Streams {
		if st.Name == name {
			return i
		}
	}
	return -1
}

func findConsumer(cfg *config.Config, id string) *config.Consumer {
	if i := consumerIndex(cfg, id); i >= 0 {
		return &cfg.Security.Consumers[i]
	}
	return nil
}

func consumerIndex(cfg *config.Config, id string) int {
	for i, c := range cfg.Security.Consumers {
		if c.ID == id {
			return i
		}
	}
	return -1
}

func streamToMap(st config.Stream) map[string]any {
	b, _ := json.Marshal(st)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func consumerToMap(c config.Consumer) map[string]any {
	b, _ := json.Marshal(c)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}
