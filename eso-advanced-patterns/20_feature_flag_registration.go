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
	"strings"
	"time"
)

// =============================================================================
// Anti-Pattern: Centralized Flag Definition
// =============================================================================
//
// Every time a provider needs a new flag, you modify the main package.
// This creates merge conflicts, tight coupling, and a file that grows endlessly.
//
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
	Name       string
	Flags      *FlagSet
	Initialize func() // called after flag parsing, optional
}

// FeatureRegistry collects features from all subsystems.
// In real code this is a package-level var in runtime/feature/feature.go.
type FeatureRegistry struct {
	features []Feature
}

func NewFeatureRegistry() *FeatureRegistry {
	return &FeatureRegistry{}
}

// Register adds a feature's flags to the registry.
// Called from init() functions in each subsystem package.
func (r *FeatureRegistry) Register(f Feature) {
	r.features = append(r.features, f)
}

// =============================================================================
// Subsystem 1: State Manager (GC flags)
// =============================================================================
//
// In real code, this lives in runtime/statemanager/statemanager.go.
// The subsystem defines its own config and registers its own flags.
// Nobody outside this file needs to know these flags exist.

type StateManagerConfig struct {
	GCGracePeriod   time.Duration
	GCCheckInterval time.Duration
}

// RegisterStateManagerFlags is called from init() in the statemanager package.
// It registers GC-related flags and an Initialize function for validation.
func RegisterStateManagerFlags(registry *FeatureRegistry) *StateManagerConfig {
	cfg := &StateManagerConfig{}
	fs := NewFlagSet("statemanager")

	// Define flags — these will appear in --help automatically
	fs.DurationVar(&cfg.GCGracePeriod, "generator-gc-grace-period", 2*time.Minute,
		"grace period before garbage collecting generator state")
	fs.DurationVar(&cfg.GCCheckInterval, "generator-gc-check-interval", 30*time.Second,
		"how often to check for expired generator state")

	registry.Register(Feature{
		Name:  "statemanager",
		Flags: fs,
		Initialize: func() {
			// Called AFTER flags are parsed — do validation here.
			// This keeps validation logic next to the flag definition,
			// not scattered in main.go.
			if cfg.GCGracePeriod < 30*time.Second {
				fmt.Println("WARNING: gc-grace-period too low, clamping to 30s")
				cfg.GCGracePeriod = 30 * time.Second
			}
			fmt.Printf("  [statemanager] initialized: gc-grace=%v, gc-interval=%v\n",
				cfg.GCGracePeriod, cfg.GCCheckInterval)
		},
	})

	return cfg
}

// =============================================================================
// Subsystem 2: AWS Provider (retry flags)
// =============================================================================
//
// Hypothetical: providers/aws/flags.go
// Adding a new flag here requires ZERO changes to main.go.

type AWSProviderConfig struct {
	MaxRetries int
	Region     string
}

func RegisterAWSProviderFlags(registry *FeatureRegistry) *AWSProviderConfig {
	cfg := &AWSProviderConfig{}
	fs := NewFlagSet("aws")

	fs.IntVar(&cfg.MaxRetries, "aws-max-retries", 3,
		"max retries for AWS API calls")
	fs.StringVar(&cfg.Region, "aws-region", "us-east-1",
		"default AWS region")

	registry.Register(Feature{
		Name:  "aws",
		Flags: fs,
		Initialize: func() {
			if cfg.MaxRetries < 0 {
				cfg.MaxRetries = 3
			}
			fmt.Printf("  [aws] initialized: region=%s, max-retries=%d\n",
				cfg.Region, cfg.MaxRetries)
		},
	})

	return cfg
}

// =============================================================================
// main.go: Collect and Apply All Flags
// =============================================================================
//
// The main package doesn't know which subsystems exist. It just iterates
// over whatever was registered.
//
// Real code flow:
//   1. Go init() functions run → each subsystem calls Register()
//   2. main() creates root cobra command
//   3. Loop over Features() → rootCmd.Flags().AddFlagSet(f.Flags)
//   4. rootCmd.Execute() parses flags
//   5. Loop over Features() → call Initialize()

