package scenario

import (
	"fmt"
	"time"
)

// parseStepTimeout parses a duration string, returning defaultTimeout if s is empty.
func parseStepTimeout(s string, defaultTimeout time.Duration) (time.Duration, error) {
	if s == "" {
		return defaultTimeout, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	return d, nil
}
