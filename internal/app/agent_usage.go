// AgentUsage counts inference requests per agent role for the current
// conversation. The count is what the status bar shows as icon + number: how
// much work each role actually spent, not how many calls were launched.
//
// Writers are the background goroutines that run agents and reviews; the render
// path only reads a snapshot. One mutex serialises them.
package app

import (
	"sort"
	"strings"
	"sync"
)

// agentUsageOrder is the display order: the advisor first, then the bundled
// roles in the order a session usually uses them, then custom agents
// alphabetically.
var agentUsageOrder = []string{"advisor", "quick", "explore", "task", "reviewer"}

// AgentUsageEntry is one role's accumulated request count.
type AgentUsageEntry struct {
	Name  string
	Count int
}

// AgentUsage accumulates per-role counts for one session. Held by pointer on
// services so every goroutine adds to the same instance.
type AgentUsage struct {
	mu     sync.Mutex
	counts map[string]int
}

// NewAgentUsage returns an empty counter.
func NewAgentUsage() *AgentUsage { return &AgentUsage{counts: make(map[string]int)} }

// Add records n inference requests for name. Non-positive counts and blank
// names are ignored: a role that ran no inference has nothing to show.
func (u *AgentUsage) Add(name string, n int) {
	if u == nil || n <= 0 {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.counts == nil {
		u.counts = make(map[string]int)
	}
	u.counts[name] += n
}

// Snapshot returns the non-zero counts in display order. The returned slice is
// a copy, so the caller may hold it while a background run adds more.
func (u *AgentUsage) Snapshot() []AgentUsageEntry {
	if u == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	entries := make([]AgentUsageEntry, 0, len(u.counts))
	seen := make(map[string]bool, len(agentUsageOrder))
	for _, name := range agentUsageOrder {
		if count := u.counts[name]; count > 0 {
			entries = append(entries, AgentUsageEntry{Name: name, Count: count})
			seen[name] = true
		}
	}
	custom := make([]string, 0, len(u.counts))
	for name, count := range u.counts {
		if count > 0 && !seen[name] {
			custom = append(custom, name)
		}
	}
	sort.Strings(custom)
	for _, name := range custom {
		entries = append(entries, AgentUsageEntry{Name: name, Count: u.counts[name]})
	}
	return entries
}

// Reset starts a new conversation's counts.
func (u *AgentUsage) Reset() {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.counts = make(map[string]int)
}
