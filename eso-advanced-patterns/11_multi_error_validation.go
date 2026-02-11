// Pattern 11: Multi-Error Accumulation in Webhook Validation
//
// Problem: Admission webhooks need to validate multiple fields simultaneously.
// If you return on the first error, the user has to fix one error at a time,
// submit, discover the next error, fix it, submit again... This is a terrible UX.
//
// Solution: Accumulate ALL validation errors using Go 1.20's errors.Join(),
// then return them all at once. The user sees every problem in a single response
// and can fix them all before resubmitting.
//
// WHY errors.Join() OVER OTHER APPROACHES:
//   - Custom []error slice: works but loses errors.Is()/errors.As() support
//   - fmt.Errorf wrapping: only wraps one error, can't accumulate N errors
//   - go-multierror: third-party dependency for something now built into stdlib
//   - errors.Join(): zero-alloc when nil, supports Unwrap() []error, stdlib
//
// KEY INSIGHT: errors.Join(nil, err) == err, and errors.Join(nil, nil) == nil.
// This means you can start with `var errs error` (nil) and keep joining — no
// special initialization or "did we get any errors?" check needed.
//
// REAL CODE REFERENCE:
//   apis/externalsecrets/v1/externalsecret_validator.go:49-80

package eso_advanced_patterns

import (
	"errors"
	"fmt"
	"strings"
)

// =============================================================================
// Anti-Pattern: Return on First Error
// =============================================================================
//
// This forces users to play whack-a-mole with validation errors:
// "Fix field A" → resubmit → "Fix field B" → resubmit → "Fix field C"
// In a CI/CD pipeline, each round-trip can cost minutes.

func validateBad(spec ExternalSecretSpec) error {
	if spec.SecretStoreRef.Name == "" {
		return fmt.Errorf("secretStoreRef.name is required")
	}
	if len(spec.Data) == 0 && len(spec.DataFrom) == 0 {
		return fmt.Errorf("either data or dataFrom should be specified")
	}
	if spec.Target.DeletionPolicy == "Delete" && spec.Target.CreationPolicy == "Merge" {
		return fmt.Errorf("deletionPolicy=Delete must not be used with creationPolicy=Merge")
	}
	return nil
}

// =============================================================================
// Correct Pattern: Accumulate All Errors with errors.Join
// =============================================================================
//
// This is exactly how ESO's ExternalSecretValidator works.
// The user sees ALL problems in one shot and can fix everything at once.
//
// In the real code (externalsecret_validator.go:49-80), it validates:
//   - Deletion/creation policy combinations
//   - Data/dataFrom presence
//   - Extract/find/generator configuration per dataFrom entry
//   - SourceRef validation
//   - Duplicate key detection

func validateGood(spec ExternalSecretSpec) error {
	var errs error

	// Validate policy combinations
	// Real code: externalsecret_validator.go:107-119
	if err := validatePolicies(spec); err != nil {
		errs = errors.Join(errs, err)
	}

	// Validate data presence
	if len(spec.Data) == 0 && len(spec.DataFrom) == 0 {
		errs = errors.Join(errs, fmt.Errorf("either data or dataFrom should be specified"))
	}

	// Validate each dataFrom entry — accumulating errors across ALL entries
	// Real code: externalsecret_validator.go:58-73
	for i, ref := range spec.DataFrom {
		if ref.Extract == nil && ref.Find == nil && ref.Generator == nil {
			errs = errors.Join(errs, fmt.Errorf("dataFrom[%d]: at least one of extract, find, or generator must be specified", i))
		}
	}

	// Validate duplicate keys
	errs = validateDuplicateKeys(spec, errs)

	// errors.Join returns nil if all inputs are nil — no special check needed
	return errs
}

func validatePolicies(spec ExternalSecretSpec) error {
	var errs error

	if spec.Target.DeletionPolicy == "Delete" && spec.Target.CreationPolicy == "Merge" {
		errs = errors.Join(errs, fmt.Errorf(
			"deletionPolicy=Delete must not be used when the controller doesn't own the secret. "+
				"Please set creationPolicy=Owner"))
	}

	if spec.Target.DeletionPolicy == "Merge" && spec.Target.CreationPolicy == "None" {
		errs = errors.Join(errs, fmt.Errorf(
			"deletionPolicy=Merge must not be used with creationPolicy=None. "+
				"There is no Secret to merge with"))
	}

	return errs
}

func validateDuplicateKeys(spec ExternalSecretSpec, errs error) error {
	seen := make(map[string]bool)
	for _, d := range spec.Data {
		key := d.SecretKey
		if key == "" {
			key = d.RemoteRef.Key // default to remote key
		}
		if seen[key] {
			errs = errors.Join(errs, fmt.Errorf("duplicate secretKey: %s", key))
		}
		seen[key] = true
	}
	return errs
}

// =============================================================================
// Advanced Usage: errors.Is() works through Join'd errors
// =============================================================================
//
// Because errors.Join returns an error that implements Unwrap() []error,
// both errors.Is() and errors.As() traverse ALL joined errors.

var ErrPolicyConflict = errors.New("policy conflict")

func demonstrateErrorsIs() {
	err1 := fmt.Errorf("bad policy: %w", ErrPolicyConflict)
	err2 := fmt.Errorf("missing data field")

	combined := errors.Join(err1, err2)

	// This returns true — errors.Is traverses into the joined errors
	_ = errors.Is(combined, ErrPolicyConflict) // true

	// You can also use errors.As to extract typed errors from the joined set
	fmt.Println(combined)
}

// --- Helper types ---

type ExternalSecretSpec struct {
	SecretStoreRef SecretStoreRef
	Data           []DataEntry
	DataFrom       []DataFromEntry
	Target         TargetSpec
}

type SecretStoreRef struct{ Name string }
type DataEntry struct {
	SecretKey string
	RemoteRef RemoteRef
}
type RemoteRef struct{ Key string }
type DataFromEntry struct {
	Extract   *string
	Find      *string
	Generator *string
}
type TargetSpec struct {
	DeletionPolicy string
	CreationPolicy string
}

// KEY TAKEAWAY:
// errors.Join() is the Go 1.20+ way to accumulate multiple validation errors.
// It starts from nil, supports Unwrap()/Is()/As(), and produces clean output
// like "error1\nerror2\nerror3". For webhook validators that check many fields,
// this is the cleanest approach — zero dependencies, stdlib only.

// Compared to the older approach in the main guide (which might use custom types),
// errors.Join is simpler and should be preferred for Go 1.20+ projects.

func init() {
	_ = validateBad
	_ = validateGood
	_ = demonstrateErrorsIs
	_ = strings.Contains // suppress import
}
