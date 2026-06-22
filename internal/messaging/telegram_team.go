// This file extends the Telegram bot with agent-team interaction (build plan
// AG-UI File 5): live agent-to-agent visibility, checkpoint approval via inline
// buttons, and direct chat with a named specialist via an "@agent" prefix.
//
// It bridges the a2a MessageBus and CheckpointManager to a Telegram chat: bus
// progress lines are batched into periodic digests, checkpoints are surfaced as
// approve/reject/view buttons, and task completion is summarised. A user can
// talk to any specialist directly by prefixing a message with "@code",
// "@test", or "@review".
package messaging

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/vortex-run/vortex/internal/a2a"
)

// checkpointResolver is the subset of *a2a.CheckpointManager the bridge needs to
// approve or reject a checkpoint from a button tap.
type checkpointResolver interface {
	Approve(id string) error
	Reject(id, reason string) error
	Get(id string) (*a2a.Checkpoint, error)
}

// directChatter routes a user message to a named specialist and returns its
// reply. *a2a.AgentServer (via DirectChatFor) is adapted to this in start.go.
type directChatter interface {
	Chat(ctx context.Context, agentID, sessionID, message string) (string, error)
}

// teamSender is the slice of *TelegramBot the bridge uses to talk to Telegram.
// Defining it as an interface keeps the bridge unit-testable with a fake.
type teamSender interface {
	SendMessage(ctx context.Context, chatID int64, text string) error
	SendApprovalRequest(ctx context.Context, chatID int64, description, approveAction, rejectAction string) error
}

// progressFlushInterval is how often batched progress lines are flushed to chat
// as a single digest, so a busy pipeline does not spam the user.
const progressFlushInterval = 4 * time.Second

// agentMention maps an "@prefix" to a specialist agent ID.
var agentMention = map[string]string{
	"@code":        "code-agent",
	"@coder":       "code-agent",
	"@test":        "test-agent",
	"@tester":      "test-agent",
	"@review":      "review-agent",
	"@reviewer":    "review-agent",
	"@coordinator": "coordinator",
}

// TeamBridge connects a Telegram chat to a running agent team. It subscribes to
// the bus, batches progress, surfaces checkpoints as inline buttons, and routes
// "@agent" messages to direct chat.
type TeamBridge struct {
	bot         teamSender
	chatID      int64
	sessionID   string
	checkpoints checkpointResolver
	chat        directChatter

	mu       sync.Mutex
	progress []string // pending progress lines awaiting the next flush
}

// NewTeamBridge constructs a bridge for one chat/session. checkpoints and chat
// may be nil (those features are then unavailable).
func NewTeamBridge(bot teamSender, chatID int64, sessionID string, cp checkpointResolver, chat directChatter) *TeamBridge {
	return &TeamBridge{bot: bot, chatID: chatID, sessionID: sessionID, checkpoints: cp, chat: chat}
}

// Run subscribes to the bus and forwards messages to Telegram until ctx is
// cancelled. Progress lines are batched and flushed on a ticker; tasks,
// results, checkpoints, and agent replies are sent as they arrive.
func (b *TeamBridge) Run(ctx context.Context, bus *a2a.MessageBus) {
	if bus == nil {
		return
	}
	sub, unsub := bus.Subscribe()
	defer unsub()
	ticker := time.NewTicker(progressFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			b.flushProgress(context.Background())
			return
		case <-ticker.C:
			b.flushProgress(ctx)
		case msg, ok := <-sub:
			if !ok {
				return
			}
			b.handle(ctx, msg)
		}
	}
}

// handle dispatches a single bus message to the right Telegram rendering.
func (b *TeamBridge) handle(ctx context.Context, msg a2a.BusMessage) {
	if b.sessionID != "" && msg.SessionID != "" && msg.SessionID != b.sessionID {
		return // not our session
	}
	switch msg.Type {
	case a2a.MsgProgress:
		b.queueProgress(msg)
	case a2a.MsgTask:
		_ = b.bot.SendMessage(ctx, b.chatID, fmt.Sprintf("▶️ *%s* → *%s*\n%s",
			agentLabel(msg.From), agentLabel(msg.To), truncate(msg.Content, 300)))
	case a2a.MsgResult:
		b.flushProgress(ctx)
		_ = b.bot.SendMessage(ctx, b.chatID, fmt.Sprintf("✅ *%s* finished\n%s",
			agentLabel(msg.From), truncate(msg.Content, 600)))
	case a2a.MsgCheckpoint:
		b.flushProgress(ctx)
		b.sendCheckpoint(ctx, msg)
	case a2a.MsgAgent, a2a.MsgDirectChat:
		// Agent replies to the user (e.g. coordinator summaries) are forwarded.
		if msg.To == "user" {
			_ = b.bot.SendMessage(ctx, b.chatID, fmt.Sprintf("*%s:* %s",
				agentLabel(msg.From), msg.Content))
		}
	}
}

// queueProgress appends a progress line for the next digest flush.
func (b *TeamBridge) queueProgress(msg a2a.BusMessage) {
	line := "• " + agentLabel(msg.From) + ": " + truncate(strings.TrimSpace(msg.Content), 120)
	b.mu.Lock()
	b.progress = append(b.progress, line)
	b.mu.Unlock()
}

