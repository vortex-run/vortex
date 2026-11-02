package orchestration

import (
	"errors"
	"strings"
	"testing"
)

func TestVerifyTaskResult_Pass(t *testing.T) {
	cases := []struct {
		criteria, result string
	}{
		{"", "anything"},
		{"nonempty", "x"},
		{"contains:ok", "hello ok world"},
		{"prefix:STATUS:", "STATUS: done"},
	}
	for _, tc := range cases {
		if err := VerifyTaskResult(tc.criteria, tc.result); err != nil {
			t.Errorf("VerifyTaskResult(%q, %q) = %v, want nil", tc.criteria, tc.result, err)
		}
	}
}

func TestVerifyTaskResult_Fail(t *testing.T) {
	cases := []struct {
		criteria, result string
		wantSub          string
	}{
		{"nonempty", "   ", "empty"},
		{"contains:needle", "haystack", "does not contain"},
		{"prefix:OK:", "FAIL: x", "does not have prefix"},
	}
	for _, tc := range cases {
		err := VerifyTaskResult(tc.criteria, tc.result)
		if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
			t.Errorf("VerifyTaskResult(%q, %q) = %v, want error containing %q", tc.criteria, tc.result, err, tc.wantSub)
		}
		if isUnsupportedVerification(err) {
			t.Errorf("expected verification failure, not unsupported: %v", err)
		}
	}
}

func TestVerifyTaskResult_Unsupported(t *testing.T) {
	cases := []string{
		"must succeed",
		"tests pass",
		"contains:",
		"prefix:",
		"shell: echo hi",
	}
	for _, c := range cases {
		err := VerifyTaskResult(c, "ok")
		if !isUnsupportedVerification(err) {
			t.Errorf("VerifyTaskResult(%q) = %v, want ErrUnsupportedCriterion", c, err)
		}
	}
}

func TestVerifyTaskResult_UnsupportedIsDistinct(t *testing.T) {
	if !errors.Is(VerifyTaskResult("free text", "x"), ErrUnsupportedCriterion) {
		t.Fatal("expected errors.Is with ErrUnsupportedCriterion")
	}
}
