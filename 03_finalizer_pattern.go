// Pattern 3: Finalizer Pattern
//
// Problem: When a Kubernetes object is deleted, it's immediately removed from etcd.
// But what if your controller needs to clean up external resources first?
// (e.g., delete the managed K8s Secret when an ExternalSecret is deleted)
//
// Solution: Finalizers are strings added to an object's metadata. Kubernetes will NOT
// delete the object from etcd until ALL finalizers are removed. This gives your controller
// a chance to run cleanup logic.
//
// THE LIFECYCLE:
//   1. Object created → controller adds finalizer
//   2. User runs "kubectl delete" → Kubernetes sets DeletionTimestamp but does NOT delete
//   3. Controller sees DeletionTimestamp → runs cleanup → removes finalizer
//   4. No finalizers left → Kubernetes actually deletes from etcd
//   5. Controller gets one more Reconcile → Get() returns NotFound → done
//
// REAL CODE REFERENCE:
//   pkg/controllers/externalsecret/externalsecret_controller.go:197-234

package guide

import (
	"context"
	"fmt"
)

// Finalizer names must be globally unique to avoid collisions with other controllers.
// By convention, they use a DNS-like prefix followed by a descriptive suffix.
// This ensures that if multiple controllers manage the same object, each one's
// finalizer is distinct and independently managed.
const MyFinalizer = "mycontroller.example.com/cleanup"

// Real finalizer name:
//   externalsecret_controller.go:71
//   ExternalSecretFinalizer = "externalsecrets.external-secrets.io/externalsecret-cleanup"

type Resource struct {
	Name              string
	Namespace         string
	DeletionTimestamp *string            // nil = not being deleted, non-nil = marked for deletion
	Finalizers        []string
	Spec              ResourceSpec
}

type ResourceSpec struct {
	TargetSecretName string
	DeletionPolicy   string // "Delete" = remove managed secret on cleanup; "Retain" = leave it
}

type FinalizerReconciler struct {
	client FinalizerFakeClient
}

func (r *FinalizerReconciler) Reconcile(ctx context.Context, name, namespace string) error {
	resource, err := r.client.Get(ctx, name, namespace)
	if err != nil {
		if isNotFoundErr(err) {
			// Step 5: Object is fully gone. Nothing to do.
			return nil
		}
		return err
	}

	// =========================================================================
	// Step 3: Handle deletion — object exists but has DeletionTimestamp
	// =========================================================================
	// Real code: externalsecret_controller.go:197-225
	//
	//   if !externalSecret.GetDeletionTimestamp().IsZero() {
	//       if err := r.cleanupManagedSecrets(ctx, log, externalSecret); err != nil {
	//           return ctrl.Result{}, err
	//       }
	//       patch := client.MergeFrom(externalSecret.DeepCopy())
	//       if updated := controllerutil.RemoveFinalizer(externalSecret, ExternalSecretFinalizer); updated {
	//           if err := r.Patch(ctx, externalSecret, patch); err != nil {
	//               return ctrl.Result{}, err
	//           }
	//       }
	//       return ctrl.Result{}, nil
	//   }
	if resource.DeletionTimestamp != nil {
		// Object is being deleted — Kubernetes has set DeletionTimestamp but is
		// waiting for all finalizers to be removed before actually deleting from etcd.
		// This is our window to perform cleanup. If cleanup fails, we return an error
		// and the finalizer stays — the object remains in a "terminating" state until
		// we successfully clean up and remove the finalizer.
		if hasFinalizer(resource, MyFinalizer) {
			// Clean up managed resources based on deletion policy.
			// The DeletionPolicy gives users control: "Delete" cleans up the managed
			// K8s Secret (default), while "Retain" leaves it in place for manual handling.
			if resource.Spec.DeletionPolicy == "Delete" {
				fmt.Println("deleting managed secret:", resource.Spec.TargetSecretName)
				if err := r.client.DeleteSecret(ctx, resource.Spec.TargetSecretName, namespace); err != nil {
					return err // Will retry — finalizer stays, object can't be deleted
				}
			} else {
				fmt.Println("retaining managed secret:", resource.Spec.TargetSecretName)
			}

			// Remove our finalizer — this is the critical step that unblocks deletion.
			// Once we remove it, Kubernetes checks if any other finalizers remain.
			// If not, the object is permanently deleted from etcd.
			removeFinalizer(resource, MyFinalizer)
			if err := r.client.Update(ctx, resource); err != nil {
				return err
			}
		}
		return nil // Done, object will be deleted by Kubernetes
	}

	// =========================================================================
	// Step 1: Normal reconcile — ensure finalizer exists
	// =========================================================================
	// We add the finalizer early (ideally on first reconcile after creation)
	// so that if the user deletes the object later, we're guaranteed a chance
	// to run cleanup. Without the finalizer, a "kubectl delete" would immediately
	// remove the object from etcd, and our cleanup logic would never execute.
	// Real code: externalsecret_controller.go:229-234
	//
	//   patch := client.MergeFrom(externalSecret.DeepCopy())
	//   if updated := controllerutil.AddFinalizer(externalSecret, ExternalSecretFinalizer); updated {
	//       if err := r.Patch(ctx, externalSecret, patch); err != nil {
	//           return ctrl.Result{}, err
	//       }
	//   }
	if !hasFinalizer(resource, MyFinalizer) {
		addFinalizer(resource, MyFinalizer)
		if err := r.client.Update(ctx, resource); err != nil {
			return err
		}
	}

	// ... rest of normal reconciliation logic ...
	fmt.Println("normal reconcile for:", resource.Name)
	return nil
}

// THE TIMELINE:
//
// Time 0: ExternalSecret "my-es" created
//   → Reconcile() called
//   → Adds finalizer: ["externalsecrets.external-secrets.io/externalsecret-cleanup"]
//   → Fetches from provider, creates K8s Secret
//
// Time 1: User runs "kubectl delete externalsecret my-es"
//   → API server sets DeletionTimestamp = "2024-01-01T00:00:00Z"
//   → API server sees finalizer exists → does NOT delete from etcd
//   → Reconcile() called
//   → Sees DeletionTimestamp is set
//   → Deletes the managed K8s Secret (cleanup)
//   → Removes finalizer: []
//   → Updates the object
//
// Time 2: API server sees no finalizers left
//   → Actually deletes object from etcd
//   → Reconcile() called one more time
//   → Get() returns NotFound
//   → Returns nil (done)
//
// WITHOUT FINALIZERS:
//   kubectl delete → object immediately gone from etcd
//   → Reconcile() gets NotFound → returns nil
//   → Managed K8s Secret is ORPHANED (never cleaned up!)

// --- Helper functions ---

func hasFinalizer(r *Resource, finalizer string) bool {
	for _, f := range r.Finalizers {
		if f == finalizer {
			return true
		}
	}
	return false
}

func addFinalizer(r *Resource, finalizer string) {
	if !hasFinalizer(r, finalizer) {
		r.Finalizers = append(r.Finalizers, finalizer)
	}
}

func removeFinalizer(r *Resource, finalizer string) {
	var result []string
	for _, f := range r.Finalizers {
		if f != finalizer {
			result = append(result, f)
		}
	}
	r.Finalizers = result
}

func isNotFoundErr(err error) bool { return false }

type FinalizerFakeClient struct{}

func (c FinalizerFakeClient) Get(ctx context.Context, name, namespace string) (*Resource, error) {
	return nil, nil
}
func (c FinalizerFakeClient) Update(ctx context.Context, r *Resource) error    { return nil }
func (c FinalizerFakeClient) DeleteSecret(ctx context.Context, name, ns string) error {
	return nil
}
