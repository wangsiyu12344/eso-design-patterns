// Pattern 8: Refresh Gating (Skip Unnecessary Work)
//
// Problem: Reconcile() is called frequently — on every change to the ExternalSecret,
// on every change to the managed Secret, and on every refresh interval tick.
// Calling the external provider (AWS, Vault, etc.) every time is:
//   - Slow (network round trips)
//   - Expensive (API calls may cost money)
//   - Rate-limited (providers have API rate limits)
//
// Solution: Check a set of conditions BEFORE calling the provider.
// If everything is already in sync, skip the provider call entirely.
//
// REAL CODE REFERENCE:
//   externalsecret_controller.go:354-357   (the skip check)
//   externalsecret_controller.go:1103-1149 (shouldRefresh logic)
//   externalsecret_controller.go:1152-1174 (isSecretValid logic)

package guide

import (
	"fmt"
	"time"
)

// =============================================================================
// The Gating Check
// =============================================================================
//
// Real code: externalsecret_controller.go:354-357
//
//   if !shouldRefresh(externalSecret) && isSecretValid(existingSecret, externalSecret) {
//       log.V(1).Info("skipping refresh")
//       return r.getRequeueResult(externalSecret), nil
//   }

type ExternalSecretStatus struct {
	SyncedResourceVersion string
	RefreshTime           time.Time
}

type ExternalSecretSpec struct {
	RefreshInterval time.Duration
	RefreshPolicy   string // "Periodic", "OnChange", "CreatedOnce"
	Generation      int64  // incremented by Kubernetes on every spec change
}

type SecretState struct {
	Exists          bool
	HasManagedLabel bool
	DataHash        string // hash of the secret's data
}

// shouldRefresh determines if we need to call the external provider.
//
// Real code: externalsecret_controller.go:1103-1149
func shouldRefresh(spec ExternalSecretSpec, status ExternalSecretStatus, currentGeneration int64) bool {
	switch spec.RefreshPolicy {

	// "CreatedOnce" — only fetch once, ever.
	// Useful for secrets that never change (e.g., static API keys).
	case "CreatedOnce":
		if status.SyncedResourceVersion == "" || status.RefreshTime.IsZero() {
			return true // never synced before
		}
		return false // already synced, never refresh again

	// "OnChange" — only fetch when the ExternalSecret spec changes.
	// Useful when you want manual control over refreshes.
	case "OnChange":
		if status.SyncedResourceVersion == "" || status.RefreshTime.IsZero() {
			return true // never synced before
		}
		return status.SyncedResourceVersion != fmt.Sprint(currentGeneration)

	// "Periodic" (default) — fetch on a timer AND when spec changes.
	default:
		return shouldRefreshPeriodic(spec, status, currentGeneration)
	}
}

// shouldRefreshPeriodic checks if the refresh interval has elapsed.
//
// Real code: externalsecret_controller.go:1126-1149
func shouldRefreshPeriodic(spec ExternalSecretSpec, status ExternalSecretStatus, currentGeneration int64) bool {
	// If refresh interval is 0 and we've synced before, never refresh again
	if spec.RefreshInterval <= 0 && status.SyncedResourceVersion != "" {
		return false
	}

	// If the spec changed (generation mismatch), always refresh
	if status.SyncedResourceVersion != fmt.Sprint(currentGeneration) {
		return true
	}

	// If we've never refreshed, refresh now
	if status.RefreshTime.IsZero() {
		return true
	}

	// If the refresh interval has elapsed, refresh
	return time.Since(status.RefreshTime) >= spec.RefreshInterval
}

// isSecretValid checks if the existing K8s Secret is in the expected state.
//
// Real code: externalsecret_controller.go:1152-1174
func isSecretValidCheck(secret SecretState, expectedDataHash string) bool {
	// Secret must exist
	if !secret.Exists {
		return false
	}

	// Must have the "managed" label (proves we created it)
	if !secret.HasManagedLabel {
		return false
	}

	// Data hash must match (proves the data hasn't been tampered with)
	// If someone manually edited the secret, the hash won't match,
	// and we'll re-sync from the provider.
	if secret.DataHash != expectedDataHash {
		return false
	}

	return true
}

// =============================================================================
// Example: What Gets Skipped
// =============================================================================

func ExampleRefreshGating() {
	spec := ExternalSecretSpec{
		RefreshInterval: 1 * time.Hour,
		RefreshPolicy:   "Periodic",
		Generation:      3,
	}
	status := ExternalSecretStatus{
		SyncedResourceVersion: "3",            // matches current generation
		RefreshTime:           time.Now().Add(-30 * time.Minute), // 30 min ago
	}
	secret := SecretState{
		Exists:          true,
		HasManagedLabel: true,
		DataHash:        "abc123",
	}

	// Check 1: Should we refresh?
	refresh := shouldRefresh(spec, status, spec.Generation)
	fmt.Println("shouldRefresh:", refresh)
	// false — generation matches, refresh interval (1h) not elapsed (only 30min ago)

	// Check 2: Is the secret valid?
	valid := isSecretValidCheck(secret, "abc123")
	fmt.Println("isSecretValid:", valid)
	// true — exists, has label, hash matches

	// Result: SKIP the provider call entirely.
	// Reconcile() returns immediately with RequeueAfter: 30min (remaining time)
	if !refresh && valid {
		fmt.Println("SKIPPED: no provider call needed")
		fmt.Println("Requeue after:", spec.RefreshInterval-time.Since(status.RefreshTime))
	}

	// ==========================================================

	// Now simulate: someone manually edited the secret's data
	secret.DataHash = "xyz789" // hash no longer matches

	valid2 := isSecretValidCheck(secret, "abc123")
	fmt.Println("\nAfter manual edit:")
	fmt.Println("isSecretValid:", valid2)
	// false — hash mismatch! Must re-sync from provider.

	// Now simulate: spec.generation changed (user updated ExternalSecret)
	status.SyncedResourceVersion = "2" // doesn't match generation 3
	refresh2 := shouldRefresh(spec, status, spec.Generation)
	fmt.Println("\nAfter spec change:")
	fmt.Println("shouldRefresh:", refresh2)
	// true — generation mismatch, must refresh
}

// KEY INSIGHT:
// Without refresh gating, a cluster with 1000 ExternalSecrets would make
// 1000 provider API calls every time any event triggers reconciliation.
// With gating, most Reconcile() calls return in microseconds without
// touching the external provider at all.
//
// The gating uses three signals:
//   1. Generation — did the ExternalSecret spec change?
//   2. RefreshTime — has the refresh interval elapsed?
//   3. DataHash — was the target secret tampered with?
//
// Only when at least one condition is unmet does the reconciler call the provider.
