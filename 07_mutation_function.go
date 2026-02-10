// Pattern 7: Mutation Function Pattern
//
// Problem: The reconciler needs to create new secrets AND update existing ones.
// The "desired state" logic (set owner refs, apply templates, set labels) is the same
// for both operations. Without a pattern, you'd duplicate this logic.
//
// Solution: Define a mutation function that transforms a Secret to the desired state.
// Pass it to both the create and update paths. The function doesn't know or care
// whether the Secret is new or existing.
//
// REAL CODE REFERENCE:
//   externalsecret_controller.go:442-533  (mutationFunc definition)
//   externalsecret_controller.go:552      (createSecret uses mutationFunc)
//   externalsecret_controller.go:555      (updateSecret uses mutationFunc)

package guide

import (
	"fmt"
)

// =============================================================================
// The Mutation Function
// =============================================================================
//
// A closure that captures the ExternalSecret and provider data,
// then applies the desired state to ANY Secret object (new or existing).

type Secret struct {
	Name            string
	Namespace       string
	Labels          map[string]string
	Annotations     map[string]string
	Data            map[string][]byte
	OwnerReferences []string
}

type ExternalSecret struct {
	Name           string
	Namespace      string
	CreationPolicy string
	Immutable      bool
}

// This is how the real code defines it:
//
// Real code: externalsecret_controller.go:442-533
//
//   mutationFunc := func(secret *v1.Secret) error {
//       // 1. Check/set owner references
//       // 2. Initialize maps
//       // 3. Apply templates
//       // 4. Set labels and annotations
//       return nil
//   }

func buildMutationFunc(es *ExternalSecret, providerData map[string][]byte) func(*Secret) error {
	return func(secret *Secret) error {
		// Initialize maps (safe to set values)
		// Real code: externalsecret_controller.go:480-488
		if secret.Labels == nil {
			secret.Labels = make(map[string]string)
		}
		if secret.Annotations == nil {
			secret.Annotations = make(map[string]string)
		}
		if secret.Data == nil {
			secret.Data = make(map[string][]byte)
		}

		// Set owner reference if policy is "Owner"
		// Real code: externalsecret_controller.go:463-468
		if es.CreationPolicy == "Owner" {
			secret.OwnerReferences = []string{es.Name}
		}

		// Apply provider data (or template transformation)
		// Real code: externalsecret_controller.go:513
		//   err = r.ApplyTemplate(ctx, externalSecret, secret, dataMap)
		for k, v := range providerData {
			secret.Data[k] = v
		}

		// Set tracking labels
		// Real code: externalsecret_controller.go:529-530
		secret.Labels["reconcile.external-secrets.io/managed"] = "true"
		secret.Annotations["reconcile.external-secrets.io/data-hash"] = "abc123"

		return nil
	}
}

// =============================================================================
// Create and Update both use the same mutation function
// =============================================================================

// Real code: externalsecret_controller.go:874-902
func createSecret(mutationFunc func(*Secret) error, name, namespace string) error {
	// Start with a blank secret
	newSecret := &Secret{
		Name:      name,
		Namespace: namespace,
	}

	// Apply the mutation — same function used for update
	if err := mutationFunc(newSecret); err != nil {
		return err
	}

	// Create in Kubernetes
	fmt.Printf("CREATE secret %s/%s with data keys: %v\n", namespace, name, dataKeys(newSecret))
	return nil
}

// Real code: externalsecret_controller.go:904-978
func updateSecret(existingSecret *Secret, mutationFunc func(*Secret) error) error {
	// Apply the mutation to a copy of the existing secret
	// The same function handles owner refs, labels, data — everything
	if err := mutationFunc(existingSecret); err != nil {
		return err
	}

	// Update in Kubernetes
	fmt.Printf("UPDATE secret %s/%s with data keys: %v\n",
		existingSecret.Namespace, existingSecret.Name, dataKeys(existingSecret))
	return nil
}

// =============================================================================
// How the reconciler uses it
// =============================================================================
//
// Real code: externalsecret_controller.go:535-573

func ExampleReconcilerUsage2() {
	es := &ExternalSecret{
		Name:           "my-es",
		Namespace:      "default",
		CreationPolicy: "Owner",
	}

	// Data fetched from provider (AWS, Vault, etc.)
	providerData := map[string][]byte{
		"username": []byte("admin"),
		"password": []byte("s3cret"),
	}

	// Build the mutation function ONCE
	mutationFunc := buildMutationFunc(es, providerData)

	// Branch: does the target secret exist?
	existingSecret := findSecret("my-secret", "default")

	if existingSecret == nil {
		// Secret doesn't exist → create it
		createSecret(mutationFunc, "my-secret", "default")
	} else {
		// Secret exists → update it
		updateSecret(existingSecret, mutationFunc)
	}

	// BOTH paths use the SAME mutationFunc.
	// The logic for "what should the secret look like" is written ONCE.
}

// --- helpers ---

func findSecret(name, namespace string) *Secret {
	// Simulate: secret doesn't exist
	return nil
}

func dataKeys(s *Secret) []string {
	keys := make([]string, 0, len(s.Data))
	for k := range s.Data {
		keys = append(keys, k)
	}
	return keys
}

// KEY INSIGHT:
// The mutation function pattern ensures:
//   - DRY: desired-state logic is written once, used for create AND update
//   - Testable: you can test the mutation function in isolation
//   - Composable: you could chain multiple mutation functions if needed
//   - Separation: "what the secret should look like" is separate from
//     "how to create/update it in Kubernetes"
//
// Without this pattern, you'd have two copies of the same logic:
//   createSecret() { set labels, set owner, set data, set annotations... }
//   updateSecret() { set labels, set owner, set data, set annotations... }  // duplicated!
