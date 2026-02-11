// Pattern 12: Smart Condition Management with Metrics Integration
//
// Problem: Kubernetes conditions (.status.conditions) are the standard way
// to report resource health. But naively overwriting conditions on every
// reconcile causes two issues:
//   1. LastTransitionTime updates constantly, making it useless for "how long
//      has this been in this state?"
//   2. Metrics don't reflect the current state, so dashboards and alerts are blind
//
// Solution: Only update LastTransitionTime when the status ACTUALLY changes,
// and atomically update Prometheus metrics alongside condition changes.
// This gives you accurate "time in state" and real-time observability.
//
// REAL CODE REFERENCE:
//   pkg/controllers/externalsecret/util.go:51-74

package eso_advanced_patterns

import (
	"fmt"
	"time"
)

// =============================================================================
// Anti-Pattern: Naive Condition Update
// =============================================================================
//
// This updates LastTransitionTime every time, even if the status hasn't changed.
// Result: LastTransitionTime is always "a few seconds ago", which makes it
// impossible to answer "how long has this ExternalSecret been failing?"

func setConditionBad(status *ResourceStatus, newCond Condition) {
	newCond.LastTransitionTime = time.Now() // WRONG: always updates
	// Replace old condition of same type
	filtered := make([]Condition, 0)
	for _, c := range status.Conditions {
		if c.Type != newCond.Type {
			filtered = append(filtered, c)
		}
	}
	status.Conditions = append(filtered, newCond)
}

// =============================================================================
// Correct Pattern: Preserve LastTransitionTime + Metrics Integration
// =============================================================================
//
// Real code: pkg/controllers/externalsecret/util.go:51-74
//
// Three key behaviors:
//   1. If status AND reason AND message are all unchanged → skip entirely (no-op)
//   2. If status is unchanged but reason/message changed → keep LastTransitionTime
//   3. If status changed → update LastTransitionTime to now
//
// Additionally, every condition change updates Prometheus metrics:
//   - Set the OLD condition gauge to 0
//   - Set the NEW condition gauge to 1
//
// This gives you instant dashboards showing "how many ExternalSecrets are Ready?"

func SetCondition(status *ResourceStatus, newCond Condition, resourceName string) {
	currentCond := getCondition(status, newCond.Type)

	// Optimization: if nothing changed at all, just refresh the metric and return.
	// This avoids unnecessary status updates that would trigger re-reconciliation
	// in controllers watching this resource.
	if currentCond != nil &&
		currentCond.Status == newCond.Status &&
		currentCond.Reason == newCond.Reason &&
		currentCond.Message == newCond.Message {
		updateConditionMetric(resourceName, &newCond, 1.0)
		return
	}

	// KEY: Only update LastTransitionTime when the STATUS actually changes.
	// If status is the same but reason/message differ, keep the old transition time.
	// This preserves the answer to "since when has this been in this state?"
	if currentCond != nil && currentCond.Status == newCond.Status {
		newCond.LastTransitionTime = currentCond.LastTransitionTime
	} else {
		// Status actually changed (or new condition) → record transition time
		newCond.LastTransitionTime = time.Now()
	}

	// Replace old condition with new one
	status.Conditions = append(
		filterOutCondition(status.Conditions, newCond.Type),
		newCond,
	)

	// Update metrics: clear old condition, set new one
	if currentCond != nil {
		updateConditionMetric(resourceName, currentCond, 0.0) // old state → 0
	}
	updateConditionMetric(resourceName, &newCond, 1.0) // new state → 1
}

// =============================================================================
// Why This Matters in Practice
// =============================================================================
//
// Consider an ExternalSecret that fails to sync:
//
// Without this pattern (naive):
//   conditions:
//   - type: Ready
//     status: "False"
//     lastTransitionTime: "2024-01-15T10:30:05Z"  ← updates every 30s
//
// With this pattern (correct):
//   conditions:
//   - type: Ready
//     status: "False"
//     lastTransitionTime: "2024-01-14T03:15:00Z"  ← when it FIRST failed
//
// The correct version tells you: "This has been failing for 31 hours."
// The naive version tells you: "This has been failing for 5 seconds." (useless)
//
// Combined with Prometheus metrics, you get alerts like:
//   alert: ExternalSecretSyncFailure
//   expr: external_secret_condition{type="Ready", status="False"} == 1
//   for: 5m

// --- Helper functions ---

func getCondition(status *ResourceStatus, condType string) *Condition {
	for i := range status.Conditions {
		if status.Conditions[i].Type == condType {
			return &status.Conditions[i]
		}
	}
	return nil
}

func filterOutCondition(conditions []Condition, condType string) []Condition {
	filtered := make([]Condition, 0, len(conditions))
	for _, c := range conditions {
		if c.Type != condType {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

func updateConditionMetric(resourceName string, cond *Condition, value float64) {
	// In ESO, this calls:
	//   esmetrics.UpdateExternalSecretCondition(es, &condition, value)
	// which sets a Prometheus gauge with labels:
	//   external_secret_condition{name="my-secret", type="Ready", status="True"} = 1
	fmt.Printf("metric: %s condition=%s status=%s value=%.0f\n",
		resourceName, cond.Type, cond.Status, value)
}

// --- Types ---

type Condition struct {
	Type               string
	Status             string // "True", "False", "Unknown"
	Reason             string
	Message            string
	LastTransitionTime time.Time
}

type ResourceStatus struct {
	Conditions []Condition
}

func init() {
	_ = setConditionBad
}
