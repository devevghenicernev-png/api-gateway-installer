// ETag / If-Match optimistic-concurrency helpers + opt-in pagination for
// list endpoints. Both surfaces are additive — clients that don't send
// the header (or pagination params) behave exactly as before.

package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// etagOf computes the canonical ETag value for `body`. We use sha256
// truncated to 16 hex chars — collision-resistant enough for optimistic
// concurrency without bloating headers. Quoted per RFC 7232.
func etagOf(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:])[:16] + `"`
}

// etagOfValue marshals `v` to JSON (canonical key order is best-effort —
// json.Marshal walks struct fields in source order, which is deterministic)
// and hashes the result. Used to stamp resources on GET.
func etagOfValue(v any) (string, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("etag marshal: %w", err)
	}
	return etagOf(body), nil
}

// checkIfMatch returns nil when the request either omits If-Match (no
// precondition) or sends one that matches the current value's ETag.
// Returns ErrPreconditionFailed otherwise; caller maps to HTTP 412.
//
// Per RFC 7232 §3.1: `If-Match: *` means "exists at all".
func checkIfMatch(r *http.Request, current any) error {
	header := r.Header.Get("If-Match")
	if header == "" {
		return nil
	}
	if header == "*" {
		return nil
	}
	want, err := etagOfValue(current)
	if err != nil {
		return err
	}
	if header != want {
		return ErrPreconditionFailed
	}
	return nil
}

// writeJSONWithETag wraps adminWriteJSON and stamps an ETag header.
// Used on GET handlers to let clients later send the value back as
// If-Match on PUT/DELETE.
func writeJSONWithETag(w http.ResponseWriter, status int, v any) {
	if tag, err := etagOfValue(v); err == nil {
		w.Header().Set("ETag", tag)
	}
	adminWriteJSON(w, status, v)
}

// ErrPreconditionFailed signals an If-Match mismatch. Mapped to 412 by
// Status(); admin endpoints surface it as a JSON error.
var ErrPreconditionFailed = errPreconditionFailed{}

type errPreconditionFailed struct{}

func (errPreconditionFailed) Error() string { return "precondition failed (If-Match mismatch)" }

// pageOpts captures the ?limit=&offset= query parameters. When `Limit` is
// 0 the caller treats this as "no pagination requested" and returns the
// full slice unmodified.
type pageOpts struct {
	Limit  int
	Offset int
}

// parsePage extracts limit/offset, clamps limit to [0, 1000] (defensive
// cap so a hostile client can't ask for a 10M-item list).
func parsePage(r *http.Request) pageOpts {
	q := r.URL.Query()
	out := pageOpts{}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			out.Limit = n
			if out.Limit > 1000 {
				out.Limit = 1000
			}
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			out.Offset = n
		}
	}
	return out
}

// applyPage slices `items` per opts and stamps X-Total-Count + X-Offset
// + X-Limit so clients can show "showing N of M" without a second call.
// Pass-through when opts.Limit == 0.
//
// The generic shape avoids reflect: callers pass a length and a slicer
// func. Hot paths can build their own.
func applyPage[T any](w http.ResponseWriter, items []T, opts pageOpts) []T {
	total := len(items)
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	if opts.Limit == 0 {
		return items
	}
	w.Header().Set("X-Offset", strconv.Itoa(opts.Offset))
	w.Header().Set("X-Limit", strconv.Itoa(opts.Limit))
	if opts.Offset >= total {
		return items[:0]
	}
	end := opts.Offset + opts.Limit
	if end > total {
		end = total
	}
	return items[opts.Offset:end]
}
