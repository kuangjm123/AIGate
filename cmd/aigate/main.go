package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kuangjm123/AIGate/internal/agent"
	"github.com/kuangjm123/AIGate/internal/app"
	"github.com/kuangjm123/AIGate/internal/state"
)

func main() {
	logger := log.New(os.Stderr, "aigate ", log.LstdFlags)
	configEnv := os.Getenv("AIGATE_CONFIG")
	userDir := ""
	if configEnv == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			logger.Fatal("user config directory unavailable")
		}
		userDir = dir
	}
	paths, err := state.ResolvePaths(configEnv, userDir)
	if err != nil {
		logger.Fatal("config path unavailable")
	}
	store := state.Open(paths)
	application := app.New(store, logger)
	application.EnableAccounts()
	machine, err := agent.ThisMachine(filepath.Join(filepath.Dir(paths.ConfigFile), "agents"))
	if err != nil {
		logger.Printf("agent wiring unavailable")
	} else {
		application.EnableAgentWiring(machine)
	}
	if err := application.Start(); err != nil {
		logger.Fatalf("start failed: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGHUP, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := application.Shutdown(shut); err != nil {
		logger.Printf("shutdown incomplete")
	}
}
