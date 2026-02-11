// Pattern 5: Interface-Based Provider Abstraction (Strategy Pattern)
//
// Problem: The reconciler needs to fetch secrets from 30+ different providers
// (AWS, Vault, GCP, Azure, etc.). Without abstraction, the reconciler would
// be full of provider-specific code:
//
//   if provider == "aws" { ... }
//   else if provider == "vault" { ... }
//   else if provider == "gcp" { ... }
//   // 30+ more branches...
//
// Solution: Define an interface. Every provider implements it.
// The reconciler only talks to the interface.
//
// This is the classic Strategy Pattern — the algorithm (how to fetch a secret)
// varies by provider, but the reconciler doesn't need to know which one is used.
//
// REAL CODE REFERENCES:
//   apis/externalsecrets/v1/provider_schema.go    - GetProvider() returns the interface
//   externalsecret_controller.go:399              - reconciler calls GetProviderSecretData()
//   Each provider in providers/v1/                - implements the interface

package guide

import (
	"context"
	"fmt"
)

// =============================================================================
// The Interface
// =============================================================================
// This is what every provider must implement.
// The reconciler only knows about this interface.

// SecretsProvider creates authenticated clients.
// Corresponds to the Provider interface in the real code.
//
// Note the two-phase design: Provider creates a Client, Client fetches secrets.
// This separation exists because authentication (NewClient) is expensive and
// can be reused across multiple GetSecret calls. The reconciler creates ONE
// client per reconciliation, then makes multiple calls to fetch different keys.
type SecretsProvider interface {
	NewClient(ctx context.Context, store StoreConfig) (SecretsClient, error)
}

// SecretsClient fetches secrets from the external provider.
// Corresponds to the client interface each provider returns.
//
// GetSecret returns a single value; GetSecretMap returns a key-value map.
// The distinction matters because some providers store structured data
// (e.g., a JSON object with multiple fields), while others store flat values.
// Close() is called via defer to release any resources (connections, tokens, etc.).
type SecretsClient interface {
	GetSecret(ctx context.Context, key string) ([]byte, error)
	GetSecretMap(ctx context.Context, key string) (map[string][]byte, error)
	Close(ctx context.Context) error
}

type StoreConfig struct {
	Provider   string
	Region     string
	Server     string
	Path       string
	AuthConfig map[string]string
}

// =============================================================================
// Provider Implementations
// =============================================================================
// Each provider is a completely independent package.
// They know nothing about each other.

// --- AWS Secrets Manager ---
type awsSecretsProvider struct{}

func (p *awsSecretsProvider) NewClient(ctx context.Context, store StoreConfig) (SecretsClient, error) {
	// In reality: create AWS session, assume IAM role, create SM client
	fmt.Println("AWS: creating client for region", store.Region)
	return &awsSecretsClient{region: store.Region}, nil
}

type awsSecretsClient struct{ region string }

func (c *awsSecretsClient) GetSecret(ctx context.Context, key string) ([]byte, error) {
	// In reality: secretsmanager.GetSecretValue(&sm.GetSecretValueInput{SecretId: &key})
	fmt.Println("AWS: fetching secret", key, "from region", c.region)
	return []byte("aws-secret-value"), nil
}
func (c *awsSecretsClient) GetSecretMap(ctx context.Context, key string) (map[string][]byte, error) {
	return map[string][]byte{"username": []byte("admin"), "password": []byte("s3cret")}, nil
}
func (c *awsSecretsClient) Close(ctx context.Context) error { return nil }

// --- HashiCorp Vault ---
type vaultProvider struct{}

func (p *vaultProvider) NewClient(ctx context.Context, store StoreConfig) (SecretsClient, error) {
	fmt.Println("Vault: creating client for server", store.Server)
	return &vaultClient{server: store.Server}, nil
}

type vaultClient struct{ server string }

func (c *vaultClient) GetSecret(ctx context.Context, key string) ([]byte, error) {
	// In reality: vault.Logical().Read(key)
	fmt.Println("Vault: fetching secret", key, "from", c.server)
	return []byte("vault-secret-value"), nil
}
func (c *vaultClient) GetSecretMap(ctx context.Context, key string) (map[string][]byte, error) {
	return map[string][]byte{"token": []byte("hvs.abc123")}, nil
}
func (c *vaultClient) Close(ctx context.Context) error { return nil }

// --- GCP Secret Manager ---
type gcpProvider struct{}

func (p *gcpProvider) NewClient(ctx context.Context, store StoreConfig) (SecretsClient, error) {
	fmt.Println("GCP: creating client for project", store.AuthConfig["project"])
	return &gcpClient{}, nil
}

type gcpClient struct{}

func (c *gcpClient) GetSecret(ctx context.Context, key string) ([]byte, error) {
	// In reality: secretmanager.AccessSecretVersion(name)
	fmt.Println("GCP: fetching secret", key)
	return []byte("gcp-secret-value"), nil
}
func (c *gcpClient) GetSecretMap(ctx context.Context, key string) (map[string][]byte, error) {
	return map[string][]byte{"key": []byte("value")}, nil
}
func (c *gcpClient) Close(ctx context.Context) error { return nil }

// =============================================================================
// The Reconciler — provider-agnostic
// =============================================================================
// This is the key: the reconciler has ZERO imports from any provider package.
// It only works with the SecretsProvider and SecretsClient interfaces.
//
// Real code: externalsecret_controller.go:399
//   dataMap, err := r.GetProviderSecretData(ctx, externalSecret)
//
// GetProviderSecretData internally does:
//   1. Look up the SecretStore to find which provider is configured
//   2. Call esv1.GetProvider(store) to get the Provider interface
//   3. Call provider.NewClient() to get an authenticated client
//   4. Call client.GetSecret() or client.GetSecretMap()

func ExampleReconciler() {
	ctx := context.Background()

	// Simulate: the SecretStore says "use AWS in us-east-1"
	store := StoreConfig{Provider: "aws", Region: "us-east-1"}

	// The reconciler gets the provider from the registry (Pattern 2)
	// It doesn't know or care that it's AWS
	var provider SecretsProvider = &awsSecretsProvider{} // returned by GetProvider(store)

	// Create an authenticated client
	client, err := provider.NewClient(ctx, store)
	if err != nil {
		panic(err)
	}
	defer client.Close(ctx)

	// Fetch the secret — same code works for ANY provider
	data, err := client.GetSecret(ctx, "my-app/database-credentials")
	if err != nil {
		panic(err)
	}

	fmt.Println("Secret data:", string(data))

	// Now swap to Vault — the reconciler code is IDENTICAL
	store2 := StoreConfig{Provider: "vault", Server: "https://vault.example.com"}
	var provider2 SecretsProvider = &vaultProvider{}
	client2, _ := provider2.NewClient(ctx, store2)
	defer client2.Close(ctx)
	data2, _ := client2.GetSecret(ctx, "secret/data/my-app")
	fmt.Println("Secret data:", string(data2))

	// The reconciler code above is the same for both providers.
	// The only difference is which Provider implementation was returned by the registry.
}

// KEY INSIGHT:
//
// The reconciler (~1000 lines) handles 30+ providers with zero provider-specific code.
// When someone adds a new provider:
//   - They implement SecretsProvider and SecretsClient
//   - They register via init() (Pattern 2)
//   - The reconciler works with it automatically
//
// Benefits:
//   - Single Responsibility: each provider only knows about its own API
//   - Open/Closed: add new providers without modifying the reconciler
//   - Testable: mock the interface in unit tests
//   - No giant switch/case or if/else chains