// flushProgress sends the batched progress lines as a single digest, if any.
func (b *TeamBridge) flushProgress(ctx context.Context) {
	b.mu.Lock()
	lines := b.progress
	b.progress = nil
	b.mu.Unlock()
	if len(lines) == 0 {
		return
	}
	_ = b.bot.SendMessage(ctx, b.chatID, "⏳ Progress\n"+strings.Join(lines, "\n"))
}

// sendCheckpoint surfaces a checkpoint as an approval request with inline
// approve/reject buttons whose callback_data the bridge resolves.
func (b *TeamBridge) sendCheckpoint(ctx context.Context, msg a2a.BusMessage) {
	id := checkpointID(msg)
	desc := "⏸ *Checkpoint* — review before continuing\n" + msg.Content
	if id != "" && b.checkpoints != nil {
		if cp, err := b.checkpoints.Get(id); err == nil && len(cp.Files) > 0 {
			desc += "\n\nFiles:"
			for _, f := range cp.Files {
				tag := "modified"
				if f.IsNew {
					tag = "new"
				}
				desc += fmt.Sprintf("\n📄 %s (%s, %d lines)", f.Path, tag, f.Lines)
			}
		}
	}
	if id == "" {
		_ = b.bot.SendMessage(ctx, b.chatID, desc)
		return
	}
	_ = b.bot.SendApprovalRequest(ctx, b.chatID, desc, "cp:approve:"+id, "cp:reject:"+id)
}

// Resolve handles a checkpoint button tap ("cp:approve:<id>" / "cp:reject:<id>").
// It returns true if it consumed the callback. It satisfies CallbackResolver so
// taps are routed here (via SetTeamCallbackResolver) before the agent runtime.
func (b *TeamBridge) Resolve(callbackData string) bool {
	const prefix = "cp:"
	if !strings.HasPrefix(callbackData, prefix) || b.checkpoints == nil {
		return false
	}
	parts := strings.SplitN(strings.TrimPrefix(callbackData, prefix), ":", 2)
	if len(parts) != 2 {
		return true // malformed but it was ours
	}
	action, id := parts[0], parts[1]
	ctx := context.Background()
	switch action {
	case "approve":
		if err := b.checkpoints.Approve(id); err != nil {
			_ = b.bot.SendMessage(ctx, b.chatID, "⚠️ "+err.Error())
		} else {
			_ = b.bot.SendMessage(ctx, b.chatID, "✅ Approved — continuing.")
		}
	case "reject":
		if err := b.checkpoints.Reject(id, "rejected via Telegram"); err != nil {
			_ = b.bot.SendMessage(ctx, b.chatID, "⚠️ "+err.Error())
		} else {
			_ = b.bot.SendMessage(ctx, b.chatID, "❌ Rejected — pipeline stopped.")
		}
	default:
		return true
	}
	return true
}

// HandleMention routes a message that begins with an "@agent" mention to that
// specialist via direct chat and replies with its answer. It returns true if
// the message was a mention (and therefore handled here, not by the runtime).
func (b *TeamBridge) HandleMention(ctx context.Context, text string) bool {
	mention, rest, ok := splitMention(text)
	if !ok {
		return false
	}
	agentID, known := agentMention[mention]
	if !known {
		_ = b.bot.SendMessage(ctx, b.chatID, "Unknown agent "+mention+". Try @code, @test, @review.")
		return true
	}
	if b.chat == nil {
		_ = b.bot.SendMessage(ctx, b.chatID, "Direct chat is not available.")
		return true
	}
	if strings.TrimSpace(rest) == "" {
		_ = b.bot.SendMessage(ctx, b.chatID, "What would you like to ask "+agentLabel(agentID)+"?")
		return true
	}
	reply, err := b.chat.Chat(ctx, agentID, b.sessionID, rest)
	if err != nil {
		_ = b.bot.SendMessage(ctx, b.chatID, "⚠️ "+err.Error())
		return true
	}
	_ = b.bot.SendMessage(ctx, b.chatID, fmt.Sprintf("*%s:* %s", agentLabel(agentID), reply))
	return true
}

// splitMention splits "@code why sqlite?" into ("@code", "why sqlite?", true).
// A message that does not start with "@" returns ok=false.
func splitMention(text string) (mention, rest string, ok bool) {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "@") {
		return "", "", false
	}
	first, remainder, _ := strings.Cut(t, " ")
	return strings.ToLower(first), strings.TrimSpace(remainder), true
}

// checkpointID extracts the checkpoint ID from a bus message's metadata.
func checkpointID(msg a2a.BusMessage) string {
	if msg.Metadata == nil {
		return ""
	}
	if v, ok := msg.Metadata["checkpoint_id"].(string); ok {
		return v
	}
	if v, ok := msg.Metadata["id"].(string); ok {
		return v
	}
	return ""
}

// agentLabel returns a friendly display name for an agent ID.
func agentLabel(id string) string {
	switch id {
	case "coordinator":
		return "Coordinator"
	case "code-agent":
		return "Code Agent"
	case "test-agent":
		return "Test Agent"
	case "review-agent":
		return "Review Agent"
	case "user":
		return "You"
	case "":
		return "VORTEX"
	default:
		return id
	}
}

// truncate shortens s to at most n runes, appending an ellipsis when cut.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
