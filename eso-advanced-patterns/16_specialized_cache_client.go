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
	"sync"
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

// DefaultCache stores ALL objects — no filtering.
type DefaultCache struct {
	store map[string]CachedObject // key → object
}

func NewDefaultCache() *DefaultCache {
	return &DefaultCache{store: make(map[string]CachedObject)}
}

func (c *DefaultCache) Add(obj CachedObject) {
	c.store[obj.Key()] = obj
}

func (c *DefaultCache) Get(key string) (CachedObject, bool) {
	obj, ok := c.store[key]
	return obj, ok
}

func (c *DefaultCache) List() []CachedObject {
	result := make([]CachedObject, 0, len(c.store))
	for _, obj := range c.store {
		result = append(result, obj)
	}
	return result
}

func (c *DefaultCache) Size() int {
	return len(c.store)
}

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

// LabelSelector defines a filter: key=value pairs that objects must have.
// In real code, this is labels.Selector from k8s.io/apimachinery.
type LabelSelector struct {
	Requirements map[string]string // label key → required value
}

// Matches returns true if the object's labels satisfy all requirements.
func (s LabelSelector) Matches(labels map[string]string) bool {
	for key, requiredValue := range s.Requirements {
		if labels[key] != requiredValue {
			return false
		}
	}
	return true
}

// LabelFilteredCache only stores objects whose labels match the selector.
// This is what controller-runtime's cache.Options.ByObject does under the hood.
//
// Real code: cache.Options{
//
//	ByObject: map[client.Object]cache.ByObject{
//	    &corev1.Secret{}: {Label: managedLabelSelector},
//	},
//
// }
type LabelFilteredCache struct {
	mu       sync.RWMutex
	store    map[string]CachedObject
	selector LabelSelector

	// registeredTypes tracks which resource types have informers.
	// When failOnMissing=true, Get/List on unregistered types returns an error.
	registeredTypes map[string]bool

	// failOnMissing mirrors ReaderFailOnMissingInformer in controller-runtime.
	// true  = Get/List for unregistered type → error (fail-fast, catches bugs)
	// false = Get/List for unregistered type → silent direct API call (hides bugs)
	failOnMissing bool
}

func NewLabelFilteredCache(selector LabelSelector, failOnMissing bool) *LabelFilteredCache {
	return &LabelFilteredCache{
		store:           make(map[string]CachedObject),
		selector:        selector,
		registeredTypes: make(map[string]bool),
		failOnMissing:   failOnMissing,
	}
}

// RegisterType declares that this cache handles a specific resource type.
// In real code: secretCache.GetInformer(ctx, &corev1.Secret{})
//
// Because ReaderFailOnMissingInformer=true, we MUST explicitly register
// types. This is documentation-as-code: it declares "this cache handles
// Secrets, nothing else."
func (c *LabelFilteredCache) RegisterType(resourceType string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.registeredTypes[resourceType] = true
}

// OnEvent processes an incoming watch event from the API server.
// Only objects matching the label selector enter the cache.
// The watch connection itself is filtered server-side via the label selector,
// so most non-matching events never even reach the client.
func (c *LabelFilteredCache) OnEvent(obj CachedObject) {
	// Label selector filtering: only cache objects that match
	if !c.selector.Matches(obj.Labels) {
		return // skip — this object isn't managed by us
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.store[obj.Key()] = obj
}

// Get retrieves an object from the cache.
// If failOnMissing=true and the type has no registered informer, returns error.
func (c *LabelFilteredCache) Get(resourceType, key string) (CachedObject, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Fail-fast: reject queries for types this cache doesn't handle.
	// Without this, the client silently falls back to a direct API call,
	// which is uncached, slow, and hides the bug.
	if c.failOnMissing && !c.registeredTypes[resourceType] {
		return CachedObject{}, fmt.Errorf(
			"no informer registered for type %q: this cache only handles %v",
			resourceType, c.registeredTypesList(),
		)
	}

	obj, ok := c.store[key]
	if !ok {
		return CachedObject{}, fmt.Errorf("object %q not found in cache", key)
	}
	return obj, nil
}

// List returns all cached objects of a given type.
func (c *LabelFilteredCache) List(resourceType string) ([]CachedObject, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.failOnMissing && !c.registeredTypes[resourceType] {
		return nil, fmt.Errorf(
			"no informer registered for type %q: this cache only handles %v",
			resourceType, c.registeredTypesList(),
		)
	}

	var result []CachedObject
	for _, obj := range c.store {
		if obj.Type == resourceType {
			result = append(result, obj)
		}
	}
	return result, nil
}

func (c *LabelFilteredCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.store)
}

func (c *LabelFilteredCache) registeredTypesList() []string {
	var types []string
	for t := range c.registeredTypes {
		types = append(types, t)
	}
	return types
}

// =============================================================================
// CachedClient: Reads from cache, writes to API server
// =============================================================================
//
// Real code:
//
//	secretClient, _ := client.New(mgr.GetConfig(), client.Options{
//	    Cache: &client.CacheOptions{Reader: secretCache},
//	})
//
// Writes go directly to the API server (no caching needed for writes).
// Reads go through the label-filtered cache.

type CachedClient struct {
	cache     *LabelFilteredCache
	apiServer *MockAPIServer // direct connection for writes
}

func NewCachedClient(cache *LabelFilteredCache, apiServer *MockAPIServer) *CachedClient {
	return &CachedClient{cache: cache, apiServer: apiServer}
}

// Get reads from the filtered cache (fast, in-memory).
func (c *CachedClient) Get(resourceType, key string) (CachedObject, error) {
	return c.cache.Get(resourceType, key)
}

