// Pattern 2: Provider Registry via init() (Plugin Pattern)
//
// This pattern allows the project to support 30+ secret providers (AWS, Vault, GCP, Azure, etc.)
// WITHOUT the core reconciler knowing anything about any of them.
//
// HOW IT WORKS:
//   1. A global map stores provider name -> provider implementation
//   2. Each provider file has an init() function that registers itself
//   3. Build tags control which providers are compiled in
//   4. A blank import in main.go triggers all init() functions
//   5. At runtime, the reconciler looks up the provider by name
//
// REAL CODE REFERENCES:
//   apis/externalsecrets/v1/provider_schema.go     - the registry (Register, GetProvider)
//   pkg/register/aws.go                            - AWS registration
//   pkg/register/vault.go                          - Vault registration
//   main.go:23                                     - blank import triggers registration

package guide

import (
	"encoding/json"
	"fmt"
	"sync"
)

// =============================================================================
// STEP 1: Define the Provider interface
// =============================================================================
// All providers must implement this interface.
// The reconciler only talks to this interface, never to concrete types.
//
// Real code: the Provider interface in apis/externalsecrets/v1/

type SecretProvider interface {
	// NewClient creates an authenticated client for this provider
	NewClient(config map[string]string) (SecretClient, error)
}

type SecretClient interface {
	GetSecret(key string) ([]byte, error)
	GetSecretMap(key string) (map[string][]byte, error)
}

// =============================================================================
// STEP 2: The Global Registry
// =============================================================================
// A simple map protected by a mutex. Providers register themselves here.
//
// Why a global variable? Because init() functions run before main(), so there's
// no opportunity to pass a registry via dependency injection. The global map is
// the standard Go pattern for self-registering plugins (used by database/sql,
// image/png, etc.). The mutex is needed because init() execution order across
// packages is not guaranteed by the Go spec.
//
// Real code: apis/externalsecrets/v1/provider_schema.go:26-50

var (
	providerRegistry     = make(map[string]SecretProvider)
	providerRegistryLock sync.RWMutex
)

// Register adds a provider to the global registry.
// Panics if a provider with the same name already exists — this is intentional.
// A duplicate registration means two packages are claiming the same provider name,
// which is always a bug. Panicking at startup (during init()) is preferable to
// silently overwriting a provider at runtime, which would be extremely hard to debug.
//
// Real code: apis/externalsecrets/v1/provider_schema.go:35-50
func RegisterProvider(name string, provider SecretProvider) {
	providerRegistryLock.Lock()
	defer providerRegistryLock.Unlock()

	if _, exists := providerRegistry[name]; exists {
		panic(fmt.Sprintf("provider %q already registered", name))
	}
	providerRegistry[name] = provider
}

// GetProviderByName looks up a provider from the registry.
//
// Real code: apis/externalsecrets/v1/provider_schema.go:67-72
func GetProviderByName(name string) (SecretProvider, bool) {
	providerRegistryLock.RLock()
	defer providerRegistryLock.RUnlock()

	p, ok := providerRegistry[name]
	return p, ok
}

// GetProviderFromSpec determines which provider to use based on the store spec.
// It marshals the spec to JSON and finds the one non-nil field.
//
// This is an elegant approach to polymorphic dispatch: the SecretStore spec is a
// Go struct with one field per provider (AWS, Vault, GCP, etc.), and exactly one
// must be non-nil. By marshaling to JSON and inspecting the keys, the code avoids
// a giant switch/case and automatically supports any new provider that's registered.
//
// For example: {"aws": {"region": "us-east-1"}} → provider name is "aws"
//
// Real code: apis/externalsecrets/v1/provider_schema.go:75-100
func GetProviderFromSpec(storeSpec map[string]interface{}) (SecretProvider, error) {
	// Marshal and inspect to find which provider is configured
	specBytes, _ := json.Marshal(storeSpec)
	specMap := make(map[string]interface{})
	json.Unmarshal(specBytes, &specMap)

	if len(specMap) != 1 {
		return nil, fmt.Errorf("exactly one provider must be specified, found %d", len(specMap))
	}

	for name := range specMap {
		p, ok := GetProviderByName(name)
		if !ok {
			return nil, fmt.Errorf("unknown provider: %s", name)
		}
		return p, nil
	}
	return nil, fmt.Errorf("no provider found")
}

// =============================================================================
// STEP 3: Concrete Provider Implementations
// =============================================================================
// Each provider is in its own package and file. It knows nothing about other providers.

// --- AWS Provider ---
// Real file: providers/v1/aws/
type AWSProvider struct{}

