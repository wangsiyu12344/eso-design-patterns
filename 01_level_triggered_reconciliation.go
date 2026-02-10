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

type EdgeTriggeredReconciler struct{}

func (r *EdgeTriggeredReconciler) OnCreate(obj MyResource) {
	// "A resource was created, so create a secret"
	// Problem: if this crashes halfway, nobody retries
	fmt.Println("creating secret for", obj.Name)
}

func (r *EdgeTriggeredReconciler) OnUpdate(oldObj, newObj MyResource) {
	// "A resource was updated, figure out the diff"
	// Problem: complex diff logic, easy to miss edge cases
	fmt.Println("updating secret for", newObj.Name)
}

func (r *EdgeTriggeredReconciler) OnDelete(obj MyResource) {
	// "A resource was deleted, clean up"
	// Problem: what if the controller wasn't running when delete happened?
	fmt.Println("deleting secret for", obj.Name)
}

// --- CORRECT PATTERN: Level-Triggered (DO this) ---
//
// This approach doesn't care what event happened. It just reads the desired state
// and the actual state, then makes them match.

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
	// If it doesn't exist, it was deleted - clean up and return.
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

	// Step 3: Check if anything needs to change
	// Skip the expensive provider call if everything is already in sync.
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
	//
	// Real code: externalsecret_controller.go:535-573
	if actual == nil {
		// Secret doesn't exist, create it
		return r.client.CreateSecret(ctx, desired.TargetSecretName, namespace, data)
	}
	// Secret exists, update it
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
