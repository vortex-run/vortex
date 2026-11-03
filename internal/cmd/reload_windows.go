//go:build windows

package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// requestReload triggers a config reload on Windows (no SIGHUP) by calling the
// localhost-only POST /internal/reload endpoint on the management API. A
// rejected file or routes that failed to start come back as the error.
func requestReload(_ int, apiPort int) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/internal/reload", apiPort)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		return fmt.Errorf("POST %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		var body struct {
			Error string `json:"error"`
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if json.Unmarshal(data, &body) == nil && body.Error != "" {
			return fmt.Errorf("%s (the server keeps running its previous config)", body.Error)
		}
		return fmt.Errorf("reload endpoint returned %s", resp.Status)
	}
	return nil
}
