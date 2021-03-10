package main

import (
	"kubectl-debugpod/internal/cmd"
)

func main() {
	cmd.RootCmd().Execute()
}