// List reads from the filtered cache.
func (c *CachedClient) List(resourceType string) ([]CachedObject, error) {
	return c.cache.List(resourceType)
}

// Create writes directly to the API server (bypasses cache).
// The cache will pick up the new object via the watch event.
func (c *CachedClient) Create(obj CachedObject) error {
	return c.apiServer.Create(obj)
}

// Update writes directly to the API server.
func (c *CachedClient) Update(obj CachedObject) error {
	return c.apiServer.Update(obj)
}

// =============================================================================
// Usage: Building the Specialized Client (mirrors real code flow)
// =============================================================================
//
// Real code: pkg/controllers/common/common.go:37-96

func BuildManagedSecretClient(apiServer *MockAPIServer, namespace string) *CachedClient {
	// Step 1: Define what to cache — only secrets with our label.
	// Real code:
	//   managedLabelReq, _ := labels.NewRequirement(
	//       "reconcile.external-secrets.io/managed",
	//       selection.Equals, []string{"true"})
	//   managedLabelSelector := labels.NewSelector().Add(*managedLabelReq)
	selector := LabelSelector{
		Requirements: map[string]string{
			"reconcile.external-secrets.io/managed": "true",
		},
	}

	// Step 2: Create cache with label selector and fail-fast mode.
	// Real code:
	//   secretCacheOpts := cache.Options{
	//       ByObject: map[client.Object]cache.ByObject{
	//           &corev1.Secret{}: {Label: managedLabelSelector},
	//       },
	//       ReaderFailOnMissingInformer: true,  // ← fail-fast safety net
	//   }
	secretCache := NewLabelFilteredCache(selector, true)

	// Step 3: Register the type this cache handles.
	// Real code: secretCache.GetInformer(ctx, &corev1.Secret{})
	//
	// Because failOnMissing=true, we MUST explicitly register types.
	// Trying to Get/List an unregistered type will return an error.
	secretCache.RegisterType("Secret")

	// Step 4 (optional): Namespace restriction for single-namespace mode.
	// Real code:
	//   if namespace != "" {
	//       secretCacheOpts.DefaultNamespaces = map[string]cache.Config{
	//           namespace: {},
	//       }
	//   }
	if namespace != "" {
		fmt.Printf("cache restricted to namespace %q\n", namespace)
	}

	// Step 5: Build client that reads from cache, writes to API server.
	// Real code:
	//   secretClient, _ := client.New(mgr.GetConfig(), client.Options{
	//       Cache: &client.CacheOptions{Reader: secretCache},
	//   })
	client := NewCachedClient(secretCache, apiServer)

	// Simulate watch events: the cache receives events from the API server
	// and only stores objects matching the label selector.
	for _, obj := range apiServer.ListAll() {
		secretCache.OnEvent(obj)
	}

	return client
}

// =============================================================================
// Demonstration: Default Cache vs Specialized Cache
// =============================================================================

func demonstrateBuildManagedSecretClient() {
	apiServer := NewMockAPIServer()

	// Simulate a cluster with 10,000 secrets, only 200 are managed by ESO
	for i := 0; i < 10000; i++ {
		labels := map[string]string{}
		if i < 200 {
			labels["reconcile.external-secrets.io/managed"] = "true"
		}
		apiServer.Create(CachedObject{
			Type:      "Secret",
			Name:      fmt.Sprintf("secret-%d", i),
			Namespace: "default",
			Labels:    labels,
		})
	}

	// Anti-pattern: default cache stores everything
	defaultCache := NewDefaultCache()
	for _, obj := range apiServer.ListAll() {
		defaultCache.Add(obj)
	}
	fmt.Printf("Default cache:     %d secrets cached (ALL of them)\n", defaultCache.Size())

	// Correct pattern: specialized cache stores only managed secrets
	client := BuildManagedSecretClient(apiServer, "")
	fmt.Printf("Specialized cache: %d secrets cached (only managed)\n", client.cache.Size())

	// Reads work for registered type (Secret)
	obj, err := client.Get("Secret", "default/secret-0")
	if err == nil {
		fmt.Printf("Got managed secret: %s\n", obj.Name)
	}

	// Reads FAIL for unregistered type (Deployment) — this is the fail-fast behavior
	_, err = client.Get("Deployment", "default/my-deploy")
	if err != nil {
		fmt.Printf("Fail-fast error: %s\n", err)
	}

	// Reads FAIL for unmanaged secrets (not in cache because label doesn't match)
	_, err = client.Get("Secret", "default/secret-9999")
	if err != nil {
		fmt.Printf("Unmanaged secret not in cache: %s\n", err)
	}
}

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

// --- Helper types ---

type CachedObject struct {
	Type      string
	Name      string
	Namespace string
	Labels    map[string]string
	Data      map[string]string
}

func (o CachedObject) Key() string {
	return o.Namespace + "/" + o.Name
}

type MockAPIServer struct {
	objects []CachedObject
}

func NewMockAPIServer() *MockAPIServer {
	return &MockAPIServer{}
}

func (s *MockAPIServer) Create(obj CachedObject) error {
	s.objects = append(s.objects, obj)
	return nil
}

func (s *MockAPIServer) Update(obj CachedObject) error {
	for i, existing := range s.objects {
		if existing.Key() == obj.Key() {
			s.objects[i] = obj
			return nil
		}
	}
	return fmt.Errorf("object %q not found", obj.Key())
}

func (s *MockAPIServer) ListAll() []CachedObject {
	return s.objects
}

func init() {
	_ = demonstrateBuildManagedSecretClient
}
