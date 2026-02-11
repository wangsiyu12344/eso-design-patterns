// Pattern 18: Dynamic Informer Management with Reference Counting
//
// Problem: ExternalSecrets can target arbitrary Kubernetes resource types
// (not just Secrets, but also ConfigMaps, custom resources, etc.). The
// controller needs to watch these target resources for changes. But you
// can't pre-register informers for every possible type — there are hundreds
// of resource types and custom resources.
//
// A naive approach creates a new informer for each ExternalSecret. But if
// 50 ExternalSecrets target Secrets, you'd have 50 redundant watchers for
// the same resource type — wasting API server connections and memory.
//
// Solution: Dynamically create informers on-demand, one per GVK (GroupVersionKind),
// and use reference counting to track how many ExternalSecrets use each informer.
// When the last ExternalSecret stops targeting a GVK, the informer is removed.
//
// REAL CODE REFERENCE:
//   pkg/controllers/externalsecret/informer_manager.go:39-311

package eso_advanced_patterns

import (
	"fmt"
	"sync"
)

// =============================================================================
// Anti-Pattern: One Informer Per Resource
// =============================================================================
//
// Each ExternalSecret creates its own watch. With 100 ExternalSecrets targeting
// Secrets, that's 100 watch connections to the API server. The API server starts
// throttling, and the controller uses 100x the memory for duplicate data.

type NaiveWatchManager struct {
	watches map[string]func() // name → stop function
}

func (m *NaiveWatchManager) StartWatch(esName, targetGVK string) {
	// Creates a NEW watch for each ExternalSecret — even if 50 others
	// are already watching the same GVK. Wasteful and unscalable.
	m.watches[esName] = func() { fmt.Println("stopping watch for", esName) }
}

// =============================================================================
// Correct Pattern: Shared Informers with Reference Counting
// =============================================================================
//
// Real code: pkg/controllers/externalsecret/informer_manager.go
//
// Architecture:
//
//   ExternalSecret A ──┐
//   ExternalSecret B ──┼──→ Informer(Secret)     [refcount=3]
//   ExternalSecret C ──┘
//
//   ExternalSecret D ──────→ Informer(ConfigMap)  [refcount=1]
//
//   ExternalSecret E ──┐
//   ExternalSecret F ──┼──→ Informer(MyCustomResource) [refcount=2]
//                      │
//   When F is deleted:  └──→ Informer(MyCustomResource) [refcount=1]
//   When E is deleted:  └──→ (informer removed, no more users)

// GVK represents a GroupVersionKind — the unique type identifier in Kubernetes.
type GVK struct {
	Group   string
	Version string
	Kind    string
}

func (g GVK) String() string {
	return fmt.Sprintf("%s/%s/%s", g.Group, g.Version, g.Kind)
}

// NamespacedName identifies a specific resource instance.
type NamespacedName struct {
	Namespace string
	Name      string
}

// informerEntry tracks one informer and all ExternalSecrets using it.
// Real code: pkg/controllers/externalsecret/informer_manager.go:63-70
type informerEntry struct {
	stopFunc func() // stops the informer

	// Map instead of counter: prevents duplicate reconcile calls from
	// inflating the count. If ES "foo" calls EnsureInformer twice,
	// it still counts as 1 reference.
	externalSecrets map[NamespacedName]struct{}
}

// InformerManager manages the lifecycle of dynamic informers.
// Real code: pkg/controllers/externalsecret/informer_manager.go:72-81
type InformerManager struct {
	mu        sync.RWMutex
	informers map[string]*informerEntry // key: GVK.String()
}

func NewInformerManager() *InformerManager {
	return &InformerManager{
		informers: make(map[string]*informerEntry),
	}
}