func demonstrateFeatureFlags() {
	registry := NewFeatureRegistry()

	// --- Phase 1: Registration (happens in init() functions) ---
	// In real code, these are called by Go's init() mechanism.
	// Each subsystem registers independently — no coordination needed.
	fmt.Println("Phase 1: Registration")
	smCfg := RegisterStateManagerFlags(registry)
	awsCfg := RegisterAWSProviderFlags(registry)

	// --- Phase 2: Collect flags for CLI (happens in main()) ---
	// In real code: rootCmd.Flags().AddFlagSet(f.Flags.pflagSet)
	fmt.Println("\nPhase 2: Collect flags for --help")
	for _, f := range registry.features {
		fmt.Printf("  registered flags from [%s]: %s\n", f.Name, f.Flags.FlagNames())
	}

	// --- Phase 3: Parse flags (cobra/pflag handles this) ---
	// Simulate user passing: --generator-gc-grace-period=5m --aws-region=eu-west-1
	fmt.Println("\nPhase 3: Parse CLI args (simulated)")
	simulateFlagParsing(registry, map[string]string{
		"generator-gc-grace-period": "5m",
		"aws-region":                "eu-west-1",
	})

	// --- Phase 4: Initialize (post-parse validation and setup) ---
	fmt.Println("\nPhase 4: Initialize subsystems")
	for _, f := range registry.features {
		if f.Initialize != nil {
			f.Initialize()
		}
	}

	// --- Result: each subsystem's config is ready to use ---
	fmt.Printf("\nResult: statemanager gc-grace=%v\n", smCfg.GCGracePeriod)
	fmt.Printf("Result: aws region=%s, retries=%d\n", awsCfg.Region, awsCfg.MaxRetries)
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

// --- FlagSet: simplified version of pflag.FlagSet ---

type FlagEntry struct {
	Name         string
	DefaultValue string
	Usage        string
	pointer      interface{} // pointer to the variable to set
}

type FlagSet struct {
	name    string
	entries []FlagEntry
}

func NewFlagSet(name string) *FlagSet {
	return &FlagSet{name: name}
}

func (fs *FlagSet) DurationVar(p *time.Duration, name string, value time.Duration, usage string) {
	*p = value // set default
	fs.entries = append(fs.entries, FlagEntry{
		Name: name, DefaultValue: value.String(), Usage: usage, pointer: p,
	})
}

func (fs *FlagSet) IntVar(p *int, name string, value int, usage string) {
	*p = value // set default
	fs.entries = append(fs.entries, FlagEntry{
		Name: name, DefaultValue: fmt.Sprintf("%d", value), Usage: usage, pointer: p,
	})
}

func (fs *FlagSet) StringVar(p *string, name string, value string, usage string) {
	*p = value // set default
	fs.entries = append(fs.entries, FlagEntry{
		Name: name, DefaultValue: value, Usage: usage, pointer: p,
	})
}

func (fs *FlagSet) FlagNames() string {
	names := make([]string, len(fs.entries))
	for i, e := range fs.entries {
		names[i] = "--" + e.Name
	}
	return strings.Join(names, ", ")
}

// Set simulates parsing a flag value from CLI args.
func (fs *FlagSet) Set(name, value string) bool {
	for _, e := range fs.entries {
		if e.Name != name {
			continue
		}
		switch p := e.pointer.(type) {
		case *time.Duration:
			if d, err := time.ParseDuration(value); err == nil {
				*p = d
				return true
			}
		case *int:
			var v int
			if _, err := fmt.Sscanf(value, "%d", &v); err == nil {
				*p = v
				return true
			}
		case *string:
			*p = value
			return true
		}
	}
	return false
}

// simulateFlagParsing simulates cobra/pflag parsing CLI arguments.
func simulateFlagParsing(registry *FeatureRegistry, args map[string]string) {
	for flagName, flagValue := range args {
		for _, f := range registry.features {
			if f.Flags.Set(flagName, flagValue) {
				fmt.Printf("  --%s=%s → applied to [%s]\n", flagName, flagValue, f.Name)
			}
		}
	}
}

func init() {
	_ = demonstrateFeatureFlags
}
