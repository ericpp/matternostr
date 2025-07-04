package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/sirupsen/logrus"
)

func main() {
	var configFile = flag.String("conf", "matternostr.toml", "config file")
	var debug = flag.Bool("debug", false, "enable debug logging")
	flag.Parse()

	logger := logrus.New()
	if *debug {
		logger.SetLevel(logrus.DebugLevel)
	}

	config, err := LoadConfig(*configFile)
	if err != nil {
		logger.Fatalf("Failed to load config: %v", err)
	}

	plugin := NewNostrPlugin(config, logger)
	if err := plugin.Start(); err != nil {
		logger.Fatalf("Failed to start plugin: %v", err)
	}

	logger.Info("Nostr plugin started successfully")

	// Wait for interrupt signal
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	logger.Info("Shutting down...")
	plugin.Stop()
}
