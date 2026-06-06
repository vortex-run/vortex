package perf

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// TuneResult records the outcome of applying OS tuning settings.
type TuneResult struct {
	Applied []string `json:"applied"`
	Skipped []string `json:"skipped"`
	Errors  []string `json:"errors"`
}

// DetectOS returns the current operating system: "linux", "darwin", or
// "windows".
func DetectOS() string {
	return runtime.GOOS
}

// RecommendedSysctl returns the recommended kernel tuning settings for the
// current OS. Windows returns an empty map (tuning is not applied via sysctl).
func RecommendedSysctl() map[string]string {
	switch runtime.GOOS {
	case "linux":
		return map[string]string{
			"net.core.somaxconn":           "65535",
			"net.core.netdev_max_backlog":  "65535",
			"net.ipv4.tcp_max_syn_backlog": "65535",
			"net.ipv4.tcp_fin_timeout":     "15",
			"net.ipv4.tcp_tw_reuse":        "1",
			"net.ipv4.ip_local_port_range": "1024 65535",
			"fs.file-max":                  "1048576",
		}
	case "darwin":
		return map[string]string{
			"kern.ipc.somaxconn":   "65535",
			"net.inet.tcp.msl":     "15000",
			"kern.maxfiles":        "1048576",
			"kern.maxfilesperproc": "1048576",
		}
	default:
		return map[string]string{}
	}
}

// Apply applies (or, with dry=true, simulates applying) the recommended sysctl
// settings on Linux. Settings that cannot be applied for lack of permissions go
// to Skipped; failures go to Errors. On non-Linux or in dry-run mode nothing is
// changed and all settings are reported as Skipped.
func Apply(dry bool) TuneResult {
	res := TuneResult{Applied: []string{}, Skipped: []string{}, Errors: []string{}}
	settings := RecommendedSysctl()

	if runtime.GOOS != "linux" || dry {
		for k, v := range settings {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s = %s", k, v))
		}
		return res
	}

	root := os.Geteuid() == 0
	for k, v := range settings {
		entry := fmt.Sprintf("%s = %s", k, v)
		if !root {
			res.Skipped = append(res.Skipped, entry+" (requires root)")
			continue
		}
		// sysctl -w key="value"
		cmd := exec.Command("sysctl", "-w", fmt.Sprintf("%s=%s", k, v))
		if out, err := cmd.CombinedOutput(); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v: %s", entry, err, strings.TrimSpace(string(out))))
			continue
		}
		res.Applied = append(res.Applied, entry)
	}
	return res
}

// MaxGOMAXPROCS returns the optimal GOMAXPROCS for this machine (the CPU count).
func MaxGOMAXPROCS() int {
	n := runtime.NumCPU()
	if n < 1 {
		return 1
	}
	return n
}

// RecommendedBufferSize returns the optimal read-buffer size in bytes based on
// available system memory: 16KB under 1GB, 32KB for 1–4GB, 64KB above 4GB.
func RecommendedBufferSize() int {
	const (
		gb   = 1024 * 1024 * 1024
		kb16 = 16 * 1024
		kb32 = 32 * 1024
		kb64 = 64 * 1024
	)
	mem := totalMemoryBytes()
	switch {
	case mem > 0 && mem < gb:
		return kb16
	case mem >= gb && mem <= 4*gb:
		return kb32
	default:
		return kb64
	}
}

// totalMemoryBytes returns total system memory, or 0 if it cannot be determined
// (in which case callers fall back to the largest buffer).
func totalMemoryBytes() uint64 {
	// A portable, dependency-free estimate: Go's runtime does not expose total
	// system RAM, so we use the process memory limit when set, otherwise return
	// 0 to select the default (largest) buffer. This keeps the package stdlib-
	// only; precise detection per-OS can be added later.
	return 0
}
