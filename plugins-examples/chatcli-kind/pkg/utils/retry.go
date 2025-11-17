package utils

import (
	"fmt"
	"time"
)

// RetryWithBackoff executa uma função com retry exponencial
func RetryWithBackoff(attempts int, initialDelay time.Duration, maxDelay time.Duration, fn func() error) error {
	delay := initialDelay
	var lastErr error

	for i := 0; i < attempts; i++ {
		if i > 0 {
			Logf("   🔄 Retry attempt %d/%d (waiting %v)...\n", i+1, attempts, delay)
			time.Sleep(delay)

			// Backoff exponencial
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}

		err := fn()
		if err == nil {
			return nil
		}

		lastErr = err
		Logf("   ⚠️  Attempt %d failed: %v\n", i+1, err)
	}

	return fmt.Errorf("all %d attempts failed. Last error: %w", attempts, lastErr)
}
