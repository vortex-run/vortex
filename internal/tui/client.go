package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ConnectionErrorMessage is the user-facing text returned (as the response
// body, not an error) when the TUI cannot reach the server. Returning it as the
// response means the code view renders it inline like an agent message instead
// of failing silently. ConnectionErrorPrefix lets callers detect it.
const (
	ConnectionErrorPrefix  = "✗ Cannot reach VORTEX server"
	ConnectionErrorMessage = ConnectionErrorPrefix + "\n\n" +
		"Start the server first:\n" +
		"  task start:team    (with agent team)\n" +
		"  task start         (single agent mode)\n\n" +
		"Then run: vortex code"
)

// ClientConfig configures the TUI's API client.
type ClientConfig struct {
	BaseURL string // default http://localhost:9090
	APIKey  string
	Timeout time.Duration // default 5s
}

// Client talks to a running VORTEX management server, returning typed data.
type Client struct {
	cfg  ClientConfig
	http *http.Client
}

// NewClient constructs a client with defaults applied.
func NewClient(cfg ClientConfig) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:9090"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}
}

// --- typed response structures ----------------------------------------------

// RouteData is one route's live health (mirrors api.RouteHealth).
type RouteData struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Listen   string `json:"listen"`
	Active   int64  `json:"active"`
}

// HealthData mirrors GET /health.
type HealthData struct {
	Status      string      `json:"status"`
	Version     string      `json:"version"`
	ClusterName string      `json:"cluster_name"`
	Uptime      string      `json:"uptime"`
	ConfigHash  string      `json:"config_hash"`
	Routes      []RouteData `json:"routes"`
}

// StatusData mirrors GET /api/status.
type StatusData struct {
	NodeID        string `json:"node_id"`
	TrustDomain   string `json:"trust_domain"`
	TLSProvider   string `json:"tls_provider"`
	SecretBackend string `json:"secret_backend"`
	PolicyDefault bool   `json:"policy_default"`
	PluginCount   int    `json:"plugin_count"`
	AuditCount    int64  `json:"audit_entry_count"`
	ClusterName   string `json:"cluster_name"`
	Version       string `json:"version"`
	WorkingDir    string `json:"working_dir"`
}

// MetricsData is parsed from the Prometheus /metrics exposition.
type MetricsData struct {
	RequestsTotal  map[string]float64 // by route
	ActiveConns    map[string]float64 // by route
	P99LatencyMs   map[string]float64 // by route
	ClusterMembers float64
}

// AgentsData mirrors GET /api/agents/status.
type AgentsData struct {
	ActiveAgents  int   `json:"active_agents"`
	TotalMessages int64 `json:"total_messages"`
	QueueDepth    int   `json:"queue_depth"`
	// Memory-tier counts (the code view's MEMORY panel).
	Skills   int `json:"skills"`
	Episodes int `json:"episodes"`
	Sessions int `json:"sessions"`
}

// AuditEntryData is one audit entry.
type AuditEntryData struct {
	Seq       int64          `json:"seq"`
	Timestamp string         `json:"timestamp"`
	Actor     string         `json:"actor"`
	Action    string         `json:"action"`
	Resource  string         `json:"resource"`
	Detail    map[string]any `json:"detail"`
}

// AuditData mirrors GET /api/audit.
type AuditData struct {
	Entries []AuditEntryData `json:"entries"`
}

// SecretStatusData is one declared secret's set/unset state.
type SecretStatusData struct {
	Name string `json:"name"`
	Set  bool   `json:"set"`
}

// SecretsData mirrors GET /api/secrets/status.
type SecretsData struct {
	Secrets []SecretStatusData `json:"secrets"`
}

// PluginData is one installed plugin.
type PluginData struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

// PluginsData mirrors GET /api/plugins.
type PluginsData struct {
	Plugins []PluginData `json:"plugins"`
}

// NamespaceData is one tenant namespace.
type NamespaceData struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	OrgID string `json:"org_id"`
}

// NamespacesData mirrors GET /api/namespaces (array or {namespaces:[...]}).
type NamespacesData struct {
	Namespaces []NamespaceData `json:"namespaces"`
}

// ForgeJobData mirrors GET /api/forge/status/{id}.
type ForgeJobData struct {
	ID              string          `json:"id"`
	Message         string          `json:"message"`
	State           string          `json:"state"`
	Progress        string          `json:"progress"`
	ProgressHistory []string        `json:"progress_history"`
	Questions       []ForgeQuestion `json:"questions"`
	Result          string          `json:"result"`
	DurationMs      int64           `json:"duration_ms"`
	Error           string          `json:"error"`
}

