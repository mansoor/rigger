package remotehost

import "sync"

// Pool keeps one reusable SSH connection per host id, dialing on demand and
// re-dialing when a cached connection has died. It is safe for concurrent use.
type Pool struct {
	mu      sync.Mutex
	clients map[int64]*Client
}

// NewPool returns an empty connection pool.
func NewPool() *Pool {
	return &Pool{clients: make(map[int64]*Client)}
}

// Get returns a live client for h, reusing a cached connection when it is still
// healthy and dialing a fresh one otherwise.
func (p *Pool) Get(h Host) (*Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if c, ok := p.clients[h.ID]; ok {
		if c.alive() {
			return c, nil
		}
		_ = c.Close()
		delete(p.clients, h.ID)
	}
	c, err := Dial(h)
	if err != nil {
		return nil, err
	}
	p.clients[h.ID] = c
	return c, nil
}

// Evict closes and forgets any cached connection for a host. Call it when a host
// is edited (key/address change) or deleted so the next Get re-dials.
func (p *Pool) Evict(id int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[id]; ok {
		_ = c.Close()
		delete(p.clients, id)
	}
}

// CloseAll closes every pooled connection (used on shutdown).
func (p *Pool) CloseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, c := range p.clients {
		_ = c.Close()
		delete(p.clients, id)
	}
}

// alive probes the SSH transport with a keepalive request.
func (c *Client) alive() bool {
	if c.ssh == nil {
		return false
	}
	_, _, err := c.ssh.SendRequest("keepalive@openssh.com", true, nil)
	return err == nil
}
