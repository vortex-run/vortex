//go:build !windows && !unix

package perf

// processCPUSeconds reports no data on platforms without process CPU times;
// the sampler then always returns 0 and the autoscaler holds MinNodes.
func processCPUSeconds() (float64, bool) {
	return 0, false
}
