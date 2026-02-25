package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/diillson/chatcli/pkg/coder/engine"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--metadata":
			if err := json.NewEncoder(os.Stdout).Encode(engine.GetMetadata()); err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
				os.Exit(1)
			}
			return
		case "--schema":
			fmt.Println(engine.GetSchema())
			return
		}
	}

	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: @coder <subcommand> [flags]\n")
		os.Exit(1)
	}

	eng := engine.NewEngine(os.Stdout, os.Stderr)
	if err := eng.Execute(context.Background(), os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}
