// Pattern 17: Sentinel Errors for Control Flow
//
// Problem: In a secrets provider, "secret not found" is NOT an error — it's
// expected behavior that should trigger deletion policy logic. But the provider
// returns an error type. Without a way to distinguish "not found" from "auth
// failed" or "network timeout", the controller treats all errors the same way:
// retry and log an error. This causes false alerts and incorrect behavior.
//
// Solution: Define sentinel errors (well-known error values) that signal
// specific conditions. The controller checks errors.Is(err, sentinel) to
// determine what happened and take the correct action.
//
// WHY SENTINEL ERRORS OVER ERROR CODES:
//   - errors.Is() works through wrapping: fmt.Errorf("provider X: %w", NoSecretErr)
//     still matches errors.Is(err, NoSecretErr)
//   - No stringly-typed comparisons: if err.Error() == "not found" is fragile
//   - Type-safe: the compiler ensures you use the right type
//   - Standard Go idiom: errors.Is/As is the expected pattern since Go 1.13
//
// REAL CODE REFERENCE:
//   apis/externalsecrets/v1/provider.go:102-123

package eso_advanced_patterns

import (
	"errors"
	"fmt"
)

// =============================================================================
// Anti-Pattern: String-Based Error Checking
// =============================================================================
//
// This is fragile, breaks if any provider changes their error message,
// and doesn't survive error wrapping.

func handleErrorBad(err error) string {
	if err == nil {
		return "success"
	}
	// FRAGILE: What if the message is "secret does not exist" vs "Secret does not exist"?
	// What if it's wrapped: "provider aws: Secret does not exist"?
	if err.Error() == "Secret does not exist" {
		return "not found"
	}
	return "error"
}

// =============================================================================
// Correct Pattern: Sentinel Errors with Custom Types
// =============================================================================
//
// Real code: apis/externalsecrets/v1/provider.go:102-123

// NoSecretErr signals that a secret does not exist in the provider.
// This is NOT an error condition — it's expected behavior used to trigger
// deletion policy logic ("the source secret is gone, should we delete the
// Kubernetes secret too?").
var NoSecretErr = NoSecretError{}

type NoSecretError struct{}

func (NoSecretError) Error() string {
	return "Secret does not exist"
}

// NotModifiedErr signals that a webhook received no changes.
// The webhook should return success without doing any work.
// This prevents unnecessary Secret updates that would trigger
// dependent controller reconciliations.
var NotModifiedErr = NotModifiedError{}

type NotModifiedError struct{}

func (NotModifiedError) Error() string {
	return "not modified"
}

// =============================================================================
// Usage: errors.Is() Through Wrapping
// =============================================================================
//
// The beauty of sentinel errors is that errors.Is() traverses the wrap chain.
// A provider can wrap the sentinel with context, and the controller still
// detects it correctly.

// Provider implementation — wraps sentinel with context
func getSecretFromProvider(key string) ([]byte, error) {
	// ... try to fetch from provider ...
	exists := false
	if !exists {
		// Wrap with context but preserve the sentinel for errors.Is()
		return nil, fmt.Errorf("key %q in store %q: %w", key, "aws-store", NoSecretErr)
	}
	return []byte("secret-value"), nil
}

// Controller — checks sentinel to determine action
func reconcileSecret(key string) error {
	data, err := getSecretFromProvider(key)
	if err != nil {
		// errors.Is traverses the wrap chain:
		// "key \"db-password\" in store \"aws-store\": Secret does not exist"
		// → still matches NoSecretErr!
		if errors.Is(err, NoSecretErr) {
			// Not an error — the source secret was intentionally deleted.
			// Apply deletion policy: delete the K8s secret, or keep it, etc.
			return applyDeletionPolicy(key)
		}
		// Actual error (auth failure, network timeout, etc.)
		// This SHOULD be retried with backoff.
		return err
	}

	// Secret exists — create or update the K8s secret
	return createOrUpdateSecret(key, data)
}

// =============================================================================
// When to Use Sentinel Errors vs. Other Approaches
// =============================================================================
//
// Use sentinel errors when:
//   - A return value signals a CONDITION, not a failure
//   - Multiple callers need to check for the same condition
//   - The condition can occur at any level of the call stack (wrapping)
//
// Don't use sentinel errors when:
//   - The condition is local to one function (just use a bool return)
//   - You need to carry data with the error (use errors.As with a typed error)
//   - The error is truly unexpected (just return a regular error)
//
// ESO's choice of NoSecretErr and NotModifiedErr is perfect because:
//   - "Secret not found" can come from any of 20+ provider implementations
//   - The controller needs to check for it regardless of which provider returned it
//   - The condition survives wrapping through multiple layers of the call stack

// --- Helper functions ---

func applyDeletionPolicy(key string) error {
	fmt.Printf("applying deletion policy for %s\n", key)
	return nil
}

func createOrUpdateSecret(key string, data []byte) error {
	fmt.Printf("creating/updating secret %s\n", key)
	return nil
}

func init() {
	_ = handleErrorBad
	_ = reconcileSecret
}
