// Pattern 10: Status Subresource for Observability
//
// Problem: Users and monitoring systems need to know:
//   - Is the ExternalSecret syncing correctly?
//   - When was it last refreshed?
//   - What went wrong if it failed?
//
// Solution: Use the Kubernetes status subresource. The ExternalSecret's .status
// field is updated by the controller to reflect the current state. Users can
// check it with "kubectl get externalsecret -o yaml" or set up alerts.
//
// KEY DESIGN: The status update uses a deferred function, so no matter how
// Reconcile() exits (success, error, early return, panic recovery), the status
// is always updated consistently.
//
// REAL CODE REFERENCE:
//   externalsecret_controller.go:359-396  (deferred status update)
//   externalsecret_controller.go:777-802  (markAsDone, markAsFailed)

package guide

import (
	"context"
	"fmt"
	"time"
)

// =============================================================================
// The Status Struct
// =============================================================================
//
// The status subresource is a special part of a Kubernetes object:
//   - The controller updates .status (not the user)
//   - The user updates .spec (not the controller)
//   - They use different API endpoints, avoiding conflicts

type ESStatus struct {
	// Conditions follow the standard Kubernetes condition pattern
	Conditions []Condition

	// When was the secret last fetched from the provider?
	RefreshTime time.Time

	// Which generation of the spec was last synced?
	SyncedResourceVersion string

	// Which K8s Secret is this ExternalSecret managing?
	Binding string // e.g., "my-secret"
}

type Condition struct {
	Type               string    // "Ready"
	Status             string    // "True", "False"
	Reason             string    // "SecretSynced", "SecretSyncedError"
	Message            string    // human-readable description
	LastTransitionTime time.Time // when the condition last changed
}

// =============================================================================
// Deferred Status Update
// =============================================================================
//
// The critical pattern: use defer to update status regardless of how Reconcile() exits.
//
// Real code: externalsecret_controller.go:359-396
//
//   currentStatus := *externalSecret.Status.DeepCopy()
//   defer func() {
//       if equality.Semantic.DeepEqual(currentStatus, externalSecret.Status) {
//           return  // no change, skip the API call
//       }
//       updateErr := r.Status().Update(ctx, externalSecret)
//       if apierrors.IsConflict(updateErr) {
//           result = ctrl.Result{Requeue: true}  // retry on conflict
//           return
//       }
//       if updateErr != nil {
//           err = updateErr  // propagate the error
//       }
//   }()

type StatusReconciler struct{}

func (r *StatusReconciler) Reconcile(ctx context.Context, name, namespace string) (err error) {
	status := &ESStatus{}

	// Snapshot the current status BEFORE any changes
	originalStatus := *status

	// Deferred function: updates status when Reconcile() returns
	defer func() {
		// Only update if something changed (avoids unnecessary API calls)
		if statusEqual(originalStatus, *status) {
			return
		}

		fmt.Printf("Updating status for %s/%s: Ready=%s, Reason=%s\n",
			namespace, name,
			status.Conditions[0].Status,
			status.Conditions[0].Reason,
		)

		// In reality: r.Status().Update(ctx, externalSecret)
		// This uses the /status subresource endpoint
	}()

	// --- Normal reconciliation ---

	// Scenario 1: Provider call fails
	providerData, providerErr := callProvider(ctx)
	if providerErr != nil {
		// Mark as failed — the deferred function will persist this
		markFailed(status, "could not get secret data from provider", providerErr)
		return providerErr // deferred func runs, status is updated
	}

	// Scenario 2: Secret update fails
	updateErr := updateTargetSecret(ctx, providerData)
	if updateErr != nil {
		markFailed(status, "could not update secret", updateErr)
		return updateErr // deferred func runs, status is updated
	}

	// Scenario 3: Success
	markDone(status, name)
	return nil // deferred func runs, status is updated
}

// =============================================================================
// markAsDone and markAsFailed
// =============================================================================
//
// Real code: externalsecret_controller.go:777-802

