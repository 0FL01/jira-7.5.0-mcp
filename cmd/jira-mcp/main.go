package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"time"

	"github.com/joho/godotenv"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmatczuk/jira-mcp/internal/jira"
	"github.com/mmatczuk/jira-mcp/internal/jiramcp"
)

// Injected at build time via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	// Load .env file if it exists (silent error if not found)
	_ = godotenv.Load()

	versionFlag := flag.Bool("version", false, "print version information and exit")
	flag.Parse()

	if *versionFlag {
		fmt.Printf("version: %s\ncommit:  %s\ndate:    %s\ngo:      %s\n", version, commit, date, runtime.Version())
		return
	}

	var interval time.Duration
	if s := os.Getenv("JIRA_REQUEST_INTERVAL"); s != "" {
		var err error
		interval, err = time.ParseDuration(s)
		if err != nil || interval < 0 {
			log.Fatal("Invalid JIRA_REQUEST_INTERVAL: use a non-negative duration such as 10s")
		}
	}
	client, err := jira.New(jira.Config{
		URL:             requireEnv("JIRA_URL"),
		Email:           requireEnv("JIRA_EMAIL"),
		APIToken:        requireEnv("JIRA_API_TOKEN"),
		MaxRetries:      3,
		BaseDelay:       time.Second,
		RequestInterval: interval,
	})
	if err != nil {
		log.Fatalf("Failed to create JIRA client: %v", err)
	}

	s := jiramcp.NewServer(client)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := s.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "Required environment variable %s is not set\n", key)
		os.Exit(1)
	}
	return v
}
