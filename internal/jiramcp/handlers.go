package jiramcp

import "sync"

type handlers struct {
	client    JiraClient
	reportMu  sync.Mutex
	uncertain map[worklogIdentity]bool
}
