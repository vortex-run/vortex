// This file implements the agent-team collaboration API (build plan AG-UI File
// 6): live visibility into agent-to-agent communication, direct chat with a
// named specialist, and human checkpoint review. These back the three-panel
// `vortex code` view and the dashboard.
//
// The handlers depend only on small provider interfaces defined here, so the
// api package stays decoupled from the a2a package (the adapters are wired in
// start.go). All endpoints are data-plane and require an API key.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// CommsRecord is one agent-communication entry exposed over the API. It mirrors
// a2a.BusMessage without importing it.
type CommsRecord struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Type      string    `json:"type"`
	Content   string    `json:"content"`
	SessionID string    `json:"session_id"`
	Timestamp time.Time `json:"timestamp"`
}

// CheckpointRecord is a pending/decided checkpoint exposed over the API.
type CheckpointRecord struct {
	ID          string                 `json:"id"`
	SessionID   string                 `json:"session_id"`
	FromAgent   string                 `json:"from_agent"`
	ToAgent     string                 `json:"to_agent"`
	Title       string                 `json:"title"`
	Description string                 `json:"description"`
	Status      string                 `json:"status"`
	Files       []CheckpointFileRecord `json:"files"`
	CreatedAt   time.Time              `json:"created_at"`
}

// CheckpointFileRecord is one file produced at a checkpoint.
type CheckpointFileRecord struct {
	Path  string `json:"path"`
	Lines int    `json:"lines"`
	IsNew bool   `json:"is_new"`
}

// CommsProvider supplies the agent-communication feed: recent history, a live
// subscription for SSE, and per-agent history. Implemented by an adapter over
// *a2a.MessageBus.
type CommsProvider interface {
	History(limit int) []CommsRecord
	Subscribe() (<-chan CommsRecord, func())
	// AgentMessages returns up to limit recent messages to or from agentID.
	AgentMessages(agentID string, limit int) []CommsRecord
}

// ChatProvider routes a direct-chat message to a named specialist and returns
// its reply. Implemented by an adapter over *a2a.AgentServer.
type ChatProvider interface {
	Chat(ctx context.Context, agentID, sessionID, message string) (string, error)
}

// CheckpointFileEdit is one file edited by the user at a checkpoint.
type CheckpointFileEdit struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// CheckpointProvider lists and resolves human checkpoints. Implemented by an
// adapter over *a2a.CheckpointManager.
type CheckpointProvider interface {
	List() []CheckpointRecord
	Approve(id string) error
	Reject(id, reason string) error
	Edit(id string, edits []CheckpointFileEdit) error
}

// SetCommsProvider wires the agent-communication feed (GET /api/agents/comms,
// /api/agents/comms/stream). Nil yields an empty feed.
func (s *Server) SetCommsProvider(p CommsProvider) { s.comms = p }

// SetChatProvider wires direct chat (POST /api/agents/{id}/chat). Nil yields 503.
func (s *Server) SetChatProvider(p ChatProvider) { s.agentChat = p }

// SetCheckpointProvider wires checkpoint review (GET/POST /api/checkpoints).
// Nil yields an empty list / 503.
func (s *Server) SetCheckpointProvider(p CheckpointProvider) { s.checkpoints = p }

// registerTeamCollab attaches the collaboration routes to the mux. Called from
// NewServer's route table.
func (s *Server) registerTeamCollab(mux *http.ServeMux) {
	mux.Handle("GET /api/agents/team/status", s.requireAPIKey(http.HandlerFunc(s.handleTeamAgents)))
	mux.Handle("GET /api/agents/comms", s.requireAPIKey(http.HandlerFunc(s.handleComms)))
	mux.Handle("GET /api/agents/comms/stream", s.requireAPIKey(http.HandlerFunc(s.handleCommsStream)))
	mux.Handle("GET /api/agents/team/{id}/messages", s.requireAPIKey(http.HandlerFunc(s.handleAgentMessages)))
	mux.Handle("POST /api/agents/{id}/chat", s.requireAPIKey(http.HandlerFunc(s.handleAgentDirectChat)))
	mux.Handle("GET /api/checkpoints", s.requireAPIKey(http.HandlerFunc(s.handleCheckpointsList)))
	mux.Handle("POST /api/checkpoints/{id}/approve", s.requireAPIKey(http.HandlerFunc(s.handleCheckpointApprove)))
	mux.Handle("POST /api/checkpoints/{id}/reject", s.requireAPIKey(http.HandlerFunc(s.handleCheckpointReject)))
	mux.Handle("POST /api/checkpoints/{id}/edit", s.requireAPIKey(http.HandlerFunc(s.handleCheckpointEdit)))
}

