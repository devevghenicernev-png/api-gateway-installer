package webhook

import (
	"encoding/json"
	"errors"
	"strings"
)

// pushEvent is the slice of GitHub's push-event payload that we care about.
//
// We deliberately don't pull in google/go-github — that package weighs ~5 MB
// and the only useful piece for us is field shape. Manual unmarshal keeps
// the binary lean and the schema explicit.
//
// https://docs.github.com/en/webhooks/webhook-events-and-payloads#push
type pushEvent struct {
	Ref        string `json:"ref"`     // refs/heads/<branch> or refs/tags/<tag>
	After      string `json:"after"`   // new commit SHA
	Before     string `json:"before"`  // previous SHA (zero on first push)
	Deleted    bool   `json:"deleted"` // branch was deleted
	Repository struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"` // https
		SSHURL   string `json:"ssh_url"`   // ssh
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
}

// ParsePush extracts the deploy-relevant fields from a push event. Returns
// the branch name (without refs/heads/ prefix), the new SHA, and an error
// for non-push events or malformed payloads.
//
// Tag pushes and branch deletions return errIgnore so callers can short-circuit.
func ParsePush(body []byte) (branch, sha, repo string, err error) {
	var p pushEvent
	if err := json.Unmarshal(body, &p); err != nil {
		return "", "", "", err
	}
	if p.Deleted {
		return "", "", "", ErrIgnore
	}
	if !strings.HasPrefix(p.Ref, "refs/heads/") {
		// Tag push, refs/pull/*, etc.
		return "", "", "", ErrIgnore
	}
	branch = strings.TrimPrefix(p.Ref, "refs/heads/")
	sha = p.After
	repo = p.Repository.CloneURL
	if repo == "" {
		repo = p.Repository.SSHURL
	}
	return branch, sha, repo, nil
}

// ErrIgnore signals a payload we intentionally drop (tag push, deleted
// branch, ping, etc.). Callers should treat it as "OK, nothing to do" — log
// at debug level, return 200 to GitHub.
var ErrIgnore = errors.New("webhook payload ignored")

// IsPing reports whether the event is GitHub's "ping" (sent on webhook
// creation to verify reachability). We accept it but skip enqueue.
func IsPing(event string) bool { return event == "ping" }
