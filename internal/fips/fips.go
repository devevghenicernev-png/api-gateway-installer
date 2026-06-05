// Package fips reports whether the binary was compiled with the BoringCrypto
// (FIPS 140-2 validated) toolchain.
//
// Build via:
//
//	GOEXPERIMENT=boringcrypto CGO_ENABLED=1 go build ./cmd/apigw
//
// At runtime, doctor surfaces the mode so operators in regulated industries
// can confirm compliance without re-reading the build script.
//
// NOTE: this does NOT mean apigw is FIPS-certified. It means the cryptographic
// primitives come from the BoringCrypto module which Google's certified.
// Formal certification of the apigw bundle is a separate process; document
// this distinction to operators.
package fips

// Enabled reports the FIPS-compatibility mode. We use a build-tag-gated
// constant so the value is determined at compile time — no runtime test.
//
// The "fips" build tag is set by GOEXPERIMENT=boringcrypto via
// internal Go tooling (`runtime/internal/sys.GOEXPERIMENT_boringcrypto`).
// We mirror it as a simple bool exported here.
func Enabled() bool { return enabled }
