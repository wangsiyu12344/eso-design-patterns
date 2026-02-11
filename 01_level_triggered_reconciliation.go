// Pattern 1: Level-Triggered Reconciliation
//
// This is the MOST IMPORTANT pattern in Kubernetes controller development.
// Instead of reacting to specific events ("Pod was created", "Secret was updated"),
// the reconciler asks: "What is the desired state? What is the current state? Fix the diff."
//
// WHY THIS MATTERS:
//   - Idempotent: calling Reconcile() 100 times with the same state produces the same result
//   - Self-healing: if something breaks midway, the next Reconcile() picks up where it left off
//   - Simple: no need to track event history or build state machines
//
// HOW IT WORKS IN EXTERNAL-SECRETS:
//   The workqueue only passes a name+namespace to Reconcile(). It does NOT pass:
//   - What event happened (create? update? delete?)
//   - The old vs new state
//   - Any event metadata
//
//   So Reconcile() must fetch the current state fresh every time and figure out what to do.
//
// REAL CODE REFERENCE:
//   pkg/controllers/externalsecret/externalsecret_controller.go:159-608

package guide

import (
	"context"
	"fmt"
)

// --- ANTI-PATTERN: Edge-Triggered (DON'T do this) ---
//
// This approach reacts to specific events. It seems intuitive but is fragile:
// - What if the controller crashes between OnCreate and creating the secret?
// - What if events are lost or duplicated?
// - What if someone manually deletes the target secret?
//
// The fundamental flaw: edge-triggered systems encode "what to do" based on
// "what happened." This requires the controller to correctly handle every
// possible sequence of events, including ones caused by crashes, network
// partitions, and concurrent modifications. In practice, this leads to
// increasingly complex state machines that are difficult to reason about
// and impossible to fully test.

type EdgeTriggeredReconciler struct{}

func (r *EdgeTriggeredReconciler) OnCreate(obj MyResource) {
	// "A resource was created, so create a secret"
	// Problem: if this crashes halfway, nobody retries.
	// Even worse, if the informer cache isn't synced yet, `obj` might be stale,
	// and the created secret will be wrong from the start.
	fmt.Println("creating secret for", obj.Name)
}

func (r *EdgeTriggeredReconciler) OnUpdate(oldObj, newObj MyResource) {
	// "A resource was updated, figure out the diff"
	// Problem: you must compute (oldObj vs newObj) diff and handle every field.
	// If you miss a field, the actual state silently drifts from the desired state.
	// Also, if multiple updates arrive rapidly, `oldObj` from one event might not
	// reflect changes made by the previous event's handler.
	fmt.Println("updating secret for", newObj.Name)
}

func (r *EdgeTriggeredReconciler) OnDelete(obj MyResource) {
	// "A resource was deleted, clean up"
	// Problem: if the controller wasn't running when the delete event fired,
	// the event is lost forever. The managed secret becomes an orphan with
	// no mechanism to clean it up (compare with the Finalizer pattern in 03).
	fmt.Println("deleting secret for", obj.Name)
}

// --- CORRECT PATTERN: Level-Triggered (DO this) ---
//
// This approach doesn't care what event happened. It just reads the desired state
// and the actual state, then makes them match.
//
// The name "level-triggered" comes from hardware design:
//   - Edge-triggered: a circuit fires when a signal CHANGES (rising/falling edge)
//   - Level-triggered: a circuit fires when a signal IS at a certain level
//
// In controller terms: we don't react to "what changed" (edge), we react to
// "what IS the current state vs what SHOULD it be" (level). This means that
// even if we miss a dozen events, a single Reconcile() call will fix everything
// because it always reads the latest state from the API server.

type LevelTriggeredReconciler struct {
	client FakeClient
}

