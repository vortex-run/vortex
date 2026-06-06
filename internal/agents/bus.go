package agents

import (
	"sync"
	"sync/atomic"
)

// Bus is a typed, in-process message bus that routes AgentMessages between
// registered agents. Routing is non-blocking: delivery to a full inbox is
// dropped (counted in stats) rather than blocking the sender, so one slow agent
// cannot stall the whole runtime.
type Bus struct {
	mu     sync.RWMutex
	agents map[string]Agent

	sent    atomic.Int64
	dropped atomic.Int64
}

// BusStats is a snapshot of bus activity.
type BusStats struct {
	MessagesSent    int64
	MessagesDropped int64
	AgentCount      int
}

// NewBus constructs an empty message bus.
func NewBus() *Bus {
	return &Bus{agents: make(map[string]Agent)}
}

// Register adds an agent under its name. It returns ErrAlreadyRegistered if the
// name is already taken.
func (b *Bus) Register(agent Agent) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.agents[agent.Name()]; ok {
		return ErrAlreadyRegistered
	}
	b.agents[agent.Name()] = agent
	return nil
}

// Unregister removes an agent by name. It is a no-op if the name is unknown.
func (b *Bus) Unregister(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.agents, name)
}

// Send routes msg to the inbox of msg.ToAgent. It returns ErrAgentNotFound if
// the recipient is not registered, or ErrBusFull if its inbox is full. A
// successful send increments MessagesSent; a full inbox increments
// MessagesDropped.
func (b *Bus) Send(msg AgentMessage) error {
	b.mu.RLock()
	agent, ok := b.agents[msg.ToAgent]
	b.mu.RUnlock()
	if !ok {
		return ErrAgentNotFound
	}
	if err := agent.Send(msg); err != nil {
		b.dropped.Add(1)
		return err
	}
	b.sent.Add(1)
	return nil
}

// Broadcast sends msg to every registered agent except the sender
// (msg.FromAgent). Delivery is non-blocking per recipient: a full inbox is
// dropped and counted, never blocking other recipients.
func (b *Bus) Broadcast(msg AgentMessage) {
	b.mu.RLock()
	recipients := make([]Agent, 0, len(b.agents))
	for name, a := range b.agents {
		if name == msg.FromAgent {
			continue
		}
		recipients = append(recipients, a)
	}
	b.mu.RUnlock()

	for _, a := range recipients {
		m := msg
		m.ToAgent = a.Name()
		if err := a.Send(m); err != nil {
			b.dropped.Add(1)
			continue
		}
		b.sent.Add(1)
	}
}

// Agents returns the names of all registered agents.
func (b *Bus) Agents() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	names := make([]string, 0, len(b.agents))
	for name := range b.agents {
		names = append(names, name)
	}
	return names
}

// Stats returns a snapshot of bus activity.
func (b *Bus) Stats() BusStats {
	b.mu.RLock()
	count := len(b.agents)
	b.mu.RUnlock()
	return BusStats{
		MessagesSent:    b.sent.Load(),
		MessagesDropped: b.dropped.Load(),
		AgentCount:      count,
	}
}
