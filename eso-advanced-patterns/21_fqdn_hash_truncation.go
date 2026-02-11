// Pattern 21: FQDN Name Truncation with Hash Fallback
//
// Problem: Kubernetes resource names, labels, and field owner strings are
// limited to 63 characters (DNS label constraint from RFC 1123). When you
// derive names from user input (e.g., ExternalSecret name → field owner),
// long names silently get rejected by the API server. Naive truncation
// (cutting at 63 chars) causes collisions: "my-very-long-secret-name-a..."
// and "my-very-long-secret-name-b..." truncate to the same string.
//
// Solution: Use the readable name when it fits. When it exceeds the limit,
// fall back to a cryptographic hash that guarantees uniqueness. This gives
// you human-readable names for the common case (short names) and collision-
// free names for the edge case (long names).
//
// REAL CODE REFERENCE:
//   pkg/controllers/externalsecret/util.go:88-96

package eso_advanced_patterns

import (
	"crypto/sha256"
	"fmt"
)

// =============================================================================
// Anti-Pattern 1: Naive Truncation
// =============================================================================
//
// Cutting at max length causes collisions.
//   "my-app-database-credentials-production-us-east-1"  → "my-app-database-cre..."
//   "my-app-database-credentials-production-us-west-2"  → "my-app-database-cre..."
// Both truncate to the same string → one silently overwrites the other.

func fqdnTruncateBad(name string) string {
	fqdn := fmt.Sprintf("externalsecrets.%s", name)
	if len(fqdn) > 63 {
		return fqdn[:63] // COLLISION RISK
	}
	return fqdn
}

// =============================================================================
// Anti-Pattern 2: Always Use Hash
// =============================================================================
//
// Using a hash for everything is safe but unreadable.
//   "my-secret" → "externalsecrets.a3f2b1c4d5e6f7..."
// When debugging, you can't tell which ExternalSecret owns which field.

func fqdnHashBad(name string) string {
	hash := sha256.Sum256([]byte(name))
	return fmt.Sprintf("externalsecrets.%x", hash[:14]) // always unreadable
}

// =============================================================================
// Correct Pattern: Readable Name with Hash Fallback
// =============================================================================
//
// Real code: pkg/controllers/externalsecret/util.go:88-96
//
//   const fieldOwnerTemplate    = "externalsecrets.%s"
//   const fieldOwnerTemplateSha = "externalsecrets.%x"
//
//   func fqdnFor(name string) string {
//       fqdn := fmt.Sprintf(fieldOwnerTemplate, name)
//       if len(fqdn) > 63 {
//           fqdn = fmt.Sprintf(fieldOwnerTemplateSha, sha3.Sum224([]byte(name)))
//       }
//       return fqdn
//   }

const (
	fieldOwnerTemplate    = "externalsecrets.%s"
	fieldOwnerTemplateSha = "externalsecrets.%x"
	maxDNSLabelLength     = 63
)

func fqdnFor(name string) string {
	fqdn := fmt.Sprintf(fieldOwnerTemplate, name)

	// Common case: name is short enough → use readable format
	// e.g., "my-secret" → "externalsecrets.my-secret" (25 chars, fits)
	if len(fqdn) <= maxDNSLabelLength {
		return fqdn
	}

	// Edge case: name too long → use hash for uniqueness
	// SHA-224 produces 28 bytes = 56 hex chars. With "externalsecrets." prefix
	// (16 chars), total is 72 chars. The real code uses SHA3-224 which is
	// slightly more collision-resistant, but any crypto hash works.
	hash := sha256.Sum256([]byte(name))
	fqdn = fmt.Sprintf(fieldOwnerTemplateSha, hash[:14]) // 28 hex chars
	return fqdn
}

func demonstrateFQDN() {
	// Short name → readable
	short := fqdnFor("my-secret")
	fmt.Printf("Short: %s (len=%d)\n", short, len(short))
	// → "externalsecrets.my-secret" (25 chars)

	// Long name → hashed but unique
	long1 := fqdnFor("my-very-long-application-secret-name-production-us-east-1-database")
	fmt.Printf("Long1: %s (len=%d)\n", long1, len(long1))
	// → "externalsecrets.a3f2b1c4d5e6f7..." (unique hash)

	// Different long name → different hash, no collision
	long2 := fqdnFor("my-very-long-application-secret-name-production-us-west-2-database")
	fmt.Printf("Long2: %s (len=%d)\n", long2, len(long2))
	// → "externalsecrets.b7e9d2f1a8c3e5..." (different hash)

	// Verify: long1 != long2 (no collision)
	fmt.Printf("Collision: %v\n", long1 == long2) // false
}

// =============================================================================
// Where This Pattern Applies
// =============================================================================
//
// This pattern is useful anywhere you derive Kubernetes names from user input:
//
//   - Field manager names (server-side apply): max 63 chars
//   - Label values: max 63 chars
//   - Annotation keys: max 63 chars (the name part)
//   - Resource names: max 253 chars (but subdomain labels still max 63)
//   - Container names, port names: max 63 chars
//
// The key insight is: MOST names are short (under 30 chars). Making the common
// case readable and only falling back to hashes for the rare long-name case
// gives you the best of both worlds: debuggability AND correctness.

// =============================================================================
// Backwards Compatibility Note
// =============================================================================
//
// From the real code comment: "Done this way for backwards compatibility thus
// avoiding breaking changes." This means the hash fallback was added later.
// Existing short-named resources keep their readable field owner strings.
// Only new long-named resources get hashed strings. This is a common pattern
// when fixing naming constraints: don't change existing names, only fix
// the generation logic for new ones.

func init() {
	_ = fqdnTruncateBad
	_ = fqdnHashBad
	_ = demonstrateFQDN
}
