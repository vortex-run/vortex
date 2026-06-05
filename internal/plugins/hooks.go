package plugins

import (
	"context"
	"sort"
	"sync"
)

// HookType identifies when in the request lifecycle a hook fires.
type HookType string

// Hook types.
const (
	HookPreRequest   HookType = "pre_request"
	HookPostResponse HookType = "post_response"
	HookOnConnect    HookType = "on_connect"
	HookOnClose      HookType = "on_close"
)

// HookInput is the data passed to a hook for one event.
type HookInput struct {
	Type    HookType            `json:"type"`
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Headers map[string][]string `json:"headers"`
	Body    []byte              `json:"body"` // first 64KB only
	Remote  string              `json:"remote"`
	Route   string              `json:"route"`
}

// HookOutput is a hook's decision and modifications.
type HookOutput struct {
	Allow    bool                `json:"allow"`
	Modified bool                `json:"modified"`
	Headers  map[string][]string `json:"headers"` // headers to add/override
	Status   int                 `json:"status"`  // override status; 0 = no override
}

// Hook is a single request/response interceptor.
type Hook interface {
	Name() string
	Type() HookType
	Execute(ctx context.Context, in HookInput) (HookOutput, error)
}

// registered pairs a hook with its priority for ordered execution.
type registered struct {
	hook     Hook
	priority int
}

// HookChain runs a set of hooks in sequence. When priority ordering is enabled,
// hooks run in ascending priority order; otherwise in registration order. It is
// safe for concurrent registration and execution.
type HookChain struct {
	prioritise bool

	mu    sync.RWMutex
	hooks []registered
}

// NewHookChain creates a HookChain. With priority=true hooks execute in priority
// order (lowest first); otherwise in registration order.
func NewHookChain(priority bool) *HookChain {
	return &HookChain{prioritise: priority}
}

// Register adds a hook with the given priority.
func (c *HookChain) Register(h Hook, priority int) {
	c.mu.Lock()
	c.hooks = append(c.hooks, registered{hook: h, priority: priority})
	if c.prioritise {
		sort.SliceStable(c.hooks, func(i, j int) bool {
			return c.hooks[i].priority < c.hooks[j].priority
		})
	}
	c.mu.Unlock()
}

// Len returns the number of registered hooks.
func (c *HookChain) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.hooks)
}

// Execute runs the chain against in. It stops and returns a deny as soon as any
// hook returns Allow=false. Header modifications from all executed hooks are
// merged into the result; the first non-zero Status override wins. The final
// output's Allow is true only if every hook allowed.
func (c *HookChain) Execute(ctx context.Context, in HookInput) (HookOutput, error) {
	c.mu.RLock()
	hooks := make([]registered, len(c.hooks))
	copy(hooks, c.hooks)
	c.mu.RUnlock()

	out := HookOutput{Allow: true, Headers: map[string][]string{}}
	for _, r := range hooks {
		res, err := r.hook.Execute(ctx, in)
		if err != nil {
			return HookOutput{Allow: false}, err
		}
		// Merge header modifications.
		if len(res.Headers) > 0 {
			out.Modified = true
			for k, v := range res.Headers {
				out.Headers[k] = v
			}
		}
		// First non-zero status override wins.
		if res.Status != 0 && out.Status == 0 {
			out.Status = res.Status
		}
		// A deny short-circuits the chain.
		if !res.Allow {
			out.Allow = false
			return out, nil
		}
	}
	return out, nil
}
