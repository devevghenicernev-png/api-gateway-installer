package auth

import (
	"net"
	"time"
)

// newLDAPDialer returns a *net.Dialer (go-ldap requires the concrete type)
// pre-configured with the requested timeout.
func newLDAPDialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout}
}
