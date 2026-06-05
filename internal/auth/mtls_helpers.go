package auth

import (
	"crypto/sha256"
	"encoding/hex"
)

// sha256SumImpl + hexEncode live in a separate file so mtls.go stays
// focused on the verification logic. Trivial pass-throughs that exist
// purely to keep the public surface of mtls.go clean.

func sha256SumImpl(b []byte) [32]byte { return sha256.Sum256(b) }

func hexEncode(b []byte) string { return hex.EncodeToString(b) }
