package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// AgentRuntime is the subset of the agent runtime the API needs. It is
// satisfied by *agents.Runtime; declaring it here keeps the api package
// decoupled from the agents package.
type AgentRuntime interface {
	Submit(ctx context.Context, userMsg, sessionID string) (<-chan string, error)
	Stats() AgentRuntimeStats
}

// AgentRuntimeStats mirrors the runtime's stats for the API. The wiring in
// start.go adapts agents.RuntimeStats into this type.
type AgentRuntimeStats struct {
	ActiveAgents  int   `json:"active_agents"`
	TotalMessages int64 `json:"total_messages"`
	QueueDepth    int   `json:"queue_depth"`
}

// SetAgentRuntime wires the agent runtime backing the /api/agents endpoints.
// When nil, those endpoints return 503.
func (s *Server) SetAgentRuntime(rt AgentRuntime) { s.agentRuntime = rt }

// agentSubmitRequest is the POST /api/agents/submit body.
type agentSubmitRequest struct {
	Message   string `json:"message"`
	SessionID string `json:"session_id"`
}

// agentSubmitResponse is the JSON (non-SSE) reply.
type agentSubmitResponse struct {
	Response  string `json:"response"`
	SessionID string `json:"session_id"`
}

// handleAgentSubmit submits a user message to the coordinator and returns its
// response. If the client sends Accept: text/event-stream, response chunks are
// streamed as Server-Sent Events; otherwise a single JSON object is returned.
func (s *Server) handleAgentSubmit(w http.ResponseWriter, r *http.Request) {
	if s.agentRuntime == nil {
		http.Error(w, "agent runtime not configured", http.StatusServiceUnavailable)
		return
	}
	var req agentSubmitRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.Message == "" {
		http.Error(w, "message is required", http.StatusBadRequest)
		return
	}

	ch, err := s.agentRuntime.Submit(r.Context(), req.Message, req.SessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if wantsSSE(r) {
		s.streamAgentSSE(w, r, ch)
		return
	}

	// Non-streaming: collect the (single) response.
	var resp string
	for chunk := range ch {
		resp += chunk
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(agentSubmitResponse{Response: resp, SessionID: req.SessionID}); err != nil {
		s.log.Error("encoding agent submit response", "err", err)
	}
}

// streamAgentSSE writes response chunks as Server-Sent Events until the channel
// closes, then sends a terminal "done" event.
func (s *Server) streamAgentSSE(w http.ResponseWriter, r *http.Request, ch <-chan string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	for {
		select {
		case <-r.Context().Done():
			return
		case chunk, open := <-ch:
			if !open {
				fmt.Fprint(w, "event: done\ndata: {}\n\n")
				flusher.Flush()
				return
			}
			data, _ := json.Marshal(map[string]string{"chunk": chunk})
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// handleAgentStatus returns the runtime stats.
func (s *Server) handleAgentStatus(w http.ResponseWriter, _ *http.Request) {
	if s.agentRuntime == nil {
		http.Error(w, "agent runtime not configured", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s.agentRuntime.Stats()); err != nil {
		s.log.Error("encoding agent status", "err", err)
	}
}

// wantsSSE reports whether the client requested Server-Sent Events.
func wantsSSE(r *http.Request) bool {
	return r.Header.Get("Accept") == "text/event-stream"
}
