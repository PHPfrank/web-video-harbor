package main

import (
	"fmt"
	"io"
	"os"
)

const version = "dev"

func run(args []string, stdout io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintf(stdout, "web-video-helper %s\n", version)
	}

	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}
