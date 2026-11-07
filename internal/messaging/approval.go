package messaging

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/sandeepbollavaram/vortex/internal/agents"
)

// ApprovalTimeout is how long an approval request waits for a human response
// before it is treated as rejected.
const ApprovalTimeout = 10 * time.Minute

// pendingApproval is an in-flight approval awaiting an approve/reject callback.
type pendingApproval struct {
	result chan bool
}

// ApprovalManager bridges agent ApprovalRequests to a messaging channel
// (Telegram) and back: it sends an approve/reject prompt, blocks until the user
// taps a button or the timeout elapses, and resolves the agents.ApprovalFunc.
type ApprovalManager struct {
	telegram *TelegramBot
	chatID   int64
	timeout  time.Duration

	seq     atomic.Uint64
	mu      sync.Mutex
	pending map[string]*pendingApproval
}

// NewApprovalManager builds a manager that routes approvals to the given
// Telegram bot + chat. timeout <= 0 uses ApprovalTimeout.
func NewApprovalManager(tg *TelegramBot, chatID int64, timeout time.Duration) *ApprovalManager {
	if timeout <= 0 {
		timeout = ApprovalTimeout
	}
	return &ApprovalManager{
		telegram: tg,
		chatID:   chatID,
		timeout:  timeout,
		pending:  make(map[string]*pendingApproval),
	}
}

// ApprovalFunc returns an agents.ApprovalFunc bound to this manager. It is wired
// into the coordinator so run_command (and other gated actions) request human
// sign-off before executing.
func (m *ApprovalManager) ApprovalFunc() agents.ApprovalFunc {
	return func(ctx context.Context, req agents.ApprovalRequest) bool {
		return m.request(ctx, req)
	}
}

// request sends an approval prompt and blocks until resolved, the context is
// cancelled, or the timeout elapses (treated as rejection).
func (m *ApprovalManager) request(ctx context.Context, req agents.ApprovalRequest) bool {
	if m.telegram == nil {
		return false // no approver configured → deny (fail safe)
	}
	id := fmt.Sprintf("appr-%d", m.seq.Add(1))
	p := &pendingApproval{result: make(chan bool, 1)}

	m.mu.Lock()
	m.pending[id] = p
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.pending, id)
		m.mu.Unlock()
	}()

	desc := approvalPrompt(id, req)
	if err := m.telegram.SendApprovalRequest(ctx, m.chatID, desc, "approve:"+id, "reject:"+id); err != nil {
		return false
	}

	timer := time.NewTimer(m.timeout)
	defer timer.Stop()
	select {
	case ok := <-p.result:
		return ok
	case <-timer.C:
		return false // timeout → reject
	case <-ctx.Done():
		return false
	}
}

// maxApprovalPreview caps the preview so the prompt stays well under
// Telegram's 4096-character message limit.
const maxApprovalPreview = 3000

// approvalPrompt renders what the approver is actually signing off on: the
// tool, its risk level, the one-line summary, the command or content preview,
// and the approval id. An approver must never see a prompt that is identical
// for every action (audit F-01). The text is sent without a parse mode, so the
// model-influenced fields cannot inject formatting.
func approvalPrompt(id string, req agents.ApprovalRequest) string {
	var b strings.Builder
	b.WriteString("🔐 Approval required (" + id + ")\n")
	tool := req.Tool
	if tool == "" && req.Command != "" {
		tool = "run_command"
	}
	if tool != "" {
		b.WriteString("Tool: " + tool + "\n")
	}
	b.WriteString("Risk: " + req.Risk() + "\n")
	if req.Description != "" {
		b.WriteString("Action: " + req.Description + "\n")
	}
	if req.Command != "" {
		cmd := req.Command
		if len(req.Args) > 0 {
			cmd += " " + strings.Join(req.Args, " ")
		}
		b.WriteString("Command: " + cmd + "\n")
	}
	preview := req.Preview
	if preview == "" && req.Command == "" && req.Description == "" {
		preview = "(no details were supplied for this action — reject unless you expected it)"
	}
	if preview != "" {
		if len(preview) > maxApprovalPreview {
			cut := maxApprovalPreview
			for cut > 0 && !utf8.RuneStart(preview[cut]) {
				cut--
			}
			preview = preview[:cut] + "\n…(truncated)"
		}
		b.WriteString("\n" + preview)
	}
	return b.String()
}

// Resolve records an approve/reject decision for the approval with the given id
// (extracted from a Telegram callback_data "approve:<id>" / "reject:<id>"). It
// returns true if a pending approval matched.
func (m *ApprovalManager) Resolve(callbackData string) bool {
	approved := false
	var id string
	switch {
	case len(callbackData) > 8 && callbackData[:8] == "approve:":
		approved, id = true, callbackData[8:]
	case len(callbackData) > 7 && callbackData[:7] == "reject:":
		approved, id = false, callbackData[7:]
	default:
		return false
	}

	m.mu.Lock()
	p, ok := m.pending[id]
	m.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case p.result <- approved:
	default:
	}
	return true
}
