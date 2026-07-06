package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ibreakthecloud/kiwi/pkg/orchestrator"
)

func main() {
	addr := flag.String("addr", ":8080", "The address the Kiwi cloud daemon listens on")
	flag.Parse()

	server := orchestrator.NewServer()

	fmt.Println("====================================================")
	fmt.Println("  KIWID: Kiwi Cloud Daemon Server")
	fmt.Println("====================================================")

	err := server.Start(*addr)
	if err != nil {
		fmt.Printf("Error starting Kiwi daemon: %v\n", err)
		os.Exit(1)
	}
}
