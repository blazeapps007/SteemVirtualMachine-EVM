// Package nodepool spreads an oracle's Steem RPC calls over several nodes and
// benches a node that misbehaves, so one validator IP never hammers a single
// public node into rate-limiting or blacklisting it, and one broken node never
// stops the oracle.
//
// A node is benched after any failed call (transport error, an HTML/non-JSON
// reply, a JSON-RPC error such as "Internal Error" or "Bad Cast", or an answer
// that fails the caller's check) for a cooldown that doubles with each
// consecutive failure (30s, 1m, 2m … capped at 10m) and resets on success.
// Benched nodes are still tried last, so a call goes out even when every node
// is benched.
package nodepool

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// Mode picks the node a call starts at.
type Mode int

const (
	// RoundRobin starts each call at the next node, spreading the load. For
	// reads whose answer is the same on every synced node (irreversible
	// blocks, price feeds).
	RoundRobin Mode = iota
	// Sticky starts each call at the first healthy node in the configured
	// order (own node first), so consecutive reads come from one view of the
	// chain. Failover only.
	Sticky
)

const (
	baseCooldown = 30 * time.Second
	maxCooldown  = 10 * time.Minute
)

// Pool is safe for concurrent use.
type Pool struct {
	mu    sync.Mutex
	mode  Mode
	nodes []string
	next  int
	fails map[string]int
	until map[string]time.Time
	now   func() time.Time
}

// Parse splits each argument on commas, trims the parts and drops empty and
// repeated entries, keeping first-seen order.
func Parse(lists ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range lists {
		for _, n := range strings.Split(l, ",") {
			n = strings.TrimRight(strings.TrimSpace(n), "/")
			if n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// New returns a pool over the given nodes (each argument may itself be a
// comma-separated list).
func New(mode Mode, nodes ...string) *Pool {
	return &Pool{
		mode:  mode,
		nodes: Parse(nodes...),
		fails: map[string]int{},
		until: map[string]time.Time{},
		now:   time.Now,
	}
}

// Nodes returns the configured nodes in their configured order.
func (p *Pool) Nodes() []string {
	return append([]string(nil), p.nodes...)
}

// Len is the number of configured nodes.
func (p *Pool) Len() int { return len(p.nodes) }

// Order returns the nodes to try for one call: healthy nodes first (from the
// round-robin cursor, or from the first node when Sticky), then benched nodes,
// soonest-to-recover first.
func (p *Pool) Order() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.nodes)
	if n == 0 {
		return nil
	}
	start := 0
	if p.mode == RoundRobin {
		start = p.next % n
		p.next = (p.next + 1) % n
	}
	now := p.now()
	healthy := make([]string, 0, n)
	var benched []string
	for i := 0; i < n; i++ {
		node := p.nodes[(start+i)%n]
		if p.until[node].After(now) {
			benched = append(benched, node)
		} else {
			healthy = append(healthy, node)
		}
	}
	sort.SliceStable(benched, func(a, b int) bool { return p.until[benched[a]].Before(p.until[benched[b]]) })
	return append(healthy, benched...)
}

// Fail benches node and returns how long for.
func (p *Pool) Fail(node string) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fails[node]++
	d := baseCooldown
	for i := 1; i < p.fails[node] && d < maxCooldown; i++ {
		d *= 2
	}
	d = min(d, maxCooldown)
	p.until[node] = p.now().Add(d)
	return d
}

// OK clears node's failure streak.
func (p *Pool) OK(node string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.fails, node)
	delete(p.until, node)
}
