// Pattern 14: TryLock for Non-Blocking Concurrent Access
//
// Problem: Multiple goroutines (reconcilers) might try to update the same
// external secret simultaneously. With a regular mutex Lock(), the second
// goroutine blocks and waits — wasting a worker thread that could be
// reconciling other resources. In a controller with 1000 resources and
// limited worker goroutines, this can cause head-of-line blocking.
//
// Solution: Use TryLock() instead of Lock(). If the lock is held, return
// an error immediately. The workqueue will retry later with backoff.
// This keeps all worker goroutines productive.
//
// WHY TryLock OVER Lock:
//   - Lock(): goroutine blocks → worker thread wasted → throughput drops
//   - TryLock(): goroutine returns immediately → worker handles next item → retry later
//   - In Kubernetes controllers, the workqueue already handles retry with backoff,
//     so TryLock + return error is the natural fit.
//
// REAL CODE REFERENCE:
//   runtime/util/locks/secret_locks.go:26-61

package eso_advanced_patterns

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// =============================================================================
// Anti-Pattern: Blocking Lock
// =============================================================================
//
// This wastes a worker goroutine by blocking it. In a controller with
// MaxConcurrentReconciles=10, one blocked goroutine means 10% less throughput.
// If several resources share the same provider secret, you can easily get
// 5 out of 10 goroutines blocked — halving your reconciliation speed.

type BlockingSecretAccess struct {
	mu sync.Mutex
}

func (s *BlockingSecretAccess) UpdateSecret(providerName, secretName string) error {
	s.mu.Lock() // ← blocks until available. Worker thread sits idle.
	defer s.mu.Unlock()

	// ... update the secret ...
	return nil
}

// =============================================================================
// Correct Pattern: TryLock with sync.Map for Per-Key Locking
// =============================================================================
//
// Real code: runtime/util/locks/secret_locks.go:26-61
//
// Key design decisions:
//   1. sync.Map for the lock registry — no global lock to look up per-key locks
//   2. LoadOrStore for atomic get-or-create — no race between "check" and "create"
//   3. TryLock returns (unlock func, bool) — caller must call unlock when done
//   4. Sentinel error ErrConflict — callers can use errors.Is() to distinguish
//      "locked" from "something broke"

// ErrConflict signals that a resource is currently being modified by another goroutine.
var ErrConflict = errors.New("unable to access secret since it is locked")

// secretLocks manages per-key locks using sync.Map.
// sync.Map is ideal here because:
//   - Reads are much more common than writes (most keys already exist)
//   - Keys are relatively stable (same secrets are accessed repeatedly)
//   - No need to pre-size or resize a regular map
type secretLocks struct {
	locks sync.Map
}

// Global shared instance — all reconcilers share the same lock set.
var sharedLocks = &secretLocks{}

// TryLock attempts to acquire a lock for a given provider+secret pair.
// Returns an unlock function on success, or ErrConflict if already locked.
//
// Real code: runtime/util/locks/secret_locks.go:33-49
func TryLock(providerName, secretName string) (unlock func(), _ error) {
	// Composite key prevents collisions between different providers
	// that might have secrets with the same name.
	key := fmt.Sprintf("%s#%s", providerName, secretName)
	unlockFunc, ok := sharedLocks.tryLock(key)
	if !ok {
		return nil, fmt.Errorf(
			"failed to acquire lock: provider: %s, secret: %s: %w",
			providerName,
			secretName,
			ErrConflict,
		)
	}
	return unlockFunc, nil
}

// tryLock does the actual lock attempt.
//
// Real code: runtime/util/locks/secret_locks.go:57-61
func (s *secretLocks) tryLock(key string) (func(), bool) {
	// LoadOrStore atomically:
	//   - If key exists: return existing mutex
	//   - If key doesn't exist: store a new mutex and return it
	// No race condition between "check if exists" and "create new".
	lock, _ := s.locks.LoadOrStore(key, &sync.Mutex{})
	mu, _ := lock.(*sync.Mutex)

	// TryLock (Go 1.18+): returns true if lock acquired, false if already held.
	// Unlike Lock(), this never blocks.
	return mu.Unlock, mu.TryLock()
}

// =============================================================================
// Usage in a Reconciler
// =============================================================================
//
// In a Kubernetes controller, TryLock fits perfectly with the workqueue:
//   - TryLock fails → return error → workqueue retries with exponential backoff
//   - TryLock succeeds → do work → unlock → workqueue marks item as done
//
// This means the workqueue's built-in retry mechanism handles contention
// naturally, without any additional coordination logic.

