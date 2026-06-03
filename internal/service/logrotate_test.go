package service

import (
	"strconv"
	"strings"
	"testing"
)

func TestGenerateLogrotateContainsLogPath(t *testing.T) {
	out := GenerateLogrotate(DefaultLogrotateConfig())
	if !strings.Contains(out, "/var/log/vortex/vortex.log") {
		t.Errorf("output should contain log path:\n%s", out)
	}
}

func TestGenerateLogrotateSize(t *testing.T) {
	cfg := DefaultLogrotateConfig()
	cfg.MaxSize = "250M"
	out := GenerateLogrotate(cfg)
	if !strings.Contains(out, "size 250M") {
		t.Errorf("size directive should match MaxSize:\n%s", out)
	}
}

func TestGenerateLogrotateRotateCount(t *testing.T) {
	cfg := DefaultLogrotateConfig()
	cfg.Rotate = 14
	out := GenerateLogrotate(cfg)
	if !strings.Contains(out, "rotate "+strconv.Itoa(14)) {
		t.Errorf("rotate directive should match Rotate value:\n%s", out)
	}
}

func TestGenerateLogrotateCompress(t *testing.T) {
	out := GenerateLogrotate(DefaultLogrotateConfig())
	if !strings.Contains(out, "compress") {
		t.Errorf("compress should be present when Compress is true:\n%s", out)
	}

	cfg := DefaultLogrotateConfig()
	cfg.Compress = false
	out = GenerateLogrotate(cfg)
	if strings.Contains(out, "compress") {
		t.Errorf("compress should be absent when Compress is false:\n%s", out)
	}
}

func TestGenerateLogrotatePostRotate(t *testing.T) {
	cfg := DefaultLogrotateConfig()
	cfg.PostRotate = "systemctl reload vortex"
	out := GenerateLogrotate(cfg)
	if !strings.Contains(out, "postrotate") {
		t.Errorf("postrotate block missing:\n%s", out)
	}
	if !strings.Contains(out, "systemctl reload vortex") {
		t.Errorf("postrotate command not reflected:\n%s", out)
	}
}

func TestDefaultLogrotateConfig(t *testing.T) {
	cfg := DefaultLogrotateConfig()
	if cfg.LogPath != "/var/log/vortex/vortex.log" {
		t.Errorf("LogPath = %q", cfg.LogPath)
	}
	if cfg.MaxSize != "100M" {
		t.Errorf("MaxSize = %q, want 100M", cfg.MaxSize)
	}
	if cfg.Rotate != 7 {
		t.Errorf("Rotate = %d, want 7", cfg.Rotate)
	}
	if !cfg.Compress {
		t.Error("Compress default should be true")
	}
	if !cfg.DateExt {
		t.Error("DateExt default should be true")
	}
	if cfg.PostRotate != "rc-service vortex reload" {
		t.Errorf("PostRotate = %q", cfg.PostRotate)
	}
}
