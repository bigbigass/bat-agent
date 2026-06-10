//go:build !cgo

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "deploy-agent-gui requires CGO enabled to build the Fyne desktop UI")
	os.Exit(1)
}
