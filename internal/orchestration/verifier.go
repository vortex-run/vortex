package orchestration

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnsupportedCriterion marks verification_criteria that is not a supported
// structured form (no free text, shell, or arbitrary code).
var ErrUnsupportedCriterion = errors.New("verification: unsupported criterion")

// VerifyTaskResult checks task output against VerificationCriteria when set.
// Supported forms (case-sensitive on the prefix token):
//   - "" — skipped (no check)
//   - "nonempty" — result must contain non-whitespace
//   - "contains:<substr>" — result must contain substr
//   - "prefix:<substr>" — result must start with substr
func VerifyTaskResult(criteria, result string) error {
	criteria = strings.TrimSpace(criteria)
	if criteria == "" {
		return nil
	}
	switch {
	case criteria == "nonempty":
		if strings.TrimSpace(result) == "" {
			return fmt.Errorf("verification failed: result is empty")
		}
		return nil
	case strings.HasPrefix(criteria, "contains:"):
		sub := strings.TrimPrefix(criteria, "contains:")
		if sub == "" {
			return fmt.Errorf("%w: empty contains substring", ErrUnsupportedCriterion)
		}
		if !strings.Contains(result, sub) {
			return fmt.Errorf("verification failed: result does not contain %q", sub)
		}
		return nil
	case strings.HasPrefix(criteria, "prefix:"):
		sub := strings.TrimPrefix(criteria, "prefix:")
		if sub == "" {
			return fmt.Errorf("%w: empty prefix", ErrUnsupportedCriterion)
		}
		if !strings.HasPrefix(result, sub) {
			return fmt.Errorf("verification failed: result does not have prefix %q", sub)
		}
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnsupportedCriterion, criteria)
	}
}

func isUnsupportedVerification(err error) bool {
	return errors.Is(err, ErrUnsupportedCriterion)
}
