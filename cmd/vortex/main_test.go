package main

import "testing"

func TestRunVersionExitsZero(t *testing.T) {
	if code := run([]string{"--version"}); code != 0 {
		t.Errorf("run --version exit code = %d, want 0", code)
	}
}

func TestRunBootExitsZero(t *testing.T) {
	if code := run([]string{"--log-level", "error"}); code != 0 {
		t.Errorf("run boot exit code = %d, want 0", code)
	}
}

func TestRunRejectsUnknownFlag(t *testing.T) {
	if code := run([]string{"--nope"}); code != 2 {
		t.Errorf("run with unknown flag exit code = %d, want 2", code)
	}
}