// Reconcile is called with ONLY a name+namespace. No event type, no old/new state.
// This is exactly how controller-runtime calls your reconciler.
//
// In external-secrets, this is:
//   externalsecret_controller.go:159
//   func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
func (r *LevelTriggeredReconciler) Reconcile(ctx context.Context, name string, namespace string) error {
	// Step 1: Get the desired state (the ExternalSecret CR)
	// This is always the first thing a level-triggered reconciler does: read
	// the authoritative desired state from the API server. We never trust cached
	// or passed-in objects because they might be stale.
	// If the object doesn't exist (NotFound), it was deleted — nothing to do.
	//
	// Real code: externalsecret_controller.go:173-194
	desired, err := r.client.GetExternalSecret(ctx, name, namespace)
	if err != nil {
		if isNotFound(err) {
			// Object was deleted. Nothing to do.
			// (Cleanup already happened via finalizers - see pattern 03)
			return nil
		}
		return err // retry with backoff
	}

	// Step 2: Get the actual state (the Kubernetes Secret)
	//
	// Real code: externalsecret_controller.go:327-333
	actual, err := r.client.GetSecret(ctx, desired.TargetSecretName, namespace)
	if err != nil && !isNotFound(err) {
		return err
	}

	// Step 3: Check if anything needs to change (Refresh Gating — see Pattern 08)
	// This is a critical optimization. External provider calls are expensive
	// (network I/O, API rate limits, potential costs). If the desired state
	// hasn't changed and the actual secret is still valid, skip the call entirely.
	// In a cluster with 1000 ExternalSecrets, this turns most Reconcile() calls
	// into cheap in-memory comparisons instead of network round-trips.
	//
	// Real code: externalsecret_controller.go:354-357
	if !needsRefresh(desired) && isSecretValid(actual, desired) {
		return nil // everything is in sync, nothing to do
	}

	// Step 4: Fetch from external provider (the expensive operation)
	//
	// Real code: externalsecret_controller.go:399
	data, err := fetchFromProvider(ctx, desired)
	if err != nil {
		return err // retry with backoff
	}

	// Step 5: Make actual state match desired state
	// This is the "convergence" step — the reconciler drives the world toward
	// the desired state. Whether the secret is missing (create) or outdated
	// (update), the end result is the same: actual == desired.
	// In the real code, both paths use a shared mutation function (Pattern 07)
	// to avoid duplicating the "what should the secret look like" logic.
	//
	// Real code: externalsecret_controller.go:535-573
	if actual == nil {
		// Secret doesn't exist yet — create it with data from the provider.
		return r.client.CreateSecret(ctx, desired.TargetSecretName, namespace, data)
	}
	// Secret exists but is out of date — update it to match provider data.
	return r.client.UpdateSecret(ctx, desired.TargetSecretName, namespace, data)
}

// KEY INSIGHT:
// Notice that Reconcile() doesn't know or care:
//   - Whether this was triggered by a create, update, or delete event
//   - Whether this is the first time or the 100th time it's been called
//   - Whether the previous Reconcile() succeeded or failed
//
// It just looks at what IS and what SHOULD BE, then fixes the difference.
// This makes it naturally idempotent and self-healing.

// --- Helper types for illustration ---

type MyResource struct {
	Name             string
	Namespace        string
	TargetSecretName string
	RefreshInterval  int
}

type FakeClient struct{}

func (c FakeClient) GetExternalSecret(ctx context.Context, name, namespace string) (*MyResource, error) {
	return nil, nil
}
func (c FakeClient) GetSecret(ctx context.Context, name, namespace string) (map[string][]byte, error) {
	return nil, nil
}
func (c FakeClient) CreateSecret(ctx context.Context, name, namespace string, data map[string][]byte) error {
	return nil
}
func (c FakeClient) UpdateSecret(ctx context.Context, name, namespace string, data map[string][]byte) error {
	return nil
}

func isNotFound(err error) bool            { return false }
func needsRefresh(r *MyResource) bool      { return true }
func isSecretValid(actual map[string][]byte, desired *MyResource) bool {
	return false
}
func fetchFromProvider(ctx context.Context, r *MyResource) (map[string][]byte, error) {
	return nil, nil
}
