package supervisor

import "time"

// Backoff returns the delay before restart attempt n (0-based):
// initial, 2*initial, 4*initial, ... capped at max.
func Backoff(initial, max time.Duration, attempt int) time.Duration {
	if initial <= 0 {
		return 0
	}
	d := initial
	for i := 0; i < attempt; i++ {
		if d >= max/2 {
			return max
		}
		d *= 2
	}
	if d > max {
		return max
	}
	return d
}
