package devops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Container is a Docker container summary.
type Container struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Image  string `json:"image"`
	Status string `json:"status"`
	Ports  string `json:"ports"`
}

// ContainerStats is a container's live resource usage.
type ContainerStats struct {
	Name   string `json:"name"`
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}

// DockerManager runs docker commands on a remote server over SSH.
type DockerManager struct {
	server *Server
}

// NewDockerManager constructs a manager over a Server.
func NewDockerManager(server *Server) *DockerManager {
	return &DockerManager{server: server}
}

// ListContainers runs `docker ps` and parses the JSON-per-line output.
func (d *DockerManager) ListContainers(ctx context.Context) ([]Container, error) {
	out, stderr, code, err := d.server.ssh.Run(ctx, `docker ps --format '{{json .}}'`)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("devops: docker ps failed: %s", strings.TrimSpace(stderr))
	}
	var containers []Container
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw struct {
			ID, Names, Image, Status, Ports string
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue // skip unparseable lines
		}
		containers = append(containers, Container{
			ID: raw.ID, Name: raw.Names, Image: raw.Image, Status: raw.Status, Ports: raw.Ports,
		})
	}
	return containers, nil
}

// StartContainer starts a container (approval-gated).
func (d *DockerManager) StartContainer(ctx context.Context, name string) error {
	return d.mutate(ctx, "start container "+name, "docker start "+shellQuote(name))
}

// StopContainer stops a container (approval-gated).
func (d *DockerManager) StopContainer(ctx context.Context, name string) error {
	return d.mutate(ctx, "stop container "+name, "docker stop "+shellQuote(name))
}

// Logs returns the last `lines` of a container's logs.
func (d *DockerManager) Logs(ctx context.Context, name string, lines int) (string, error) {
	if lines <= 0 {
		lines = 100
	}
	out, stderr, _, err := d.server.ssh.Run(ctx, fmt.Sprintf("docker logs --tail=%d %s 2>&1", lines, shellQuote(name)))
	if err != nil {
		return "", err
	}
	if out == "" {
		out = stderr
	}
	return out, nil
}

// Pull pulls an image (approval-gated).
func (d *DockerManager) Pull(ctx context.Context, image string) error {
	return d.mutate(ctx, "pull image "+image, "docker pull "+shellQuote(image))
}

// RunContainer runs a detached container with the given ports/env (approval).
func (d *DockerManager) RunContainer(ctx context.Context, image, name string, ports, envVars map[string]string) error {
	var b strings.Builder
	b.WriteString("docker run -d --name " + shellQuote(name))
	for host, container := range ports {
		b.WriteString(" -p " + shellQuote(host+":"+container))
	}
	for k, v := range envVars {
		b.WriteString(" -e " + shellQuote(k+"="+v))
	}
	b.WriteString(" " + shellQuote(image))
	return d.mutate(ctx, "run container "+name+" from "+image, b.String())
}

// Stats runs `docker stats --no-stream` and parses CPU/memory per container.
func (d *DockerManager) Stats(ctx context.Context) ([]ContainerStats, error) {
	out, stderr, code, err := d.server.ssh.Run(ctx, `docker stats --no-stream --format '{{json .}}'`)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("devops: docker stats failed: %s", strings.TrimSpace(stderr))
	}
	var stats []ContainerStats
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw struct {
			Name, CPUPerc, MemUsage string
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		stats = append(stats, ContainerStats{Name: raw.Name, CPU: raw.CPUPerc, Memory: raw.MemUsage})
	}
	return stats, nil
}

// mutate runs an approval-gated docker command and checks its exit code.
func (d *DockerManager) mutate(ctx context.Context, action, cmd string) error {
	if !d.server.approve(action) {
		return fmt.Errorf("devops: %s not approved", action)
	}
	_, stderr, code, err := d.server.ssh.Run(ctx, cmd)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("devops: %s failed (exit %d): %s", action, code, strings.TrimSpace(stderr))
	}
	return nil
}
