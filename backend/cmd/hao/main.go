package main

import (
	"os"

	"github.com/aoagents/agent-orchestrator/backend/internal/haocli"
)

func main() { os.Exit(haocli.Execute()) }
