// Pattern 13: Commit/Rollback Transaction Pattern
//
// Problem: A controller operation might involve creating multiple resources
// (e.g., generate a new secret, store state, update references). If step 3
// of 5 fails, the resources from steps 1-2 become orphans — leaking state,
// consuming resources, and confusing users.
//
// Solution: Queue all operations with paired Commit and Rollback functions.
// On success, call Commit() to finalize everything. On failure, call Rollback()
// to clean up partial work. This brings database transaction semantics to
// Kubernetes controller operations.
//
// WHY THIS IS BETTER THAN TRY/CATCH:
//   - Rollback logic is defined alongside the operation, not in a distant error handler
//   - Each operation can have its own cleanup, keeping concerns local
//   - Rollback attempts all cleanups even if some fail (no short-circuiting)
//   - The pattern is explicit — no magic, no hidden state
//
// REAL CODE REFERENCE:
//   runtime/statemanager/statemanager.go:43-162

package eso_advanced_patterns

import (
	"context"
	"errors"
	"fmt"
)

// =============================================================================
// Anti-Pattern: Linear Operations Without Rollback
// =============================================================================
//
// If step 2 fails, the credential from step 1 leaks. In a production system,
// this means orphaned cloud resources that accumulate over time and cost money.

func generateWithoutRollback(ctx context.Context) error {
	cred, err := createCloudCredential(ctx) // step 1: creates a real cloud resource
	if err != nil {
		return err
	}

	err = storeState(ctx, cred) // step 2: if this fails, cred is orphaned!
	if err != nil {
		// You COULD manually clean up here, but as operations grow,
		// this becomes a deeply nested cleanup pyramid:
		//   if err2 := deleteCloudCredential(ctx, cred); err2 != nil { ... }
		// And if step 5 fails, you need to undo steps 1-4.
		return err
	}

	return updateReferences(ctx, cred) // step 3: if this fails, state is inconsistent
}

// =============================================================================
// Correct Pattern: Queue Operations with Commit/Rollback
// =============================================================================
//
// Real code: runtime/statemanager/statemanager.go:43-57

// QueueItem represents a single operation with its rollback.
// Both Commit and Rollback are optional (nil = no-op).
type QueueItem struct {
	Rollback func() error
	Commit   func() error
}

// StateManager queues operations and applies them atomically.
type StateManager struct {
	queue []QueueItem
}

// Enqueue adds an operation to the queue.
// The commit function finalizes the operation.
// The rollback function undoes it on failure.
func (m *StateManager) Enqueue(item QueueItem) {
	m.queue = append(m.queue, item)
}

