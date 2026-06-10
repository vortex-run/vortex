package devops

import (
	"context"
	"fmt"
	"strings"
)

// fileWriter is the subset of *SSHClient used to write remote files. The Server
// runner satisfies it via *SSHClient; tests stub it.
type fileWriter interface {
	WriteRemote(ctx context.Context, remotePath string, data []byte) error
}

// NginxManager manages Nginx on a remote server over SSH.
type NginxManager struct {
	server *Server
	writer fileWriter
}

// NewNginxManager constructs a manager over a Server. The server's SSH client
// must implement fileWriter (the real *SSHClient does).
func NewNginxManager(server *Server) *NginxManager {
	m := &NginxManager{server: server}
	if w, ok := server.ssh.(fileWriter); ok {
		m.writer = w
	}
	return m
}

// Status returns the systemctl status of nginx.
func (n *NginxManager) Status(ctx context.Context) (string, error) {
	return n.server.ServiceStatus(ctx, "nginx")
}

// Reload tests the config then reloads nginx (approval-gated).
func (n *NginxManager) Reload(ctx context.Context) error {
	if !n.server.approve("reload nginx") {
		return fmt.Errorf("devops: nginx reload not approved")
	}
	_, stderr, code, err := n.server.ssh.Run(ctx, "nginx -t && systemctl reload nginx")
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("devops: nginx reload failed (exit %d): %s", code, strings.TrimSpace(stderr))
	}
	return nil
}

// AddSite writes a reverse-proxy site config, enables it, and reloads nginx
// (approval-gated).
func (n *NginxManager) AddSite(ctx context.Context, domain, upstream string, sslEnabled bool) error {
	if !n.server.approve("add nginx site " + domain) {
		return fmt.Errorf("devops: add site not approved")
	}
	if n.writer == nil {
		return fmt.Errorf("devops: nginx file writer not available")
	}
	conf := siteConfig(domain, upstream)
	avail := "/etc/nginx/sites-available/" + domain
	enabled := "/etc/nginx/sites-enabled/" + domain
	if err := n.writer.WriteRemote(ctx, avail, []byte(conf)); err != nil {
		return fmt.Errorf("devops: writing site config: %w", err)
	}
	cmd := fmt.Sprintf("ln -sf %s %s && nginx -t && systemctl reload nginx", shellQuote(avail), shellQuote(enabled))
	_, stderr, code, err := n.server.ssh.Run(ctx, cmd)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("devops: enabling site failed (exit %d): %s", code, strings.TrimSpace(stderr))
	}
	_ = sslEnabled // SSL is provisioned separately via EnableSSL
	return nil
}

// EnableSSL provisions a Let's Encrypt cert for domain via certbot (approval).
func (n *NginxManager) EnableSSL(ctx context.Context, domain, email string) error {
	if !n.server.approve("enable SSL for " + domain) {
		return fmt.Errorf("devops: enable SSL not approved")
	}
	cmd := fmt.Sprintf(
		"command -v certbot >/dev/null 2>&1 || (apt-get update && apt-get install -y certbot python3-certbot-nginx); "+
			"certbot --nginx -d %s --email %s --non-interactive --agree-tos",
		shellQuote(domain), shellQuote(email))
	_, stderr, code, err := n.server.ssh.Run(ctx, cmd)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("devops: certbot failed (exit %d): %s", code, strings.TrimSpace(stderr))
	}
	return nil
}

// ListSites returns the enabled site names.
func (n *NginxManager) ListSites(ctx context.Context) ([]string, error) {
	out, _, _, err := n.server.ssh.Run(ctx, "ls /etc/nginx/sites-enabled/ 2>/dev/null")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// RemoveSite disables a site config and reloads nginx (approval-gated).
func (n *NginxManager) RemoveSite(ctx context.Context, domain string) error {
	if !n.server.approve("remove nginx site " + domain) {
		return fmt.Errorf("devops: remove site not approved")
	}
	cmd := fmt.Sprintf("rm -f /etc/nginx/sites-enabled/%s && systemctl reload nginx", shellQuote(domain))
	_, stderr, code, err := n.server.ssh.Run(ctx, cmd)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("devops: remove site failed (exit %d): %s", code, strings.TrimSpace(stderr))
	}
	return nil
}

// siteConfig renders an nginx reverse-proxy server block.
func siteConfig(domain, upstream string) string {
	return fmt.Sprintf(`server {
    listen 80;
    server_name %s;
    location / {
        proxy_pass %s;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }
}
`, domain, upstream)
}
