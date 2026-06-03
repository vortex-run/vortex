//go:build windows

package cmd

import (
	"fmt"
	"net/http"
	"time"
)

// requestStop triggers shutdown on Windows, which has no SIGTERM, by calling
// the localhost-only POST /internal/shutdown endpoint on the management API.
func requestStop(_ int, apiPort int) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/internal/shutdown", apiPort)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		return fmt.Errorf("POST %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("shutdown endpoint returned %s", resp.Status)
	}
	return nil
}