func markDone(status *ESStatus, secretName string) {
	// Real code: externalsecret_controller.go:777-795
	status.Conditions = []Condition{{
		Type:               "Ready",
		Status:             "True",
		Reason:             "SecretSynced",
		Message:            "secret synced",
		LastTransitionTime: time.Now(),
	}}
	status.RefreshTime = time.Now()
	status.Binding = secretName
}

func markFailed(status *ESStatus, msg string, err error) {
	// Real code: externalsecret_controller.go:797-802
	status.Conditions = []Condition{{
		Type:               "Ready",
		Status:             "False",
		Reason:             "SecretSyncedError",
		Message:            fmt.Sprintf("%s: %v", msg, err),
		LastTransitionTime: time.Now(),
	}}
}

// =============================================================================
// What Users See
// =============================================================================

func ExampleUserView() {
	// kubectl get externalsecret my-es -o yaml
	//
	// SUCCESS:
	//   status:
	//     conditions:
	//     - type: Ready
	//       status: "True"
	//       reason: SecretSynced
	//       message: "secret synced"
	//       lastTransitionTime: "2024-01-15T10:30:00Z"
	//     refreshTime: "2024-01-15T10:30:00Z"
	//     syncedResourceVersion: "3"
	//     binding:
	//       name: my-secret
	fmt.Println("Success: Ready=True, Reason=SecretSynced")

	// FAILURE:
	//   status:
	//     conditions:
	//     - type: Ready
	//       status: "False"
	//       reason: SecretSyncedError
	//       message: "could not get secret data from provider: AccessDeniedException: ..."
	//       lastTransitionTime: "2024-01-15T10:31:00Z"
	//     refreshTime: "2024-01-15T10:30:00Z"    ← still shows last successful refresh
	fmt.Println("Failure: Ready=False, Reason=SecretSyncedError")

	// kubectl get externalsecret
	//
	//   NAME    STORE      REFRESH   STATUS          READY
	//   my-es   my-store   1h        SecretSynced    True
	//   bad-es  my-store   1h        SecretSyncedError  False
}

// =============================================================================
// Why Deferred Status Update Matters
// =============================================================================

func ExampleWhyDefer() {
	// Without defer, you'd need to update status at EVERY return point:
	//
	//   if err := step1(); err != nil {
	//       updateStatus(failed)    // easy to forget!
	//       return err
	//   }
	//   if err := step2(); err != nil {
	//       updateStatus(failed)    // easy to forget!
	//       return err
	//   }
	//   if err := step3(); err != nil {
	//       updateStatus(failed)    // easy to forget!
	//       return err
	//   }
	//   updateStatus(success)
	//
	// With defer, status is ALWAYS updated:
	//
	//   defer updateStatus()
	//   if err := step1(); err != nil { markFailed(); return err }
	//   if err := step2(); err != nil { markFailed(); return err }
	//   if err := step3(); err != nil { markFailed(); return err }
	//   markDone()
	//
	// The defer also handles an important edge case:
	// It uses NAMED RETURN VALUES to modify `result` and `err` from within the defer.
	//
	// Real code: externalsecret_controller.go:159
	//   func (r *Reconciler) Reconcile(...) (result ctrl.Result, err error) {
	//
	// This lets the deferred function change the return value, for example
	// to set Requeue=true if the status update got a conflict.

	fmt.Println("Defer guarantees status is always updated, even on early returns or errors")
}

// --- helpers ---

func callProvider(ctx context.Context) (map[string][]byte, error) {
	return map[string][]byte{"key": []byte("value")}, nil
}
func updateTargetSecret(ctx context.Context, data map[string][]byte) error { return nil }
func statusEqual(a, b ESStatus) bool                                       { return false }

// KEY INSIGHT:
// The status subresource is how Kubernetes controllers communicate state
// back to users and monitoring systems. The deferred update pattern
// guarantees consistency — the status always reflects what actually happened,
// regardless of which code path was taken or which error occurred.
//
// Combined with Prometheus metrics (also updated in the same code),
// this gives operators full visibility into the health of every ExternalSecret.
