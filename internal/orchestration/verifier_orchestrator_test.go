package orchestration

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRun_VerificationPass(t *testing.T) {
	o := newOrch(t, ExecutorFunc(func(_ context.Context, _ *Task, _ *SharedMemory) (string, error) {
		return "STATUS: ok", nil
	}))
	res, err := o.Run(context.Background(), "g", []*Task{
		{ID: "a", Name: "t", AgentType: "general", VerificationCriteria: "prefix:STATUS:"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Completed != 1 || res.Failed != 0 {
		t.Fatalf("completed=%d failed=%d", res.Completed, res.Failed)
	}
	if res.Tasks[0].Meta["verification_status"] != "passed" {
		t.Fatalf("meta = %+v", res.Tasks[0].Meta)
	}
}

func TestRun_VerificationFailTerminal(t *testing.T) {
	o := newOrch(t, ExecutorFunc(func(_ context.Context, _ *Task, _ *SharedMemory) (string, error) {
		return "nope", nil
	}))
	res, err := o.Run(context.Background(), "g", []*Task{
		{ID: "a", AgentType: "general", MaxAttempts: 1, VerificationCriteria: "contains:OK"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 1 || res.Completed != 0 {
		t.Fatalf("completed=%d failed=%d", res.Completed, res.Failed)
	}
	if !strings.Contains(res.Tasks[0].Error, "verification failed") {
		t.Fatalf("error = %q", res.Tasks[0].Error)
	}
	if res.Tasks[0].Meta["verification_status"] != "failed" {
		t.Fatalf("meta = %+v", res.Tasks[0].Meta)
	}
}

func TestRun_VerificationUnsupportedNoRetry(t *testing.T) {
	var calls atomic.Int32
	o := newOrch(t, ExecutorFunc(func(_ context.Context, _ *Task, _ *SharedMemory) (string, error) {
		calls.Add(1)
		return "ok", nil
	}))
	res, err := o.Run(context.Background(), "g", []*Task{
		{ID: "a", AgentType: "general", MaxAttempts: 3, VerificationCriteria: "must succeed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || res.Failed != 1 {
		t.Fatalf("calls=%d failed=%d", calls.Load(), res.Failed)
	}
	if !strings.Contains(res.Tasks[0].Error, "unsupported") {
		t.Fatalf("error = %q", res.Tasks[0].Error)
	}
	if res.Tasks[0].Meta["verification_status"] != "unsupported" {
		t.Fatalf("meta = %+v", res.Tasks[0].Meta)
	}
}

func TestRun_VerificationRetryThenPass(t *testing.T) {
	var calls atomic.Int32
	o := newOrch(t, ExecutorFunc(func(_ context.Context, _ *Task, _ *SharedMemory) (string, error) {
		if calls.Add(1) == 1 {
			return "missing token", nil
		}
		return "contains TOKEN here", nil
	}))
	res, err := o.Run(context.Background(), "g", []*Task{
		{ID: "a", AgentType: "general", MaxAttempts: 2, VerificationCriteria: "contains:TOKEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || res.Completed != 1 || res.Failed != 0 {
		t.Fatalf("calls=%d completed=%d failed=%d", calls.Load(), res.Completed, res.Failed)
	}
}

func TestRun_SkippedDependencyNotVerifiedAsPass(t *testing.T) {
	o := newOrch(t, ExecutorFunc(func(_ context.Context, t *Task, _ *SharedMemory) (string, error) {
		if t.ID == "a" {
			return "", errors.New("upstream fail")
		}
		return "OK", nil
	}))
	res, err := o.Run(context.Background(), "g", []*Task{
		{ID: "a", AgentType: "general", MaxAttempts: 1},
		{ID: "b", AgentType: "general", DependsOn: []string{"a"}, VerificationCriteria: "contains:OK"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var b *Task
	for _, task := range res.Tasks {
		if task.ID == "b" {
			b = task
		}
	}
	if b == nil || b.State != StateFailed || b.Error != "dependency failed" {
		t.Fatalf("skipped task b = %+v", b)
	}
	if b.Meta != nil && b.Meta["verification_status"] == "passed" {
		t.Fatal("skipped task must not be marked verification passed")
	}
}
