// Pattern 4: Workqueue Deduplication + Rate Limiting
//
// Problem: In a busy cluster, the same object might be modified many times per second.
// If every modification triggers a full Reconcile(), you waste resources and may
// overwhelm external APIs (AWS, Vault, etc.).
//
// Solution: The workqueue provides three critical features:
//   1. Deduplication: same key enqueued 10 times → only 1 Reconcile() call
//   2. Rate limiting: failed reconciles use exponential backoff
//   3. Delayed requeue: Reconcile() can request "call me again in 1 hour"
//
// HOW IT WORKS:
//   - The informer receives Watch events and enqueues {name, namespace} into the workqueue
//   - The workqueue deduplicates: if "default/my-es" is already in the queue, adding it again is a no-op
//   - Worker goroutines dequeue items and call Reconcile()
//   - Based on the result, the item is forgotten, requeued with backoff, or requeued after a delay
//
// REAL CODE REFERENCE:
//   controller-runtime/pkg/internal/controller/controller.go:259-313  (Start, worker loop)
//   controller-runtime/pkg/internal/controller/controller.go:403-423  (processNextWorkItem)
//   controller-runtime/pkg/internal/controller/controller.go:444-495  (reconcileHandler)

package guide

import (
	"fmt"
	"time"
)

// =============================================================================
// The Workqueue Behavior
// =============================================================================

// Scenario: ExternalSecret "my-es" is updated 5 times in 100ms
//
// Without deduplication:
//   Reconcile("my-es") called 5 times → 5 calls to AWS Secrets Manager
//
// With deduplication (how it actually works):
//   Event 1: enqueue("default/my-es") → queue: ["default/my-es"]
//   Event 2: enqueue("default/my-es") → queue: ["default/my-es"] (already there, no-op)
//   Event 3: enqueue("default/my-es") → queue: ["default/my-es"] (already there, no-op)
//   Event 4: enqueue("default/my-es") → queue: ["default/my-es"] (already there, no-op)
//   Event 5: enqueue("default/my-es") → queue: ["default/my-es"] (already there, no-op)
//   Worker dequeues → Reconcile("my-es") called ONCE → 1 call to AWS
//
// This works because of level-triggered reconciliation (Pattern 1):
// Reconcile() always reads the LATEST state, so it doesn't matter which
// event triggered it. The most recent state is always used.
//
// The deduplication is key to making level-triggered reconciliation efficient.
// Without it, a burst of updates to the same object would cause redundant work.
// With it, no matter how many events fire, only ONE reconcile runs, and it
// always converges to the correct state because it reads the current truth.

// =============================================================================
// Result Handling After Reconcile()
// =============================================================================
//
// Real code: controller.go:444-495 (reconcileHandler)
//
//   result, err := c.Reconcile(ctx, req)
//   switch {
//   case err != nil:
//       c.Queue.AddWithOpts(priorityqueue.AddOpts{RateLimited: true}, req)  // backoff retry
//   case result.RequeueAfter > 0:
//       c.Queue.Forget(req)
//       c.Queue.AddWithOpts(priorityqueue.AddOpts{After: result.RequeueAfter}, req)
//   case result.Requeue:
//       c.Queue.AddWithOpts(priorityqueue.AddOpts{RateLimited: true}, req)
//   default:
//       c.Queue.Forget(req)  // success, remove from retry tracking
//   }

type ReconcileResult struct {
	Requeue      bool
	RequeueAfter time.Duration
}