// Commit applies all queued operations.
// Real code: runtime/statemanager/statemanager.go:95-107
func (m *StateManager) Commit() error {
	var errs []error
	for _, item := range m.queue {
		if item.Commit == nil {
			continue
		}
		if err := item.Commit(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Rollback undoes all queued operations.
// KEY: it tries ALL rollbacks even if some fail. This maximizes cleanup.
// Real code: runtime/statemanager/statemanager.go:81-93
func (m *StateManager) Rollback() error {
	var errs []error
	for _, item := range m.queue {
		if item.Rollback == nil {
			continue
		}
		if err := item.Rollback(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// =============================================================================
// Usage: Generator Lifecycle Management
// =============================================================================
//
// In ESO, generators create cloud resources (e.g., temporary credentials).
// The state manager ensures that:
//   1. If generation succeeds → commit all state
//   2. If generation fails → rollback and clean up orphaned resources
//
// Real code: runtime/statemanager/statemanager.go:128-162

func generateWithRollback(ctx context.Context) error {
	mgr := &StateManager{}

	// Step 1: Create cloud credential
	cred, err := createCloudCredential(ctx)
	if err != nil {
		return err
	}

	// Queue the state storage — with rollback that cleans up the credential
	mgr.Enqueue(QueueItem{
		Commit: func() error {
			// Persist the state so we can track this credential
			return storeState(ctx, cred)
		},
		Rollback: func() error {
			// If something goes wrong later, clean up the credential
			// In the real code, if cleanup fails, it creates a GarbageCollection
			// entry so the credential is eventually cleaned up
			return deleteCloudCredential(ctx, cred)
		},
	})

	// Step 2: Create reference update
	mgr.Enqueue(QueueItem{
		Commit: func() error {
			return updateReferences(ctx, cred)
		},
		Rollback: func() error {
			return removeReferences(ctx, cred)
		},
	})

	// Step 3: Final validation (no rollback needed for read-only ops)
	err = validateFinalState(ctx, cred)
	if err != nil {
		// Something went wrong — rollback everything
		rollbackErr := mgr.Rollback()
		return errors.Join(err, rollbackErr)
	}

	// Everything looks good — commit all state
	return mgr.Commit()
}

// =============================================================================
// Advanced: Garbage Collection as Rollback Fallback
// =============================================================================
//
// In the real ESO code (statemanager.go:145-161), if the Rollback's cleanup
// fails (e.g., network error), it creates a GeneratorState resource with a
// GarbageCollectionDeadline. A separate GC controller will eventually clean it up.
//
// This is a "best effort cleanup with eventual consistency" pattern:
//   1. Try to clean up immediately → success? done.
//   2. Immediate cleanup failed? Create a GC record → eventual cleanup
//   3. GC record creation failed? "We're out of luck :(" (actual comment in code)
//
// This layered approach ensures resources are cleaned up even when the controller
// crashes during rollback.

// --- Helper functions for illustration ---

type Credential struct{ ID string }

func createCloudCredential(ctx context.Context) (*Credential, error) {
	return &Credential{ID: "cred-123"}, nil
}
func deleteCloudCredential(ctx context.Context, c *Credential) error {
	// IMPORTANT: Rollback functions MUST be idempotent.
	// If the credential doesn't exist (already deleted, or creation partially failed),
	// treat it as success — not an error.
	// In real code: call cloud API delete, and ignore "NotFound" / "404" errors.
	//   err := cloudClient.DeleteCredential(c.ID)
	//   if err != nil && !isNotFound(err) {
	//       return err
	//   }
	fmt.Printf("deleting credential %s (no-op if not found)\n", c.ID)
	return nil
}
func storeState(ctx context.Context, c *Credential) error { return nil }
func updateReferences(ctx context.Context, c *Credential) error { return nil }
func removeReferences(ctx context.Context, c *Credential) error {
	// IMPORTANT: Idempotent — if references were never created
	// (because Commit never ran), this is a no-op, not an error.
	//   err := k8sClient.Delete(ctx, ref)
	//   if apierrors.IsNotFound(err) {
	//       return nil  // already gone or never existed — that's fine
	//   }
	fmt.Printf("removing references for %s (no-op if not found)\n", c.ID)
	return nil
}
func validateFinalState(ctx context.Context, c *Credential) error {
	// PURPOSE: Verify the preconditions that Commit() depends on.
	// Each check here maps to a Commit operation that would fail or produce
	// invalid state if the precondition isn't met.
	//
	// Commit operation              → What we validate here
	// ──────────────────────────────────────────────────────────────
	// storeState(ctx, cred)         → credential is active and usable
	//                                 (no point persisting a dead credential)
	// updateReferences(ctx, cred)   → credential has correct permissions
	//                                 (references to a broken cred = broken secrets)
	//
	// If validation fails → Rollback (clean up the credential we created)
	// If validation passes → Commit (safe to persist state + update references)

	// 1. Precondition for storeState: credential must be active
	//    If the cloud provider hasn't finished provisioning it yet,
	//    committing would persist a reference to a non-functional credential.
	if err := verifyCredentialActive(ctx, c); err != nil {
		return fmt.Errorf("credential %s not active: %w", c.ID, err)
	}

	// 2. Precondition for updateReferences: credential must actually work
	//    If we can't auth with it, updating references would point
	//    ExternalSecrets at a credential that can't fetch anything.
	if err := testCredentialAccess(ctx, c); err != nil {
		return fmt.Errorf("credential %s access test failed: %w", c.ID, err)
	}

	return nil
}

func verifyCredentialActive(ctx context.Context, c *Credential) error {
	// In real code: call cloud provider API to check credential status
	// e.g., AWS IAM GetAccessKeyLastUsed, GCP ServiceAccountKey get
	// Returns error if credential is in "Inactive" or "Creating" state
	fmt.Printf("verifying credential %s is active\n", c.ID)
	return nil
}

func testCredentialAccess(ctx context.Context, c *Credential) error {
	// In real code: attempt a lightweight operation using the credential
	// e.g., STS GetCallerIdentity for AWS, or a token introspection for GCP
	// This ensures the credential actually works before we commit it into state
	fmt.Printf("testing access with credential %s\n", c.ID)
	return nil
}

func init() {
	_ = generateWithoutRollback
	_ = generateWithRollback
}
