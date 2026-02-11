// Pattern 15: Custom Rate Limiter with Exponential Backoff + Token Bucket
//
// Problem: The default Kubernetes controller rate limiter starts retries at 5ms,
// which is absurdly fast. For a controller that calls external APIs (like a secret
// provider), retrying at 5ms just hammers the provider, burns API quota, and
// likely hits the same transient error. It also generates excessive API server
// load from rapid re-reads of the same resource.
//
// Solution: Build a custom rate limiter that combines TWO strategies:
//   1. Per-item exponential backoff (1s base, 7m max) — for individual failures
//   2. Global token bucket (10/sec, burst 100) — for overall throughput control
//   Then take the MAX (worst-case) of both — this ensures both limits are respected.
//
// REAL CODE REFERENCE:
//   pkg/controllers/common/common.go:98-121

package eso_advanced_patterns

import (
	"fmt"
	"math"
	"time"
)

// =============================================================================
// Anti-Pattern: Using the Default Rate Limiter
// =============================================================================
//
// The default controller-runtime rate limiter:
//   workqueue.DefaultControllerRateLimiter()
//
// Uses: 5ms base delay, 1000s max, with 10 QPS / 100 burst bucket.
// The 5ms base delay means:
//   Failure 1: retry after 5ms
//   Failure 2: retry after 10ms
//   Failure 3: retry after 20ms
//
// For an external API call, 5ms is meaningless. If the API returned an error,
// it's not going to recover in 5ms. You're just wasting API calls and worker time.

// =============================================================================
// Correct Pattern: Reasonable Backoff + Global Rate Limit
// =============================================================================
//
// Real code: pkg/controllers/common/common.go:98-121
//
// Strategy 1: Exponential backoff PER ITEM
//   - Handles failures for individual resources
//   - Formula: delay = baseDelay * 2^failures
//   - baseDelay=1s, maxDelay=7m
//   - Failure 1: 1s, Failure 2: 2s, Failure 3: 4s, ..., caps at 7m
//   - After success, the failure counter resets to 0
//
// Strategy 2: Token bucket GLOBAL
//   - Handles overall throughput — prevents stampede after mass failures
//   - 10 tokens per second, burst of 100
//   - This means: sustained rate of 10 reconciles/sec, can burst to 100
//
// Combination: MaxOf — take the LONGER delay from either strategy
//   - If backoff says "wait 30s" and bucket says "wait 0s" → wait 30s
//   - If backoff says "wait 0s" and bucket says "wait 2s" → wait 2s
//   - This ensures both per-item and global limits are always respected

// ExponentialBackoff simulates per-item exponential backoff.
type ExponentialBackoff struct {
	BaseDelay time.Duration
	MaxDelay  time.Duration
}

func (e *ExponentialBackoff) DelayForFailure(failures int) time.Duration {
	delay := time.Duration(float64(e.BaseDelay) * math.Pow(2, float64(failures)))
	if delay > e.MaxDelay {
		return e.MaxDelay
	}
	return delay
}

// TokenBucket simulates global rate limiting.
type TokenBucket struct {
	Rate  int // tokens per second
	Burst int // max burst size
}

// MaxOfLimiter returns the worst-case (longest) delay from multiple limiters.
// This is the key insight: you don't pick one limiter, you combine them.
func MaxOfDelay(delays ...time.Duration) time.Duration {
	max := time.Duration(0)
	for _, d := range delays {
		if d > max {
			max = d
		}
	}
	return max
}

// BuildRateLimiter creates a custom rate limiter for ESO controllers.
//
// Real code:
//   func BuildRateLimiter() workqueue.TypedRateLimiter[reconcile.Request] {
//       failureRateLimiter := workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](
//           1*time.Second, 7*time.Minute)
//
//       totalRateLimiter := &workqueue.TypedBucketRateLimiter[reconcile.Request]{
//           Limiter: rate.NewLimiter(rate.Limit(10), 100),
//       }
//
//       return workqueue.NewTypedMaxOfRateLimiter[reconcile.Request](
//           failureRateLimiter, totalRateLimiter)
//   }
func demonstrateRateLimiter() {
	backoff := &ExponentialBackoff{
		BaseDelay: 1 * time.Second,
		MaxDelay:  7 * time.Minute,
	}

	// Simulate successive failures for one item
	for i := 0; i < 10; i++ {
		delay := backoff.DelayForFailure(i)
		fmt.Printf("Failure %d: retry after %v\n", i+1, delay)
	}
	// Output:
	// Failure 1: retry after 1s
	// Failure 2: retry after 2s
	// Failure 3: retry after 4s
	// Failure 4: retry after 8s
	// Failure 5: retry after 16s
	// Failure 6: retry after 32s
	// Failure 7: retry after 1m4s
	// Failure 8: retry after 2m8s
	// Failure 9: retry after 4m16s
	// Failure 10: retry after 7m (capped at MaxDelay)
}

// =============================================================================
// Why 1s Base Instead of 5ms?
// =============================================================================
//
// The ESO team chose 1s because:
//   1. External API calls rarely recover in < 1s (rate limits, auth failures, etc.)
//   2. Re-reading the ExternalSecret from the API server just to fail again wastes
//      API server capacity
//   3. Most transient errors (network blips) resolve in 1-5s
//   4. The 7m max cap prevents items from being "lost" in backoff hell
//
// The default 5ms base is designed for in-cluster operations (like updating a
// ConfigMap) where sub-second retries make sense. For external API calls, it's
// just noise.

// =============================================================================
// Why MaxOf Instead of Picking One?
// =============================================================================
//
// Consider a scenario where 500 ExternalSecrets fail simultaneously (e.g., a
// provider outage). Without the global token bucket:
//   - All 500 items retry after 1s
//   - That's 500 API calls in a burst, likely hitting rate limits again
//
// With the token bucket (10/sec, burst 100):
//   - First 100 retry immediately (burst)
//   - Remaining 400 are spread over 40 seconds
//   - Combined with per-item backoff, this creates a gentle recovery curve
//
// MaxOf ensures BOTH limits are respected — you get reasonable per-item backoff
// AND controlled global throughput. Neither limiter can be bypassed.

func init() {
	_ = demonstrateRateLimiter
}