// EnsureInformer creates an informer for the GVK if one doesn't exist,
// and registers the ExternalSecret as a user. Returns true if a new
// informer was created.
//
// Real code: pkg/controllers/externalsecret/informer_manager.go:94-147
func (m *InformerManager) EnsureInformer(gvk GVK, es NamespacedName) (created bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := gvk.String()

	// If informer already exists, just register this ES as a user
	if entry, exists := m.informers[key]; exists {
		entry.externalSecrets[es] = struct{}{} // idempotent (map, not counter)
		fmt.Printf("registered %s/%s with existing informer for %s (total users: %d)\n",
			es.Namespace, es.Name, key, len(entry.externalSecrets))
		return false
	}

	// Create new informer
	// In real code: cache.GetInformerForKind(ctx, gvk)
	stopFunc := startInformer(gvk)

	m.informers[key] = &informerEntry{
		stopFunc:        stopFunc,
		externalSecrets: map[NamespacedName]struct{}{es: {}},
	}

	fmt.Printf("created new informer for %s (first user: %s/%s)\n",
		key, es.Namespace, es.Name)
	return true
}

// ReleaseInformer unregisters an ExternalSecret. If no more ESes use the
// informer, it's stopped and removed.
//
// Real code: pkg/controllers/externalsecret/informer_manager.go:223-266
func (m *InformerManager) ReleaseInformer(gvk GVK, es NamespacedName) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := gvk.String()

	entry, exists := m.informers[key]
	if !exists {
		// Already removed or never existed. This can happen during error recovery
		// or if EnsureInformer failed. Not an error — just a no-op.
		return
	}

	// Remove this ES from the reference set
	delete(entry.externalSecrets, es)
	fmt.Printf("unregistered %s/%s from informer %s (remaining users: %d)\n",
		es.Namespace, es.Name, key, len(entry.externalSecrets))

	// If no more users, stop and remove the informer
	if len(entry.externalSecrets) == 0 {
		entry.stopFunc()
		delete(m.informers, key)
		fmt.Printf("removed informer for %s (no more users)\n", key)
	}
}

// IsManaged checks if a GVK is currently being watched.
func (m *InformerManager) IsManaged(gvk GVK) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.informers[gvk.String()]
	return exists
}

// =============================================================================
// Usage Example
// =============================================================================

func demonstrateInformerManager() {
	mgr := NewInformerManager()

	secretGVK := GVK{Group: "", Version: "v1", Kind: "Secret"}
	configMapGVK := GVK{Group: "", Version: "v1", Kind: "ConfigMap"}

	esA := NamespacedName{Namespace: "default", Name: "es-a"}
	esB := NamespacedName{Namespace: "default", Name: "es-b"}
	esC := NamespacedName{Namespace: "default", Name: "es-c"}

	// Three ESes target Secrets → one informer, refcount=3
	mgr.EnsureInformer(secretGVK, esA) // creates informer
	mgr.EnsureInformer(secretGVK, esB) // reuses informer
	mgr.EnsureInformer(secretGVK, esC) // reuses informer

	// One ES targets ConfigMaps → separate informer, refcount=1
	mgr.EnsureInformer(configMapGVK, esC)

	// ES-A is deleted → Secret informer refcount drops to 2
	mgr.ReleaseInformer(secretGVK, esA)

	// ES-B is deleted → Secret informer refcount drops to 1
	mgr.ReleaseInformer(secretGVK, esB)

	// ES-C is deleted → both informers are removed
	mgr.ReleaseInformer(secretGVK, esC)    // Secret informer: refcount=0, removed
	mgr.ReleaseInformer(configMapGVK, esC) // ConfigMap informer: refcount=0, removed
}

// KEY INSIGHTS:
//
// 1. Map vs Counter: Using map[NamespacedName]struct{} instead of int prevents
//    double-counting when the same ES reconciles multiple times.
//
// 2. RWMutex: EnsureInformer/ReleaseInformer use write lock. IsManaged uses
//    read lock. This allows concurrent read checks without blocking.
//
// 3. Graceful degradation: ReleaseInformer doesn't error if the informer
//    doesn't exist — this handles startup failures and error recovery.
//
// 4. Event routing: Each informer has an event handler that maps resource
//    changes back to the ExternalSecrets that reference them, using field
//    indexes (see util.go for the indexing pattern).

// --- Helper ---

func startInformer(gvk GVK) func() {
	return func() {
		fmt.Printf("stopped informer for %s\n", gvk.String())
	}
}

func init() {
	_ = demonstrateInformerManager
}