func (p *AWSProvider) NewClient(config map[string]string) (SecretClient, error) {
	// Create AWS Secrets Manager client using config["region"], config["role"], etc.
	return &AWSClient{region: config["region"]}, nil
}

type AWSClient struct{ region string }

func (c *AWSClient) GetSecret(key string) ([]byte, error) {
	// Call AWS Secrets Manager API: secretsmanager.GetSecretValue(key)
	return []byte("aws-secret-value"), nil
}
func (c *AWSClient) GetSecretMap(key string) (map[string][]byte, error) {
	return map[string][]byte{"key": []byte("value")}, nil
}

// --- Vault Provider ---
// Real file: providers/v1/vault/
type VaultProvider struct{}

func (p *VaultProvider) NewClient(config map[string]string) (SecretClient, error) {
	// Create HashiCorp Vault client using config["server"], config["path"], etc.
	return &VaultClient{server: config["server"]}, nil
}

type VaultClient struct{ server string }

func (c *VaultClient) GetSecret(key string) ([]byte, error) {
	// Call Vault API: vault.Logical().Read(key)
	return []byte("vault-secret-value"), nil
}
func (c *VaultClient) GetSecretMap(key string) (map[string][]byte, error) {
	return map[string][]byte{"key": []byte("value")}, nil
}

// =============================================================================
// STEP 4: Registration via init()
// =============================================================================
// Each provider file registers itself when imported.
// Build tags control which files are compiled.
//
// The init() + build tag combination is powerful:
//   - init() runs automatically when a package is imported — no manual wiring needed
//   - Build tags (//go:build aws || all_providers) let you compile only the
//     providers you need, reducing binary size and attack surface
//   - For example, `go build -tags=aws,vault` produces a binary with only
//     AWS and Vault support; `go build -tags=all_providers` includes everything
//
// Real file: pkg/register/aws.go
//   //go:build aws || all_providers
//   func init() {
//       esv1.Register(aws.NewProvider(), aws.ProviderSpec(), aws.MaintenanceStatus())
//   }
//
// Real file: pkg/register/vault.go
//   //go:build vault || all_providers
//   func init() {
//       esv1.Register(vault.NewProvider(), vault.ProviderSpec(), vault.MaintenanceStatus())
//   }

func init() {
	// In the real project, each provider registers in its own init() in a separate file.
	// Here we show them together for illustration.
	RegisterProvider("aws", &AWSProvider{})
	RegisterProvider("vault", &VaultProvider{})
	// RegisterProvider("gcp", &GCPProvider{})
	// RegisterProvider("azure", &AzureProvider{})
	// ... 30+ more providers
}

// =============================================================================
// STEP 5: Blank Import in main.go
// =============================================================================
// The blank import triggers all init() functions in the register package.
//
// Real code: main.go:23
//   import (
//       _ "github.com/external-secrets/external-secrets/pkg/register"
//   )
//
// The underscore (_) means "import for side effects only" — we don't use any
// exported names, we just want the init() functions to run.

// =============================================================================
// STEP 6: Usage in Reconciler
// =============================================================================
// The reconciler looks up the provider by name and uses the interface.
// It has ZERO knowledge of AWS, Vault, or any specific provider.
//
// Real code: called from externalsecret_controller.go:399
//   dataMap, err := r.GetProviderSecretData(ctx, externalSecret)

func ExampleReconcilerUsage() {
	// The SecretStore spec tells us which provider to use:
	//   SecretStore:
	//     spec:
	//       provider:
	//         aws:
	//           region: us-east-1
	storeSpec := map[string]interface{}{
		"aws": map[string]string{"region": "us-east-1"},
	}

	// Look up the provider — the reconciler doesn't know it's AWS
	provider, err := GetProviderFromSpec(storeSpec)
	if err != nil {
		panic(err)
	}

	// Create a client — could be AWS, Vault, GCP... the code is the same
	client, err := provider.NewClient(map[string]string{"region": "us-east-1"})
	if err != nil {
		panic(err)
	}

	// Fetch the secret — generic interface, works for any provider
	secret, _ := client.GetSecret("my-secret-key")
	fmt.Println(string(secret)) // "aws-secret-value"
}

// KEY INSIGHT:
// Adding a new provider (e.g. "digitalocean") requires:
//   1. Create providers/v1/digitalocean/ with DigitalOceanProvider implementing SecretProvider
//   2. Create pkg/register/digitalocean.go with init() { Register(...) }
//   3. Add build tag: //go:build digitalocean || all_providers
//
// ZERO changes to the reconciler or any existing provider code.
// This is the Open/Closed Principle in action: open for extension, closed for modification.