// handleComms returns the recent agent-communication feed as JSON.
func (s *Server) handleComms(w http.ResponseWriter, r *http.Request) {
	limit := parseLimit(r.URL.Query().Get("limit"), 100)
	feed := []CommsRecord{}
	if s.comms != nil {
		feed = s.comms.History(limit)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"messages": feed})
}

// handleAgentMessages returns the recent messages to or from one agent — the
// per-agent slice of the comms feed (e.g. a dashboard/TUI drill-down on a
// single specialist). The id path segment names the agent.
func (s *Server) handleAgentMessages(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if agentID == "" {
		http.Error(w, "agent id required", http.StatusBadRequest)
		return
	}
	limit := parseLimit(r.URL.Query().Get("limit"), 100)
	msgs := []CommsRecord{}
	if s.comms != nil {
		msgs = s.comms.AgentMessages(agentID, limit)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"agent_id": agentID, "messages": msgs})
}

// handleCommsStream streams agent-communication messages as Server-Sent Events.
func (s *Server) handleCommsStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	if s.comms == nil {
		http.Error(w, "agent comms not configured", http.StatusServiceUnavailable)
		return
	}
	// Long-lived SSE feed: without this the server's WriteTimeout severs the
	// stream ~60s in, silently dropping events until the client reconnects.
	allowLongResponse(w)
	ch, unsub := s.comms.Subscribe()
	defer unsub()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Replay recent history first so a late subscriber has context.
	for _, rec := range s.comms.History(50) {
		writeCommsEvent(w, rec)
	}
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case rec, open := <-ch:
			if !open {
				return
			}
			writeCommsEvent(w, rec)
			flusher.Flush()
		}
	}
}

// writeCommsEvent writes one comms record as an SSE data frame.
func writeCommsEvent(w http.ResponseWriter, rec CommsRecord) {
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
}

// handleAgentDirectChat routes a direct-chat message to a specialist.
func (s *Server) handleAgentDirectChat(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if s.agentChat == nil {
		http.Error(w, "direct chat not configured", http.StatusServiceUnavailable)
		return
	}
	// Direct chat is an AI generation; it can outlive the 60s WriteTimeout.
	allowLongResponse(w)
	var req struct {
		SessionID string `json:"session_id"`
		Message   string `json:"message"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Message) == "" {
		http.Error(w, "message required", http.StatusBadRequest)
		return
	}
	reply, err := s.agentChat.Chat(r.Context(), agentID, req.SessionID, req.Message)
	if err != nil {
		http.Error(w, "chat failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"agent_id":  agentID,
		"response":  reply,
		"timestamp": time.Now(),
	})
}

// handleCheckpointsList returns pending/decided checkpoints.
func (s *Server) handleCheckpointsList(w http.ResponseWriter, _ *http.Request) {
	list := []CheckpointRecord{}
	if s.checkpoints != nil {
		list = s.checkpoints.List()
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"checkpoints": list})
}

// handleCheckpointApprove approves a pending checkpoint, unblocking the pipeline.
func (s *Server) handleCheckpointApprove(w http.ResponseWriter, r *http.Request) {
	if s.checkpoints == nil {
		http.Error(w, "checkpoints not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	if err := s.checkpoints.Approve(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "approved"})
}

// handleCheckpointReject rejects a pending checkpoint with an optional reason.
func (s *Server) handleCheckpointReject(w http.ResponseWriter, r *http.Request) {
	if s.checkpoints == nil {
		http.Error(w, "checkpoints not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	var req struct {
		Reason string `json:"reason"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	_ = json.NewDecoder(r.Body).Decode(&req) // reason is optional
	if err := s.checkpoints.Reject(id, req.Reason); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "rejected"})
}

// handleCheckpointEdit applies user file edits and resolves the checkpoint as
// edited, unblocking the pipeline with the edited content.
func (s *Server) handleCheckpointEdit(w http.ResponseWriter, r *http.Request) {
	if s.checkpoints == nil {
		http.Error(w, "checkpoints not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	var req struct {
		Files []CheckpointFileEdit `json:"files"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Files) == 0 {
		http.Error(w, "files required", http.StatusBadRequest)
		return
	}
	if err := s.checkpoints.Edit(id, req.Files); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "edited"})
}

// parseLimit parses a positive integer query parameter, falling back to def.
func parseLimit(raw string, def int) int {
	if raw == "" {
		return def
	}
	n := 0
	for _, c := range raw {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	if n <= 0 {
		return def
	}
	return n
}
