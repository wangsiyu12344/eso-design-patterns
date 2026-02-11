// Pattern 19: Resource Version Hashing for Cache Invalidation
//
// Problem: Kubernetes objects have a .metadata.generation field that increments
// when .spec changes. Controllers often use generation to decide "should I
// re-reconcile?" But generation does NOT change when labels or annotations
// change. So if someone adds an annotation that affects behavior (e.g.,
// "force-refresh=true"), the controller doesn't notice.
//
// Solution: Compute a composite version string that combines generation (for
// spec changes) with a hash of labels+annotations (for metadata changes).
// If either changes, the version string changes, triggering reconciliation.
//
// REAL CODE REFERENCE:
//   pkg/controllers/util/util.go:28-44

package eso_advanced_patterns

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// =============================================================================
// Anti-Pattern: Using Only Generation
// =============================================================================
//
// Generation-only comparison misses metadata changes. In ESO, annotations
// like "force-sync" or labels for selector-based filtering can change
// without bumping generation. The controller ignores these changes.

func shouldReconcileBad(oldGen, newGen int64) bool {
	return oldGen != newGen // Misses label/annotation changes!
}

// =============================================================================
// Anti-Pattern: Using ResourceVersion
// =============================================================================
//
// ResourceVersion changes on EVERY update, including status updates by the
// controller itself. This creates infinite reconciliation loops:
//   Reconcile → update status → ResourceVersion changes → Reconcile → ...

func shouldReconcileAlsoBad(oldRV, newRV string) bool {
	return oldRV != newRV // Changes on EVERY update, including status!
}

// =============================================================================
// Correct Pattern: Generation + Metadata Hash
// =============================================================================
//
// Real code: pkg/controllers/util/util.go:28-44
//
// This combines:
//   - Generation: increments on spec changes (set by API server)
//   - Hash of labels+annotations: captures metadata changes
//
// The result is a string like "5-a3f2b1c4" that changes when either
// the spec or the metadata changes, but NOT when status changes.

// ObjectMeta is a simplified version of metav1.ObjectMeta.
type ObjectMeta struct {
	Name        string
	Namespace   string
	Generation  int64
	Annotations map[string]string
	Labels      map[string]string
}

// GetResourceVersion returns a composite version string.
// Real code: pkg/controllers/util/util.go:28-31
func GetResourceVersion(meta ObjectMeta) string {
	return fmt.Sprintf("%d-%s", meta.Generation, HashMeta(meta))
}

// HashMeta hashes only the labels and annotations.
// Real code: pkg/controllers/util/util.go:33-44
func HashMeta(m ObjectMeta) string {
	// Only hash the fields that matter for reconciliation decisions.
	// Importantly, this does NOT include:
	//   - ResourceVersion (changes on every write, including status)
	//   - ManagedFields (internal bookkeeping)
	//   - CreationTimestamp, UID, etc. (immutable after creation)
	type meta struct {
		Annotations map[string]string `json:"annotations"`
		Labels      map[string]string `json:"labels"`
	}
	return objectHash(meta{
		Annotations: m.Annotations,
		Labels:      m.Labels,
	})
}

// =============================================================================
// Usage in Reconciliation
// =============================================================================
//
// The controller stores the last-seen resource version in status.
// On the next reconcile, it compares the current version with the stored one.

func shouldReconcile(meta ObjectMeta, lastSeenVersion string) bool {
	currentVersion := GetResourceVersion(meta)
	return currentVersion != lastSeenVersion
}

func demonstrateVersioning() {
	meta := ObjectMeta{
		Name:       "my-secret",
		Generation: 5,
		Annotations: map[string]string{
			"app": "backend",
		},
		Labels: map[string]string{
			"env": "prod",
		},
	}

	v1 := GetResourceVersion(meta)
	fmt.Printf("Version 1: %s\n", v1) // e.g., "5-a3f2b1c4"

	// Spec changes → generation bumps → version changes
	meta.Generation = 6
	v2 := GetResourceVersion(meta)
	fmt.Printf("Version 2: %s (spec changed)\n", v2) // e.g., "6-a3f2b1c4"

	// Annotation changes → hash changes → version changes
	meta.Annotations["force-refresh"] = "true"
	v3 := GetResourceVersion(meta)
	fmt.Printf("Version 3: %s (annotation changed)\n", v3) // e.g., "6-b7e9d2f1"

	// No changes → same version → skip reconciliation
	v4 := GetResourceVersion(meta)
	fmt.Printf("Version 4: %s (no change)\n", v4) // same as v3
}

// =============================================================================
// Why Not Hash Everything?
// =============================================================================
//
// You could hash the entire object, but that has problems:
//   1. Status changes would trigger re-reconciliation (infinite loop)
//   2. ManagedFields changes (from SSA) would trigger unnecessary reconciliation
//   3. ResourceVersion changes on every write — useless for comparison
//
// By hashing ONLY labels and annotations, plus using generation for spec,
// you get precise detection of "changes that should trigger reconciliation"
// without false positives from controller-internal updates.

// --- Helper ---

func objectHash(obj interface{}) string {
	data, _ := json.Marshal(obj)
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash[:4]) // first 8 hex chars
}

func init() {
	_ = shouldReconcileBad
	_ = shouldReconcileAlsoBad
	_ = shouldReconcile
	_ = demonstrateVersioning
}
