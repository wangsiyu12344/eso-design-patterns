// Pattern 9: Layered Cache Strategy
//
// Problem: In a large cluster with 10,000 secrets, caching all of them in the
// controller's memory is expensive. But making direct API calls for every
// Reconcile() is also expensive (high API server load).
//
// Solution: Multiple cache layers, each with different trade-offs,
// controlled by CLI flags so operators can tune for their environment.
//
// THE THREE LAYERS:
//   Layer 1: Partial cache (metadata only) — always on, low memory
//   Layer 2: Managed secrets cache — only secrets with "managed" label
//   Layer 3: Full cache — all secrets (optional, high memory)
//
// REAL CODE REFERENCE:
//   cmd/controller/root.go:139-147        (cache disable configuration)
//   cmd/controller/root.go:192-199        (managed secrets cache client)
//   externalsecret_controller.go:295-344  (using both partial and full cache)
//   externalsecret_controller.go:1230     (WatchesMetadata for partial cache)

package guide

import "fmt"

// =============================================================================
// Layer 1: Partial Object Metadata Cache (Always On)
// =============================================================================
//
// WatchesMetadata() in SetupWithManager() creates a Watch that only receives
// metadata (name, namespace, labels, annotations, resourceVersion) — NOT the
// full secret data. This dramatically reduces memory usage.
//
// Real code: externalsecret_controller.go:1230-1234
//
//   WatchesMetadata(
//       &v1.Secret{},
//       handler.EnqueueRequestsFromMapFunc(r.findObjectsForSecret),
//       builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}, secretHasESLabel),
//   )
//
// This means:
//   - The controller watches ALL secrets that have the "managed" label
//   - But only the metadata is cached (not the actual secret data bytes)
//   - Used to detect changes and trigger reconciliation

type PartialObjectMetadata struct {
	Name            string
	Namespace       string
	Labels          map[string]string
	Annotations     map[string]string
	ResourceVersion string
	UID             string
	// NOTE: no Data field! Only metadata.
}

// =============================================================================
// Layer 2: Managed Secrets Cache (Default: enabled)
// =============================================================================
//
// Flag: --enable-managed-secrets-caching (default: true)
//
// Only caches the FULL secret objects that have the label:
//   reconcile.external-secrets.io/managed=true
//
// This is a middle ground: you get cached reads for the secrets you manage,
// without caching every secret in the cluster.
//
// Real code: cmd/controller/root.go:192-199
//
//   secretClient := mgr.GetClient()
//   if enableManagedSecretsCache && !enableSecretsCache {
//       secretClient, err = ctrlcommon.BuildManagedSecretClient(mgr, namespace)
//   }
//
// The reconciler uses this special client for secret reads:
//   externalsecret_controller.go:328
//   err = r.SecretClient.Get(ctx, ..., existingSecret)

func ExampleManagedSecretsCache() {
	// Cluster has 10,000 secrets
	// Only 500 are managed by ExternalSecrets (have the "managed" label)
	//
	// With managed secrets cache:
	//   - 500 secrets cached in memory (full objects including data)
	//   - 9,500 secrets NOT cached
	//   - Memory: ~500 * avg_secret_size
	//
	// Without any cache (--enable-managed-secrets-caching=false):
	//   - Every Reconcile() makes a direct API call to read the secret
	//   - Memory: ~0
	//   - API server load: HIGH

	fmt.Println("Managed cache: 500 secrets cached out of 10,000")
}

// =============================================================================
// Layer 3: Full Secrets Cache (Default: disabled)
// =============================================================================
//
// Flag: --enable-secrets-caching (default: false)
//
// Caches ALL secrets in the cluster. Only needed if your reconciler frequently
// reads secrets that are NOT managed by ExternalSecrets.
//
// Real code: cmd/controller/root.go:140-143
//
//   if !enableSecretsCache {
//       clientCacheDisableFor = append(clientCacheDisableFor, &v1.Secret{})
//   }

func ExampleFullSecretsCache() {
	// Cluster has 10,000 secrets
	//
	// With full cache:
	//   - ALL 10,000 secrets cached in memory
	//   - Fast reads, zero API calls
	//   - Memory: ~10,000 * avg_secret_size (WARNING: can be GBs)
	//
	// This is the same as the default controller-runtime behavior.
	// External-secrets disables it by default to save memory.

	fmt.Println("Full cache: all 10,000 secrets cached (high memory!)")
}

// =============================================================================
// How the Reconciler Uses Both Caches
// =============================================================================
//
// Real code: externalsecret_controller.go:295-344
//
// The reconciler reads the secret from BOTH caches and cross-checks:

func ExampleDualCacheRead() {
	// Step 1: Read from partial cache (metadata only)
	// Real code: externalsecret_controller.go:295-302
	//
	//   secretPartial := &metav1.PartialObjectMetadata{}
	//   err = r.Get(ctx, ..., secretPartial)   // uses normal client (partial cache)
	partial := PartialObjectMetadata{
		UID:             "abc-123",
		ResourceVersion: "12345",
		Labels:          map[string]string{"reconcile.external-secrets.io/managed": "true"},
	}

	// Step 2: Read from full/managed cache
	// Real code: externalsecret_controller.go:327-333
	//
	//   existingSecret := &v1.Secret{}
	//   err = r.SecretClient.Get(ctx, ..., existingSecret)  // uses managed secret client
	fullSecretUID := "abc-123"
	fullSecretRV := "12345"

	// Step 3: Cross-check — ensure caches are in sync
	// Real code: externalsecret_controller.go:339-344
	//
	//   if secretPartial.UID != existingSecret.UID || secretPartial.ResourceVersion != existingSecret.ResourceVersion {
	//       err = fmt.Errorf(errSecretCachesNotSynced, secretName)
	//       return ctrl.Result{}, err  // retry with backoff
	//   }
	if partial.UID != fullSecretUID || partial.ResourceVersion != fullSecretRV {
		fmt.Println("Caches not in sync! Retry with backoff.")
	} else {
		fmt.Println("Caches in sync, proceed with reconciliation.")
	}
}

// =============================================================================
// Configuration Summary
// =============================================================================

func ExampleConfigurations() {
	// Small cluster (< 1000 secrets):
	// ./external-secrets --enable-secrets-caching=true
	// → Cache everything, fastest performance, acceptable memory
	fmt.Println("Small cluster: cache everything")

	// Medium cluster (1000-10000 secrets):
	// ./external-secrets  (defaults)
	// → Partial metadata cache + managed secrets cache
	// → Good balance of memory and performance
	fmt.Println("Medium cluster: managed cache only (default)")

	// Large cluster (10000+ secrets):
	// ./external-secrets --enable-managed-secrets-caching=false
	// → Only partial metadata cache
	// → Minimal memory, but more API calls
	fmt.Println("Large cluster: metadata only, direct API reads")
}

// KEY INSIGHT:
// There's no one-size-fits-all caching strategy. This project gives operators
// three knobs to tune the memory vs. API-server-load trade-off:
//
//   --enable-secrets-caching         (Layer 3: cache all secrets)
//   --enable-managed-secrets-caching (Layer 2: cache managed secrets only)
//   --enable-configmaps-caching      (same pattern for ConfigMaps)
//
// The dual-cache cross-check (partial vs full) prevents race conditions
// where one cache is ahead of the other, which could cause the reconciler
// to act on stale data.