// ForgeQuestion is a structured clarifying question (option-selection UI).
type ForgeQuestion struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Key      string   `json:"key"`
}

// --- requests ---------------------------------------------------------------

// newReq builds a request with the API key header set.
func (c *Client) newReq(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if c.cfg.APIKey != "" {
		req.Header.Set("X-API-Key", c.cfg.APIKey)
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// getJSON GETs path and decodes the JSON body into out.
func (c *Client) getJSON(path string, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.Timeout)
	defer cancel()
	req, err := c.newReq(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tui: %s returned %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Health fetches GET /health.
func (c *Client) Health() (*HealthData, error) {
	var d HealthData
	if err := c.getJSON("/health", &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Status fetches GET /api/status.
func (c *Client) Status() (*StatusData, error) {
	var d StatusData
	if err := c.getJSON("/api/status", &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Agents fetches GET /api/agents/status.
func (c *Client) Agents() (*AgentsData, error) {
	var d AgentsData
	if err := c.getJSON("/api/agents/status", &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Audit fetches GET /api/audit?limit=N.
func (c *Client) Audit(limit int) (*AuditData, error) {
	if limit <= 0 {
		limit = 50
	}
	var d AuditData
	if err := c.getJSON("/api/audit?limit="+strconv.Itoa(limit), &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Secrets fetches GET /api/secrets/status.
func (c *Client) Secrets() (*SecretsData, error) {
	var d SecretsData
	if err := c.getJSON("/api/secrets/status", &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Plugins fetches GET /api/plugins.
func (c *Client) Plugins() (*PluginsData, error) {
	var d PluginsData
	if err := c.getJSON("/api/plugins", &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Namespaces fetches GET /api/namespaces (handles array or object form).
func (c *Client) Namespaces() (*NamespacesData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.Timeout)
	defer cancel()
	req, err := c.newReq(ctx, http.MethodGet, "/api/namespaces", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tui: /api/namespaces returned %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	// The endpoint returns a bare array; accept {namespaces:[...]} too.
	var arr []NamespaceData
	if err := json.Unmarshal(raw, &arr); err == nil {
		return &NamespacesData{Namespaces: arr}, nil
	}
	var obj NamespacesData
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	return &obj, nil
}

// ForgeStatus fetches GET /api/forge/status/{id}.
// SessionSummaryData mirrors one entry of GET /api/agents/history.
type SessionSummaryData struct {
	SessionID string `json:"session_id"`
	Summary   string `json:"summary"`
	UpdatedAt string `json:"updated_at"`
}

// SessionMessageData mirrors one message of GET /api/agents/history/{id}.
type SessionMessageData struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// AICostData mirrors GET /api/ai/cost.
type AICostData struct {
	Provider        string  `json:"provider"`
	TotalUSD        float64 `json:"total_usd"`
	RequestsToday   int     `json:"requests_today"`
	DailyBudget     float64 `json:"daily_budget"`
	RemainingBudget float64 `json:"remaining_budget"`
	Free            bool    `json:"free"`
}

// AICost fetches today's AI usage and budget.
func (c *Client) AICost() (*AICostData, error) {
	var d AICostData
	if err := c.getJSON("/api/ai/cost", &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// KeySlotData is one API-key slot's status (autonomous key rotation).
type KeySlotData struct {
	ID            string  `json:"id"`
	Provider      string  `json:"provider"`
	Label         string  `json:"label"`
	Model         string  `json:"model"`
	MaskedKey     string  `json:"masked_key"`
	Priority      int     `json:"priority"`
	Enabled       bool    `json:"enabled"`
	Score         int     `json:"score"`
	RequestsToday int64   `json:"requests_today"`
	ErrorsLast10  int     `json:"errors_last_10"`
	AvgLatencyMs  int64   `json:"avg_latency_ms"`
	SpentTodayUSD float64 `json:"spent_today_usd"`
	DailyBudget   float64 `json:"daily_budget"`
	RateLimited   bool    `json:"rate_limited"`
	Active        bool    `json:"active"`
}

// KeyStatusData is the GET /api/keys/status response.
type KeyStatusData struct {
	Mode     string        `json:"mode"`
	Slots    []KeySlotData `json:"slots"`
	TotalUSD float64       `json:"total_usd"`
}

// KeyStatus fetches the key-rotation slot statuses. An empty slot list means
// single-provider mode.
func (c *Client) KeyStatus() (*KeyStatusData, error) {
	var d KeyStatusData
	if err := c.getJSON("/api/keys/status", &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// History fetches the list of stored conversation sessions.
func (c *Client) History() ([]SessionSummaryData, error) {
	var out struct {
		Sessions []SessionSummaryData `json:"sessions"`
	}
	if err := c.getJSON("/api/agents/history", &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

// SessionHistory fetches the messages of a past session.
func (c *Client) SessionHistory(sessionID string) ([]SessionMessageData, error) {
	var out struct {
		Messages []SessionMessageData `json:"messages"`
	}
	if err := c.getJSON("/api/agents/history/"+sessionID, &out); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

func (c *Client) ForgeStatus(jobID string) (*ForgeJobData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), forgeStatusTimeout)
	defer cancel()
	req, err := c.newReq(ctx, http.MethodGet, "/api/forge/status/"+jobID, nil)
	if err != nil {
		return nil, err
	}
	forgeClient := &http.Client{Timeout: forgeStatusTimeout}
	resp, err := forgeClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tui: forge status returned %d", resp.StatusCode)
	}
	var d ForgeJobData
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, err
	}
	return &d, nil
}

// submitTimeout bounds an agent submit. The coordinator may call a slow
// provider (DeepSeek can take 10–30s) for content generation, so Submit uses a
// much longer deadline than the fast read endpoints.
const submitTimeout = 180 * time.Second

// forgeStatusTimeout bounds a forge status poll. Forge builds can run for
// minutes; the status endpoint itself is fast, but we allow generous headroom.
const forgeStatusTimeout = 300 * time.Second

// Submit POSTs a message to the agent runtime and returns the response. It uses
// a dedicated 180s client/context (not the 5s default) because the agent may
// wait on a slow AI provider.
func (c *Client) Submit(msg, sessionID string) (string, error) {
	body, _ := json.Marshal(map[string]string{"message": msg, "session_id": sessionID})
	ctx, cancel := context.WithTimeout(context.Background(), submitTimeout)
	defer cancel()
	req, err := c.newReq(ctx, http.MethodPost, "/api/agents/submit", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	submitClient := &http.Client{Timeout: submitTimeout}
	resp, err := submitClient.Do(req)
	if err != nil {
		// Server unreachable (connection refused, DNS, timeout): surface a
		// helpful message as the response so the user sees it in the chat panel
		// instead of a silent no-op.
		return ConnectionErrorMessage, nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tui: submit returned %d", resp.StatusCode)
	}
	var out struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Response, nil
}

// Approve posts an approve/reject decision for a pending agent tool action and
// returns the result transcript (the action executes server-side on approval,
// which may call a slow tool, so this uses the long submit timeout).
func (c *Client) Approve(sessionID string, approved bool) (string, error) {
	body, _ := json.Marshal(map[string]any{"session_id": sessionID, "approved": approved})
	ctx, cancel := context.WithTimeout(context.Background(), submitTimeout)
	defer cancel()
	req, err := c.newReq(ctx, http.MethodPost, "/api/agents/approve", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	approveClient := &http.Client{Timeout: submitTimeout}
	resp, err := approveClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tui: approve returned %d", resp.StatusCode)
	}
	var out struct {
		Result string `json:"result"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Result, nil
}

// Notify sends a {title, body} message through the server's messaging router
// (POST /api/notify) — the code view's [T] Telegram forward.
func (c *Client) Notify(title, body string) error {
	payload, _ := json.Marshal(map[string]string{"title": title, "body": body})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := c.newReq(ctx, http.MethodPost, "/api/notify", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tui: notify returned %d", resp.StatusCode)
	}
	return nil
}

// AgentChat sends a direct-chat message to a specific agent and returns its
// reply (POST /api/agents/<id>/chat — the agent-teams collaboration layer).
func (c *Client) AgentChat(agentID, sessionID, message string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"session_id": sessionID, "message": message})
	ctx, cancel := context.WithTimeout(context.Background(), submitTimeout)
	defer cancel()
	req, err := c.newReq(ctx, http.MethodPost, "/api/agents/"+agentID+"/chat", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	chatClient := &http.Client{Timeout: submitTimeout}
	resp, err := chatClient.Do(req)
	if err != nil {
		// Same as Submit: show the offline message inline rather than failing
		// silently in the chat panel.
		return ConnectionErrorMessage, nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tui: agent chat returned %d", resp.StatusCode)
	}
	var out struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Response, nil
}

// Reload triggers a config reload via the control plane.
func (c *Client) Reload() error {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.Timeout)
	defer cancel()
	req, err := c.newReq(ctx, http.MethodPost, "/internal/reload", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tui: reload returned %d", resp.StatusCode)
	}
	return nil
}

// Metrics fetches and parses the Prometheus /metrics exposition.
func (c *Client) Metrics() (*MetricsData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.Timeout)
	defer cancel()
	req, err := c.newReq(ctx, http.MethodGet, "/metrics", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tui: /metrics returned %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	return parsePrometheus(string(raw)), nil
}

// IsConnected reports whether the server responds 200 to /health within 1s.
func (c *Client) IsConnected() bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, err := c.newReq(ctx, http.MethodGet, "/health", nil)
	if err != nil {
		return false
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// APIKeyFilePath returns the path where `vortex setup` persists the plaintext
// API key for the TUI to read back: <user-config>/vortex/tui-key. The apikeys
// store only holds bcrypt hashes (the raw secret is unrecoverable from it), so
// the setup wizard writes the secret here once for the local dashboard/TUI.
func APIKeyFilePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "vortex", "tui-key")
}

// LoadAPIKey resolves an API key, checking in order: the VORTEX_API_KEY env
// var, the setup/TUI key file under the user config dir (APIKeyFilePath), then
// the same file under the user cache dir. It returns the first non-empty value,
// or "" when none is found.
func (c *Client) LoadAPIKey() string {
	if k := os.Getenv("VORTEX_API_KEY"); k != "" {
		return k
	}
	for _, path := range apiKeyFileCandidates() {
		if data, err := os.ReadFile(path); err == nil {
			if k := strings.TrimSpace(string(data)); k != "" {
				return k
			}
		}
	}
	return ""
}

// apiKeyFileCandidates returns the tui-key file locations to check, in order:
// <user-config>/vortex/tui-key then <user-cache>/vortex/tui-key.
func apiKeyFileCandidates() []string {
	paths := []string{APIKeyFilePath()}
	if cache, err := os.UserCacheDir(); err == nil {
		paths = append(paths, filepath.Join(cache, "vortex", "tui-key"))
	}
	return paths
}

// parsePrometheus extracts the metrics the TUI displays from exposition text.
// It reads vortex_requests_total, vortex_active_connections,
// vortex_request_duration_seconds (p99 via _sum/_count or a quantile label is
// out of scope; we expose the per-route counter values), and
// vortex_cluster_members.
func parsePrometheus(text string) *MetricsData {
	d := &MetricsData{
		RequestsTotal: map[string]float64{},
		ActiveConns:   map[string]float64{},
		P99LatencyMs:  map[string]float64{},
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, labels, val, ok := parseMetricLine(line)
		if !ok {
			continue
		}
		route := labels["route"]
		switch name {
		case "vortex_requests_total":
			if route != "" {
				d.RequestsTotal[route] += val
			}
		case "vortex_active_connections":
			if route != "" {
				d.ActiveConns[route] = val
			}
		case "vortex_cluster_members":
			d.ClusterMembers = val
		}
	}
	return d
}

// parseMetricLine parses a single Prometheus sample line into name, labels, and
// value.
func parseMetricLine(line string) (name string, labels map[string]string, val float64, ok bool) {
	labels = map[string]string{}
	// Split metric{labels} value.
	sp := strings.LastIndex(line, " ")
	if sp < 0 {
		return "", nil, 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(line[sp+1:]), 64)
	if err != nil {
		return "", nil, 0, false
	}
	metric := strings.TrimSpace(line[:sp])
	if br := strings.IndexByte(metric, '{'); br >= 0 {
		name = metric[:br]
		labelStr := strings.TrimSuffix(metric[br+1:], "}")
		for _, kv := range strings.Split(labelStr, ",") {
			eq := strings.IndexByte(kv, '=')
			if eq < 0 {
				continue
			}
			k := strings.TrimSpace(kv[:eq])
			val := strings.Trim(strings.TrimSpace(kv[eq+1:]), `"`)
			labels[k] = val
		}
	} else {
		name = metric
	}
	return name, labels, v, true
}
