// Pattern 6: Ownership & Garbage Collection
//
// Problem: When an ExternalSecret is deleted, who cleans up the managed K8s Secret?
// You could write custom cleanup logic, but Kubernetes has a built-in mechanism.
//
// Solution: Owner References. When object A sets an ownerReference on object B,
// Kubernetes automatically deletes B when A is deleted. This is called
// "garbage collection" and it's built into the API server.
//
// EXTERNAL-SECRETS USES TWO LAYERS OF OWNERSHIP:
//   1. OwnerReference — Kubernetes built-in GC (for CreationPolicy=Owner)
//   2. Labels — Custom ownership tracking (for orphan detection)
//
// REAL CODE REFERENCE:
//   externalsecret_controller.go:446-477  (owner reference logic)
//   externalsecret_controller.go:519-530  (label-based ownership)
//   externalsecret_controller.go:842-871  (orphan detection)

package guide

import "fmt"

// =============================================================================
// Layer 1: Kubernetes Owner References
// =============================================================================
//
// When CreationPolicy=Owner, the ExternalSecret sets itself as the "controller owner"
// of the target Secret. This means:
//   - Kubernetes knows the Secret "belongs to" the ExternalSecret
//   - When the ExternalSecret is deleted, Kubernetes automatically deletes the Secret
//   - Only ONE controller owner is allowed per object (prevents conflicts)
//
// Real code: externalsecret_controller.go:463-468
//
//   if externalSecret.Spec.Target.CreationPolicy == esv1.CreatePolicyOwner {
//       err = controllerutil.SetControllerReference(externalSecret, secret, r.Scheme)
//   }

// The Secret's metadata looks like this after SetControllerReference:
//
//   apiVersion: v1
//   kind: Secret
//   metadata:
//     name: my-secret
//     ownerReferences:
//     - apiVersion: external-secrets.io/v1
//       kind: ExternalSecret
//       name: my-es
//       uid: abc-123
//       controller: true        ← marks this as THE controller owner
//       blockOwnerDeletion: true

type OwnerReference struct {
	APIVersion         string
	Kind               string
	Name               string
	UID                string // The UID ensures we reference the exact object, not just a name match
	Controller         bool   // true = this is the controller owner (only one allowed per object)
	BlockOwnerDeletion bool   // true = block owner deletion until this child is cleaned up
}

// Why Controller=true is important:
// Kubernetes allows multiple ownerReferences (an object can have multiple "parents"),
// but only ONE can be the "controller" owner. This prevents conflicts: if two
// ExternalSecrets both targeted the same Secret with Controller=true, the second
// SetControllerReference call would fail, surfacing the conflict immediately
// rather than allowing a silent update war.

// =============================================================================
// Layer 2: Label-Based Ownership (for orphan detection)
// =============================================================================
//
// Problem: If the user changes spec.target.name from "secret-a" to "secret-b",
// the controller creates "secret-b" but "secret-a" is now orphaned.
// OwnerReferences don't help here because the OLD secret still has a valid
// ownerReference pointing to the ExternalSecret — Kubernetes won't GC it
// since the owner (ExternalSecret) still exists. The ownerRef only triggers
// deletion when the OWNER is deleted, not when the owner stops "wanting" the child.
//
// Solution: A label that hashes the ExternalSecret's name, allowing the controller
// to find all secrets owned by a specific ExternalSecret and delete orphans.
//
// Real code: externalsecret_controller.go:519-527
//
//   if externalSecret.Spec.Target.CreationPolicy == esv1.CreatePolicyOwner {
//       lblValue := esutils.ObjectHash(fmt.Sprintf("%v/%v", externalSecret.Namespace, externalSecret.Name))
//       secret.Labels[esv1.LabelOwner] = lblValue
//   }

func ExampleOrphanDetection() {
	// Step 1: ExternalSecret "my-es" with target.name = "secret-a"
	//   → Creates Secret "secret-a" with label: reconcile.external-secrets.io/owner=hash("default/my-es")

	// Step 2: User updates ExternalSecret "my-es" with target.name = "secret-b"
	//   → Creates Secret "secret-b" with label: reconcile.external-secrets.io/owner=hash("default/my-es")
	//   → Secret "secret-a" still exists with the same owner label!

	// Step 3: Reconcile detects the orphan
	//   Real code: externalsecret_controller.go:842-871 (deleteOrphanedSecrets)
	//
	//   ownerLabel := esutils.ObjectHash("default/my-es")
	//   secretList := client.List(labels: {"reconcile.external-secrets.io/owner": ownerLabel})
	//   for _, secret := range secretList {
	//       if secret.Name != "secret-b" {   // not the current target
	//           client.Delete(secret)          // delete orphan "secret-a"
	//       }
	//   }

	fmt.Println("Orphan 'secret-a' detected and deleted")
	fmt.Println("Current target 'secret-b' kept")
}

// =============================================================================
// Another ExternalSecret Owns It — Conflict Prevention
// =============================================================================
//
// What if two ExternalSecrets target the same Secret name?
// Without protection, they'd fight each other (update loops).
//
// Real code: externalsecret_controller.go:446-459
//
//   currentOwner := metav1.GetControllerOf(secret)
//   ownerIsESKind := currentOwnerGK.String() == esv1.ExtSecretGroupKind
//   ownerIsCurrentES := ownerIsESKind && currentOwner.Name == externalSecret.Name
//
//   // If another ExternalSecret owns it, refuse to update
//   if ownerIsESKind && !ownerIsCurrentES {
//       return fmt.Errorf("%w: %s", ErrSecretIsOwned, currentOwner.Name)
//   }

func ExampleConflictPrevention() {
	// ExternalSecret "es-1" creates Secret "shared-secret" with ownerRef → es-1
	// ExternalSecret "es-2" tries to update Secret "shared-secret"
	//   → Sees ownerRef points to "es-1", not "es-2"
	//   → Returns ErrSecretIsOwned
	//   → Does NOT retry (permanent error)
	//   → Status shows: "target is owned by another ExternalSecret: es-1"

	fmt.Println("es-2 refused: secret is owned by es-1")
}

// =============================================================================
// Creation Policies
// =============================================================================
//
// Real code: externalsecret_controller.go:535-573
//
// The project supports different ownership models:

func ExampleCreationPolicies() {
	// Owner (default):
	//   - Sets ownerReference on the Secret
	//   - Kubernetes auto-deletes Secret when ExternalSecret is deleted
	//   - Orphan detection cleans up old secrets when target name changes
	fmt.Println("Owner: full lifecycle management")

	// Merge:
	//   - Does NOT create the Secret (must already exist)
	//   - Merges data into existing Secret
	//   - No ownerReference (someone else owns it)
	fmt.Println("Merge: only update existing secrets")

	// Orphan:
	//   - Creates the Secret if needed
	//   - No ownerReference (Secret survives ExternalSecret deletion)
	//   - Secret is "abandoned" when ExternalSecret is deleted
	fmt.Println("Orphan: create but don't clean up")

	// None:
	//   - Does nothing to the Secret
	//   - Only updates status
	fmt.Println("None: read-only mode")
}

// KEY INSIGHT:
// Leverage Kubernetes' built-in garbage collection instead of building your own.
// OwnerReferences handle the common case (delete parent → delete child).
// Labels handle edge cases (target name changed → find and delete orphans).
// The controller owner check prevents two controllers from fighting over the same resource.
