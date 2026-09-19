package main

import (
	"fmt"
	"os"

	"github.com/nchgroup/artifact-delivery-server/internal/app"
)

func main() {
	if err := app.Run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "artifact-delivery-server: %v\n", err)
		os.Exit(1)
	}
}
