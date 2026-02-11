// Pattern 20: Decoupled Feature Flag Registration
//
// Problem: A Kubernetes operator has many providers and subsystems, each with
// their own CLI flags (e.g., GC grace period, cache TTL, retry settings).
// Centralizing all flags in main.go creates a god file that imports every
// subsystem and changes every time a provider adds a flag.
//
// Solution: Let each subsystem register its own flags via a global registry.
// At startup, the main package collects all registered flags, adds them to
// the root command, and optionally calls an Initialize() function after
// flag parsing.
//
// WHY THIS OVER OTHER APPROACHES:
//   - Centralized flags: every new flag requires touching main.go
//   - Config struct: requires all subsystems to agree on a shared type
//   - Registry pattern: each subsystem is self-contained, adding a flag is
//     a single-file change with no coordination needed
//
// REAL CODE REFERENCE:
//   runtime/feature/feature.go:24-42
//   runtime/statemanager/statemanager.go (init func registers GC flags)

package eso_advanced_patterns

import (
	"fmt"
	"time"
)

// =============================================================================
// Anti-Pattern: Centralized Flag Definition
// =============================================================================
//
// Every time a provider needs a new flag, you modify the main package.
// This creates merge conflicts, tight coupling, and a file that grows endlessly.

// In main.go:
//   var gcGracePeriod time.Duration
//   var awsRegion string
//   var gcpProjectID string
//   var azureTenantID string
//   var vaultAddr string
//   // ... 50 more flags from 20 providers ...
//
//   func init() {
//       flag.DurationVar(&gcGracePeriod, "gc-grace-period", 2*time.Minute, "...")
//       flag.StringVar(&awsRegion, "aws-region", "us-east-1", "...")
//       // ... 50 more flag registrations ...
//   }

// =============================================================================
// Correct Pattern: Decoupled Feature Registration
// =============================================================================
//
// Real code: runtime/feature/feature.go:24-42

// Feature contains CLI flags that a subsystem exposes, plus an optional
// initialization function called after flags are parsed.
type Feature struct {
	Flags      *FlagSet
	Initialize func() // called after flag parsing, optional
}

// Global registry — populated by init() functions across packages.
var features = make([]Feature, 0)

// Register adds a feature's flags to the global registry.
// Called from init() functions in each subsystem.
func Register(f Feature) {
	features = append(features, f)
}

// Features returns all registered features.
func Features() []Feature {
	return features
}

// =============================================================================
// How Subsystems Register Flags
// =============================================================================
//
// Each subsystem defines its flags in its own package and registers them
// via init(). No changes to main.go required.

// --- In runtime/statemanager/statemanager.go ---

var gcGracePeriod time.Duration

func init() {
	fs := NewFlagSet("gc")
	fs.DurationVar(&gcGracePeriod, "generator-gc-grace-period", 2*time.Minute,
		"grace period before garbage collecting generator state")
	Register(Feature{Flags: fs})
}

// --- In providers/aws/flags.go (hypothetical) ---

var awsMaxRetries int

// func init() {
//     fs := NewFlagSet("aws")
//     fs.IntVar(&awsMaxRetries, "aws-max-retries", 3, "max retries for AWS API calls")
//     Register(Feature{
//         Flags: fs,
//         Initialize: func() {
//             // Called after flags are parsed — can do validation or setup
//             if awsMaxRetries < 0 {
//                 awsMaxRetries = 3
//             }
//         },
//     })
// }

// =============================================================================
// How main.go Collects and Applies Flags
// =============================================================================
//
// The main package iterates over all registered features, adds their flags
// to the root command, and calls Initialize after parsing.

func setupFlags() {
	// Collect all flags from registered features
	for _, f := range Features() {
		// In real code: rootCmd.Flags().AddFlagSet(f.Flags.pflagSet)
		fmt.Printf("Adding flags from: %s\n", f.Flags.name)
	}

	// Parse flags (handled by cobra/pflag in real code)

	// Call Initialize functions
	for _, f := range Features() {
		if f.Initialize != nil {
			f.Initialize()
		}
	}
}

// =============================================================================
// Benefits
// =============================================================================
//
// 1. ZERO COORDINATION: Adding a flag to a provider is a single-file change.
//    No main.go modification, no merge conflicts.
//
// 2. SELF-DOCUMENTING: Each subsystem's flags live next to the code that uses
//    them. Reading statemanager.go tells you everything about GC configuration.
//
// 3. LAZY LOADING: If a provider isn't compiled in (build tags), its flags
//    aren't registered. No dead flags in --help output.
//
// 4. INITIALIZATION HOOK: The optional Initialize() function lets subsystems
//    do post-parse validation or setup without polluting main.go.
//
// 5. TESTABLE: Each subsystem can test its own flag defaults and validation
//    without spinning up the entire operator.

// --- Simplified FlagSet for illustration ---

type FlagSet struct {
	name  string
	flags map[string]interface{}
}

func NewFlagSet(name string) *FlagSet {
	return &FlagSet{name: name, flags: make(map[string]interface{})}
}

func (fs *FlagSet) DurationVar(p *time.Duration, name string, value time.Duration, usage string) {
	*p = value
	fs.flags[name] = p
}

func (fs *FlagSet) IntVar(p *int, name string, value int, usage string) {
	*p = value
	fs.flags[name] = p
}

func init() {
	_ = setupFlags
}
