# Kubernetes Controller Design Patterns Guide

Production-grade design patterns learned from the [External Secrets Operator (ESO)](https://github.com/external-secrets/external-secrets) codebase.

21 patterns organized from foundational concepts to advanced production optimizations, each with:
- Problem description and anti-pattern example
- Correct pattern with detailed explanation
- Real ESO code references

## Foundation Patterns

| # | Pattern | Key Idea |
|---|---------|----------|
| 01 | [Level-Triggered Reconciliation](01_level_triggered_reconciliation.go) | Compare desired vs current state, fix the diff. Idempotent and self-healing. |
| 02 | [Provider Registry via init()](02_provider_registry.go) | Plugin pattern: 30+ providers register themselves via `init()` + build tags. |
| 03 | [Finalizer Pattern](03_finalizer_pattern.go) | Guarantee external resource cleanup before Kubernetes deletes an object. |
| 04 | [Workqueue Deduplication](04_workqueue_deduplication.go) | Dedup, rate limit, and delayed requeue — same key enqueued 10x = 1 reconcile. |
| 05 | [Interface-Based Abstraction](05_interface_abstraction.go) | Strategy pattern: reconciler talks to an interface, not provider-specific code. |
| 06 | [Ownership & Garbage Collection](06_ownership_gc.go) | Two-layer ownership: OwnerReference (built-in GC) + Labels (orphan detection). |
| 07 | [Mutation Function](07_mutation_function.go) | Single function defines desired state, reused for both create and update. |
| 08 | [Refresh Gating](08_refresh_gating.go) | Skip external API calls when state is already in sync. |
| 09 | [Layered Cache Strategy](09_layered_cache.go) | Three configurable cache layers for large clusters (10,000+ secrets). |
| 10 | [Status Subresource](10_status_subresource.go) | Deferred status update ensures consistent observability on success, error, or panic. |

## Advanced Patterns

| # | Pattern | Key Idea |
|---|---------|----------|
| 11 | [Multi-Error Validation](eso-advanced-patterns/11_multi_error_validation.go) | Accumulate ALL validation errors with `errors.Join()`, return together. |
| 12 | [Condition Management](eso-advanced-patterns/12_condition_management.go) | Only update `LastTransitionTime` when status actually changes + Prometheus metrics. |
| 13 | [Commit/Rollback State](eso-advanced-patterns/13_commit_rollback_state.go) | Database transaction semantics: queue operations with paired commit/rollback. |
| 14 | [TryLock Concurrent Access](eso-advanced-patterns/14_trylock_concurrent_access.go) | Non-blocking `TryLock()` + workqueue retry, with exponential backoff. |
| 15 | [Custom Rate Limiter](eso-advanced-patterns/15_custom_rate_limiter.go) | Combine per-item exponential backoff + global token bucket, take the max. |
| 16 | [Specialized Cache Client](eso-advanced-patterns/16_specialized_cache_client.go) | Label-filtered cache: only watch managed secrets, 98% memory reduction. |
| 17 | [Sentinel Errors](eso-advanced-patterns/17_sentinel_errors.go) | Well-known error values for control flow, checked with `errors.Is()`. |
| 18 | [Dynamic Informer Refcount](eso-advanced-patterns/18_dynamic_informer_refcount.go) | On-demand informers with reference counting; auto-cleanup when unused. |
| 19 | [Resource Version Hash](eso-advanced-patterns/19_resource_version_hash.go) | Composite version = generation + hash(labels+annotations) for cache invalidation. |
| 20 | [Feature Flag Registration](eso-advanced-patterns/20_feature_flag_registration.go) | Global registry: each subsystem registers its own flags, no god file. |
| 21 | [FQDN Hash Truncation](eso-advanced-patterns/21_fqdn_hash_truncation.go) | Human-readable names when short, cryptographic hash fallback at 63-char limit. |

## Suggested Learning Path

**Start with foundations (1-10):**
1. Reconciliation fundamentals — Pattern 1
2. Plugin architecture — Patterns 2, 5
3. Kubernetes lifecycle — Patterns 3, 6
4. Workqueue & performance — Patterns 4, 8, 9
5. State management — Patterns 7, 10

**Then advanced topics (11-21):**
1. Error handling — Patterns 11, 17
2. State & conditions — Patterns 12, 13, 19
3. Concurrency & performance — Patterns 14, 15, 16
4. Dynamic resources — Pattern 18
5. Operational concerns — Patterns 20, 21

## Project Structure

```
design-patterns-guide/
├── 01-10: Foundation patterns (main directory)
├── eso-advanced-patterns/
│   └── 11-21: Advanced patterns
├── go.mod
└── README.md
```

Each `.go` file is self-contained with:
- Comment header explaining the problem and solution
- Anti-pattern (what NOT to do)
- Correct pattern with inline annotations
- Real ESO code file references