func ExampleResultHandling() {
	// CASE 1: Success — provider returned data, secret updated
	// Real code: externalsecret_controller.go:607
	//   return r.getRequeueResult(externalSecret), nil
	//
	// The item is "forgotten" (removed from retry tracking), which resets its
	// exponential backoff counter. It's then requeued after the refresh interval
	// (e.g. 1 hour). The distinction between Forget+RequeueAfter vs just Requeue
	// is important: Forget clears the failure count, so the next failure starts
	// backoff from scratch rather than continuing from a high delay.
	_ = ReconcileResult{RequeueAfter: 1 * time.Hour}
	fmt.Println("Success → requeue after 1 hour for periodic refresh")

	// CASE 2: Transient error — provider unavailable, network timeout
	// Real code: externalsecret_controller.go:402
	//   return ctrl.Result{}, err
	//
	// The item is requeued with EXPONENTIAL BACKOFF:
	//   Attempt 1: retry after ~5ms
	//   Attempt 2: retry after ~10ms
	//   Attempt 3: retry after ~20ms
	//   ...
	//   Attempt N: retry after ~max (usually 1000s)
	//
	// The backoff prevents "thundering herd" problems: if an external provider
	// goes down, all ExternalSecrets targeting it would fail and retry. Without
	// backoff, they'd all hammer the provider simultaneously as soon as it comes
	// back up, potentially causing it to fail again.
	fmt.Println("Error → requeue with exponential backoff")

	// CASE 3: Permanent error — configuration issue, can't be fixed by retrying
	// Real code: externalsecret_controller.go:585
	//   return ctrl.Result{}, nil   // return nil error so it doesn't retry
	//
	// The item is forgotten. No retry. The status is updated to show the error.
	// A new Reconcile() will only happen if the resource is modified.
	//
	// Notice the subtle trick: returning nil as the error tells the workqueue
	// "this succeeded" even though the reconciliation logically failed. The
	// controller uses status conditions (Pattern 10) to communicate the error
	// to users. This avoids wasting CPU on retries that can never succeed.
	_ = ReconcileResult{}
	fmt.Println("Permanent error → don't retry, wait for user to fix config")

	// CASE 4: Immediate requeue — cache not in sync yet
	// Real code: externalsecret_controller.go:319
	//   return ctrl.Result{Requeue: true}, nil
	//
	// The item is requeued immediately (with rate limiting).
	_ = ReconcileResult{Requeue: true}
	fmt.Println("Requeue → retry immediately (rate limited)")
}

// =============================================================================
// The Worker Loop
// =============================================================================
//
// Real code: controller.go:290-299
//
//   for i := 0; i < c.MaxConcurrentReconciles; i++ {
//       go func() {
//           for c.processNextWorkItem(ctx) {
//           }
//       }()
//   }
//
// Each worker runs an infinite loop:
//   1. queue.Get() — BLOCKS until an item is available
//   2. Reconcile(item)
//   3. Handle result (forget, requeue, backoff)
//   4. queue.Done(item) — marks item as processed (allows re-enqueue)
//   5. Go back to step 1
//
// The queue.Done() call is critical: while an item is being processed (between
// Get and Done), the queue prevents other workers from picking up the same key.
// However, if a new event for the same key arrives during processing, the queue
// remembers it and re-enqueues the key after Done() is called. This guarantees
// that the latest state will always be reconciled, without concurrent access.

func ExampleWorkerLoop() {
	// With --concurrent=5, there are 5 worker goroutines.
	// Each one independently dequeues and reconciles.
	//
	// The workqueue guarantees that the SAME key is never processed
	// by two workers simultaneously. If "default/my-es" is being
	// processed by worker 1, worker 2 will skip it and pick the next item.
	//
	// This prevents race conditions where two goroutines try to
	// update the same Secret at the same time.

	fmt.Println("Worker 1: processing default/my-es-1")
	fmt.Println("Worker 2: processing default/my-es-2")
	fmt.Println("Worker 3: waiting... (queue empty, blocked on Get())")
	fmt.Println("Worker 4: waiting... (queue empty, blocked on Get())")
	fmt.Println("Worker 5: waiting... (queue empty, blocked on Get())")
}

// KEY INSIGHT:
// The workqueue is what makes the controller scalable and resilient:
//   - 1000 events in 1 second? Deduplicated to maybe 50 Reconcile() calls
//   - Provider down? Exponential backoff prevents hammering
//   - Need periodic refresh? RequeueAfter handles it without external cron jobs
//   - Multiple workers? Same key never processed concurrently
