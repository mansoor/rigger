package alerts

import "sync"

// Broker is a tiny in-process fan-out hub so the evaluator can push alert
// events to every connected SSE client. The api package's StreamEvents handler
// subscribes; the evaluator publishes. Sends are non-blocking — a slow or stuck
// subscriber drops messages rather than stalling the evaluator.
type Broker struct {
	mu   sync.RWMutex
	subs map[chan []byte]struct{}
}

func NewBroker() *Broker {
	return &Broker{subs: make(map[chan []byte]struct{})}
}

// Subscribe returns a buffered channel that receives published messages.
// Always pair with Unsubscribe (e.g. via defer) when the consumer goes away.
func (b *Broker) Subscribe() chan []byte {
	ch := make(chan []byte, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Broker) Unsubscribe(ch chan []byte) {
	b.mu.Lock()
	if _, ok := b.subs[ch]; ok {
		delete(b.subs, ch)
		close(ch)
	}
	b.mu.Unlock()
}

// Publish delivers msg to all subscribers without blocking. If a subscriber's
// buffer is full the message is dropped for that subscriber only.
func (b *Broker) Publish(msg []byte) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- msg:
		default: // subscriber not keeping up — drop
		}
	}
}
