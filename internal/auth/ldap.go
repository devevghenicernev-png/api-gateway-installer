// Package auth — LDAP / Active Directory bind authentication.
//
// Flow: user runs `apigw login --ldap` → CLI prompts for username+password
// → apigw connects to the LDAP server, binds with the user's DN+password,
// queries their group memberships, and issues a short-lived session token
// (JWT signed with the dashboard's session key) that subsequent CLI calls
// present via APIGW_TOKEN env or ~/.config/apigw/session.
//
// Group → role mapping lives in config.RBAC.Assignments with the Group
// field — same primitive RBAC uses for any group source.
package auth

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
)

// LDAPConfig describes the directory and binding strategy.
//
// Two bind modes:
//
//	BindMode=simple        — user supplies DN+password directly
//	                         (BindDN must be a template like "uid={user},ou=people,dc=acme,dc=com")
//
//	BindMode=search        — apigw binds as a service account (BindUser+BindPass),
//	                         searches for the user by Filter, then re-binds as the
//	                         user with their password
type LDAPConfig struct {
	URL          string        // ldaps://ad.example.com:636 OR ldap://ds:389
	BaseDN       string        // dc=example,dc=com
	BindMode     string        // simple | search
	BindDN       string        // simple: template e.g. uid={user},ou=people,{base}
	BindUser     string        // search: service-account DN
	BindPass     string        // search: service-account password (use secret://...)
	UserFilter   string        // search: e.g. (&(objectClass=person)(uid={user}))
	GroupFilter  string        // search for the user's groups; default (member={dn})
	GroupAttr    string        // attribute holding group name; default "cn"
	TLSSkipCheck bool          // for self-signed; never in production
	Timeout      time.Duration // default 5s
}

// LDAPAuthResult is what AuthenticateLDAP returns on success.
type LDAPAuthResult struct {
	User   string
	DN     string
	Groups []string
}

// AuthenticateLDAP binds against the directory with `username` + `password`
// and resolves group memberships. Returns ErrInvalidCredentials on bind
// failure (wrong password, no such user), other errors for network/config
// issues.
func AuthenticateLDAP(ctx context.Context, cfg LDAPConfig, username, password string) (*LDAPAuthResult, error) {
	if cfg.URL == "" {
		return nil, errors.New("ldap: URL required")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Second
	}

	conn, err := dialLDAP(cfg)
	if err != nil {
		return nil, fmt.Errorf("ldap connect: %w", err)
	}
	defer conn.Close()
	conn.SetTimeout(cfg.Timeout)

	var userDN string
	switch cfg.BindMode {
	case "simple":
		userDN = expand(cfg.BindDN, username, cfg.BaseDN)
		if err := conn.Bind(userDN, password); err != nil {
			return nil, wrapBindErr(err)
		}
	case "search":
		// Service account bind first.
		if err := conn.Bind(cfg.BindUser, cfg.BindPass); err != nil {
			return nil, fmt.Errorf("ldap service bind: %w", err)
		}
		// Find the user.
		userDN, err = searchUser(conn, cfg, username)
		if err != nil {
			return nil, err
		}
		// Re-bind as the user.
		if err := conn.Bind(userDN, password); err != nil {
			return nil, wrapBindErr(err)
		}
	default:
		return nil, fmt.Errorf("ldap: unknown BindMode %q (use simple|search)", cfg.BindMode)
	}

	// After successful bind, look up groups.
	groups, err := searchGroups(conn, cfg, userDN)
	if err != nil {
		// Non-fatal: surface auth success but no groups.
		groups = nil
	}

	return &LDAPAuthResult{User: username, DN: userDN, Groups: groups}, nil
}

func dialLDAP(cfg LDAPConfig) (*ldap.Conn, error) {
	opts := []ldap.DialOpt{ldap.DialWithDialer(newLDAPDialer(cfg.Timeout))}
	if strings.HasPrefix(cfg.URL, "ldaps://") {
		opts = append(opts, ldap.DialWithTLSConfig(&tls.Config{InsecureSkipVerify: cfg.TLSSkipCheck}))
	}
	return ldap.DialURL(cfg.URL, opts...)
}

func searchUser(conn *ldap.Conn, cfg LDAPConfig, user string) (string, error) {
	filter := expand(cfg.UserFilter, user, cfg.BaseDN)
	req := ldap.NewSearchRequest(
		cfg.BaseDN,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		2, int(cfg.Timeout/time.Second), false,
		filter,
		[]string{"dn"},
		nil,
	)
	sr, err := conn.Search(req)
	if err != nil {
		return "", fmt.Errorf("ldap search user: %w", err)
	}
	if len(sr.Entries) == 0 {
		return "", ErrInvalidCredentials
	}
	if len(sr.Entries) > 1 {
		return "", fmt.Errorf("ldap: filter %q is ambiguous (%d matches)", filter, len(sr.Entries))
	}
	return sr.Entries[0].DN, nil
}

func searchGroups(conn *ldap.Conn, cfg LDAPConfig, userDN string) ([]string, error) {
	filter := cfg.GroupFilter
	if filter == "" {
		filter = "(member={dn})"
	}
	filter = strings.ReplaceAll(filter, "{dn}", ldap.EscapeFilter(userDN))
	attr := cfg.GroupAttr
	if attr == "" {
		attr = "cn"
	}
	req := ldap.NewSearchRequest(
		cfg.BaseDN,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		0, int(cfg.Timeout/time.Second), false,
		filter,
		[]string{attr},
		nil,
	)
	sr, err := conn.Search(req)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, e := range sr.Entries {
		for _, a := range e.Attributes {
			if a.Name == attr {
				out = append(out, a.Values...)
			}
		}
	}
	return out, nil
}

// expand substitutes {user} and {base} in a template string. Both LDAP
// search filters and bind DN templates use the same placeholders.
func expand(s, user, base string) string {
	s = strings.ReplaceAll(s, "{user}", ldap.EscapeFilter(user))
	s = strings.ReplaceAll(s, "{base}", base)
	return s
}

func wrapBindErr(err error) error {
	var le *ldap.Error
	if errors.As(err, &le) {
		// 49 = invalidCredentials per RFC 4511.
		if le.ResultCode == ldap.LDAPResultInvalidCredentials {
			return ErrInvalidCredentials
		}
	}
	return fmt.Errorf("ldap bind: %w", err)
}

// ErrInvalidCredentials is returned on bind failure (wrong password,
// no such user). Distinct from network/config errors so the CLI can
// show "invalid login" vs "directory unreachable".
var ErrInvalidCredentials = errors.New("ldap: invalid credentials")

// Silence ctx unused linter — kept on AuthenticateLDAP for future hop.
var _ = context.Background
