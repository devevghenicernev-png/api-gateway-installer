// Command apigw is the API Gateway installer and operator.
//
// This file is intentionally tiny — all wiring lives in internal/apigwcmd
// so the rest of the binary remains testable. Mirrors cli/cli's main.go.
package main

import (
	"os"

	"github.com/devevghenicernev-png/apigw/internal/apigwcmd"
)

func main() {
	os.Exit(int(apigwcmd.Main()))
}