func ReconcileWithTryLock(providerName, secretName string) error {
	// Attempt to acquire the lock — non-blocking
	unlock, err := TryLock(providerName, secretName)
	if err != nil {
		// Return error → workqueue retries with backoff
		// The caller can check errors.Is(err, ErrConflict) to distinguish
		// "someone else is working on this" from "something is broken"
		return err
	}
	defer unlock() // Always release when done

	// Safe to modify the secret — we have exclusive access
	fmt.Printf("updating secret %s/%s\n", providerName, secretName)
	return nil
}

// =============================================================================
// Retry Logic: How the Workqueue Handles TryLock Failures
// =============================================================================
//
// In a real Kubernetes controller, the workqueue automatically retries on error.
// But here we show the explicit retry logic to make the pattern clear.
// This is what controller-runtime's workqueue does under the hood.

// ReconcileWithRetry wraps ReconcileWithTryLock with explicit retry logic.
// In a real controller, you DON'T need to write this — the workqueue does it
// for you. This is here to illustrate what happens when TryLock fails.
func ReconcileWithRetry(providerName, secretName string, maxRetries int) error {
	backoff := NewRetryBackoff(100*time.Millisecond, 10*time.Second, 2.0)

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		err := ReconcileWithTryLock(providerName, secretName)
		if err == nil {
			return nil // success
		}

		// Distinguish "locked by another goroutine" from real errors.
		// Only retry on ErrConflict — other errors should surface immediately.
		if !errors.Is(err, ErrConflict) {
			return err // real error, don't retry
		}

		// Wait with exponential backoff before retrying.
		// In a real controller, you DON'T call time.Sleep — the workqueue's
		// rate limiter handles this. The goroutine goes back to processing
		// other items, and the item is re-enqueued after the delay.
		//
		// We use time.Sleep here only for illustration purposes.
		delay := backoff.NextDelay()
		fmt.Printf("attempt %d: lock conflict for %s/%s, retrying in %v\n",
			attempt+1, providerName, secretName, delay)
		time.Sleep(delay)
		lastErr = err
	}
	return fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}

// =============================================================================
// Exponential Backoff Implementation
// =============================================================================
//
// This mirrors what controller-runtime's workqueue.DefaultItemBasedRateLimiter
// does internally (client-go/util/workqueue/default_rate_limiters.go).
//
// The formula: delay = base * (factor ^ attempt), capped at maxDelay
//
// Example with base=100ms, factor=2, max=10s:
//   Attempt 0: 100ms
//   Attempt 1: 200ms
//   Attempt 2: 400ms
//   Attempt 3: 800ms
//   Attempt 4: 1.6s
//   Attempt 5: 3.2s
//   Attempt 6: 6.4s
//   Attempt 7: 10s  ← capped at maxDelay
//   Attempt 8: 10s  ← stays at max

type RetryBackoff struct {
	base     time.Duration // initial delay (e.g., 100ms)
	maxDelay time.Duration // upper bound (e.g., 10s)
	factor   float64       // multiplier per attempt (e.g., 2.0)
	current  time.Duration // current delay
}

func NewRetryBackoff(base, maxDelay time.Duration, factor float64) *RetryBackoff {
	return &RetryBackoff{
		base:     base,
		maxDelay: maxDelay,
		factor:   factor,
		current:  base,
	}
}

func (b *RetryBackoff) NextDelay() time.Duration {
	delay := b.current

	// Increase for next attempt: current = current * factor
	b.current = time.Duration(float64(b.current) * b.factor)

	// Cap at maxDelay to prevent unbounded growth
	if b.current > b.maxDelay {
		b.current = b.maxDelay
	}

	return delay
}

// Reset resets the backoff to its initial state.
// In a real controller, this is called when the item succeeds:
//   queue.Forget(key)  ← resets the rate limiter for this key
func (b *RetryBackoff) Reset() {
	b.current = b.base
}

// KEY INSIGHT:
// The combination of sync.Map + TryLock + workqueue retry gives you:
//   - Per-key granularity (different secrets can be updated in parallel)
//   - No blocking (TryLock returns immediately)
//   - Automatic retry (workqueue handles it)
//   - No deadlocks (TryLock can't deadlock by definition)
//   - No memory leaks (sync.Map entries are small and bounded by # of secrets)
//
// The retry flow in a Kubernetes controller:
//   1. Reconcile() called → TryLock fails → return ErrConflict
//   2. controller-runtime sees error → calls queue.AddRateLimited(key)
//   3. Rate limiter waits (exponential backoff: 5ms, 10ms, 20ms, ...)
//   4. Item re-queued → Reconcile() called again → TryLock succeeds
//   5. Do work → unlock → queue.Forget(key) (reset backoff)
//
// The goroutine is NEVER idle — between retries it processes other items.

func init() {
	_ = ReconcileWithTryLock
	_ = ReconcileWithRetry
}
