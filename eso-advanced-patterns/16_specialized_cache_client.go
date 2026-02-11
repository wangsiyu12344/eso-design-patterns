// Pattern 16: Specialized Cache Client with Label Selector
//
// Problem: In a cluster with 10,000 secrets, the controller's informer cache
// stores ALL of them in memory. But the controller only manages secrets it
// created (maybe 200 out of 10,000). The other 9,800 secrets waste memory,
// increase startup time, and generate unnecessary watch events.
//
// Solution: Create a specialized client with its own cache that uses a label
// selector. This cache ONLY stores secrets with a specific label (e.g.,
// "reconcile.external-secrets.io/managed=true"). Memory usage drops by 98%.
//
// KEY DESIGN DECISION: Set ReaderFailOnMissingInformer=true so that if someone
// accidentally tries to use this client for Deployments (which have no informer
// registered), it fails loudly instead of silently making uncached API calls.
//
// REAL CODE REFERENCE:
//   pkg/controllers/common/common.go:37-96

package eso_advanced_patterns

import (
	"fmt"
)

// =============================================================================
// Anti-Pattern: Using the Default Manager Client
// =============================================================================
//
// The default manager client caches ALL resources of each watched type.
// For secrets, this means every secret in every namespace goes into memory.
//
//   mgr.GetClient().List(ctx, &secretList, ...)
//
// This client works, but:
//   - Caches ALL secrets → O(total secrets) memory
//   - Watches ALL secret events → unnecessary CPU for irrelevant changes
//   - Startup time grows with cluster size → slow leader election transitions

// =============================================================================
// Correct Pattern: Build a Restricted Cache Client
// =============================================================================
//
// Real code: pkg/controllers/common/common.go:37-96
//
// Architecture:
//
//   ┌──────────────────────┐      ┌──────────────────────┐
//   │  Default Manager     │      │  Secret Cache         │
//   │  Client              │      │  Client               │
//   │                      │      │                       │
//   │  Caches:             │      │  Caches ONLY:         │
//   │  - ExternalSecrets   │      │  - Secrets with label │
//   │  - SecretStores      │      │    managed=true       │
//   │  - ClusterSecretStore│      │                       │
//   │  (all instances)     │      │  (200 out of 10,000)  │
//   └──────────────────────┘      └──────────────────────┘
//         ↓                              ↓
//   Used for: reading CRDs       Used for: reading managed secrets
//
// The secret cache client is a completely separate client with its own
// informer, its own watch connection, and its own in-memory store.

func demonstrateBuildManagedSecretClient() {
	// Real code walkthrough:

	// Step 1: Define what to cache — only secrets with our label
	//
	//   managedLabelReq, _ := labels.NewRequirement(
	//       esv1.LabelManaged,         // "reconcile.external-secrets.io/managed"
	//       selection.Equals,
	//       []string{esv1.LabelManagedValue},  // "true"
	//   )
	//   managedLabelSelector := labels.NewSelector().Add(*managedLabelReq)

	// Step 2: Create cache with label selector
	//
	//   secretCacheOpts := cache.Options{
	//       ByObject: map[client.Object]cache.ByObject{
	//           &corev1.Secret{}: {
	//               Label: managedLabelSelector,  // ← only these secrets
	//           },
	//       },
	//       ReaderFailOnMissingInformer: true,  // ← fail-fast safety net
	//   }
	//
	// ReaderFailOnMissingInformer is critical:
	// Without it, if someone writes `secretClient.Get(ctx, key, &deployment)`,
	// the client silently falls back to a direct API call (uncached, slow).
	// With it, you get an immediate error: "no informer for deployments" — which
	// makes the bug obvious instead of hidden.

	// Step 3: Explicitly start the informer
	//
	//   _, err = secretCache.GetInformer(context.Background(), &corev1.Secret{})
	//
	// Because ReaderFailOnMissingInformer is true, we MUST explicitly register
	// which types this cache handles. This is documentation-as-code: the
	// GetInformer call declares "this cache is for Secrets, nothing else."

	// Step 4: Register with manager for lifecycle management
	//
	//   err = mgr.Add(secretCache)
	//
	// The manager starts the cache when the controller starts, and stops it
	// when the controller stops. No manual goroutine management needed.

	// Step 5: Build the client using the restricted cache
	//
	//   secretClient, err := client.New(mgr.GetConfig(), client.Options{
	//       Cache: &client.CacheOptions{
	//           Reader: secretCache,  // ← reads from our restricted cache
	//       },
	//   })
	//
	// Writes go directly to the API server (no caching needed for writes).
	// Reads go through our label-filtered cache.

	fmt.Println("Managed secret client: caches only labeled secrets")
}

// =============================================================================
// Optional: Namespace Restriction
// =============================================================================
//
// For single-namespace installations, the cache can also be restricted to
// one namespace, further reducing scope:
//
//   if namespace != "" {
//       secretCacheOpts.DefaultNamespaces = map[string]cache.Config{
//           namespace: {},
//       }
//   }
//
// This means the watch connection uses ?fieldSelector=metadata.namespace=X,
// so the API server only sends events for that namespace.

// =============================================================================
// Memory Impact
// =============================================================================
//
// Cluster with 10,000 secrets, 200 managed by ESO:
//
//   Default client:    10,000 secrets cached → ~50MB RAM
//   Specialized client:   200 secrets cached → ~1MB RAM
//
// The savings compound with cluster size. In large multi-tenant clusters
// with 100,000+ secrets, this pattern prevents OOM kills.

func init() {
	_ = demonstrateBuildManagedSecretClient
}
