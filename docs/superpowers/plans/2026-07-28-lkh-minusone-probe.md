# LKH3 Minus-One Vehicle Probe Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When the stagnation solver's `-use-lkh` path fires on its first attempt, probe LKH3 for one fewer vehicle than the destroyed-route count before falling back to today's behavior, and make the probe visible in the solver log.

**Architecture:** Extract a pure decision function (`shouldProbeLKHMinusOne`) that decides whether/how to probe given the attempt number and destroyed-route count. Use it at the existing `*useLKH` call site in `main()` to try `invokeLKHSubSolver` with `maxVehicles-1` first; fall back to the existing `maxVehicles` call unchanged if the probe returns nil or doesn't fire.

**Tech Stack:** Go 1.24, existing `solver/main.go` + `solver/route_elimination_test.go` test patterns.

## Global Constraints

- Probe fires only when `attempt == 1` (bounds added LKH3 call cost to at most one extra solve per stagnation trigger, not per attempt) — see spec `docs/superpowers/specs/2026-07-28-lkh-minusone-probe-design.md`.
- Probe is skipped (not attempted) when `len(finalDestroyIDs) <= 1` — probing for 0 vehicles is meaningless.
- No changes to `invokeLKHSubSolver`'s signature, `buildLKHInstanceText`, or either `runLKHSolver` implementation (native/WASM) — `maxVehicles` is already a plain parameter.
- Downstream logic (`LKH:SUCCESS`/`LKH:FALLBACK` logging, `lkhHandled` flag, feasibility re-validation inside `invokeLKHSubSolver`, acceptance) is unchanged — it only ever consumes a single `lkhSol` value regardless of how it was obtained.
- New log categories: `LKH:PROBE`, `LKH:PROBE-SUCCESS`, `LKH:PROBE-FAILED`, added via the existing `sendProgressLog` mechanism.

---

### Task 1: Add `shouldProbeLKHMinusOne` and wire the probe into the stagnation solver

**Files:**
- Modify: `solver/main.go:321-334` (the `if *useLKH { ... }` block)
- Test: `solver/route_elimination_test.go`

**Interfaces:**
- Produces: `func shouldProbeLKHMinusOne(attempt int, destroyedRouteCount int) (probeVehicles int, ok bool)` — pure, no I/O, callable from tests without invoking the real LKH3 binary.

- [ ] **Step 1: Write the failing test**

Add to `solver/route_elimination_test.go`:

```go
func TestShouldProbeLKHMinusOne(t *testing.T) {
	cases := []struct {
		name                string
		attempt             int
		destroyedRouteCount int
		wantProbeVehicles   int
		wantOK              bool
	}{
		{"first attempt, multiple routes destroyed", 1, 3, 2, true},
		{"first attempt, exactly two routes destroyed", 1, 2, 1, true},
		{"first attempt, single route destroyed - probing 0 is meaningless", 1, 1, 0, false},
		{"second attempt never probes", 2, 3, 0, false},
		{"third attempt never probes", 3, 3, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotProbeVehicles, gotOK := shouldProbeLKHMinusOne(tc.attempt, tc.destroyedRouteCount)
			if gotOK != tc.wantOK {
				t.Fatalf("shouldProbeLKHMinusOne(%d, %d) ok = %v, want %v", tc.attempt, tc.destroyedRouteCount, gotOK, tc.wantOK)
			}
			if gotOK && gotProbeVehicles != tc.wantProbeVehicles {
				t.Fatalf("shouldProbeLKHMinusOne(%d, %d) probeVehicles = %d, want %d", tc.attempt, tc.destroyedRouteCount, gotProbeVehicles, tc.wantProbeVehicles)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./solver/... -run TestShouldProbeLKHMinusOne -v`
Expected: FAIL — `shouldProbeLKHMinusOne` is undefined.

- [ ] **Step 3: Implement `shouldProbeLKHMinusOne` and wire it in**

Add this function in `solver/main.go`, near `invokeLKHSubSolver` (around line 1549, just above it):

```go
// shouldProbeLKHMinusOne reports whether the LKH3 minus-one vehicle probe
// should fire for this stagnation attempt, and if so, the vehicle count to
// probe. Probing only fires on the first attempt of a stagnation trigger -
// this bounds the added LKH3 call cost to at most one extra solve per
// trigger, not per attempt (attempts 2-3 keep today's unprobed behavior).
// destroyedRouteCount <= 1 skips the probe entirely: asking LKH3 for 0
// vehicles is meaningless.
func shouldProbeLKHMinusOne(attempt int, destroyedRouteCount int) (probeVehicles int, ok bool) {
	if attempt != 1 || destroyedRouteCount <= 1 {
		return 0, false
	}
	return destroyedRouteCount - 1, true
}
```

Replace the existing `if *useLKH { ... }` block at `solver/main.go:321-334`:

```go
						if *useLKH {
							sendProgressLog(iter, bestSol, startTime, "LKH:TRIGGER", "Attempt %d: Invoking LKH3 on %d removed customers (vehicles cap = %d, no timeout)...", attempt, len(destroyedCustomers), len(finalDestroyIDs))
							lkhStart := time.Now()
							lkhSol := invokeLKHSubSolver(destroyedCustomers, depot, capacity, customerMap, len(finalDestroyIDs))
							lkhElapsed := time.Since(lkhStart)

							if lkhSol != nil {
								subSol = *lkhSol
								lkhHandled = true
								sendProgressLog(iter, bestSol, startTime, "LKH:SUCCESS", "LKH3 sub-solve used (size=%d customers): %d vehicles, %.2f distance, took %v.", len(destroyedCustomers), subSol.TotalVehicles, subSol.TotalDistance, lkhElapsed)
							} else {
								sendProgressLog(iter, bestSol, startTime, "LKH:FALLBACK", "LKH3 sub-solve failed or returned an infeasible result (size=%d customers, took %v); falling back to the pure-Go sub-solver.", len(destroyedCustomers), lkhElapsed)
							}
						}
```

with:

```go
						if *useLKH {
							lkhStart := time.Now()
							var lkhSol *Solution

							if probeVehicles, probeOK := shouldProbeLKHMinusOne(attempt, len(finalDestroyIDs)); probeOK {
								sendProgressLog(iter, bestSol, startTime, "LKH:PROBE", "Attempt %d: probing whether %d vehicles suffice for %d removed customers (down from %d)...", attempt, probeVehicles, len(destroyedCustomers), len(finalDestroyIDs))
								lkhSol = invokeLKHSubSolver(destroyedCustomers, depot, capacity, customerMap, probeVehicles)
								if lkhSol != nil {
									sendProgressLog(iter, bestSol, startTime, "LKH:PROBE-SUCCESS", "Probe succeeded: %d vehicles sufficient (reduced from %d) - skipping the %d-vehicle fallback.", probeVehicles, len(finalDestroyIDs), len(finalDestroyIDs))
								} else {
									sendProgressLog(iter, bestSol, startTime, "LKH:PROBE-FAILED", "Probe failed: %d vehicles not sufficient - falling back to %d.", probeVehicles, len(finalDestroyIDs))
								}
							}

							if lkhSol == nil {
								sendProgressLog(iter, bestSol, startTime, "LKH:TRIGGER", "Attempt %d: Invoking LKH3 on %d removed customers (vehicles cap = %d, no timeout)...", attempt, len(destroyedCustomers), len(finalDestroyIDs))
								lkhSol = invokeLKHSubSolver(destroyedCustomers, depot, capacity, customerMap, len(finalDestroyIDs))
							}
							lkhElapsed := time.Since(lkhStart)

							if lkhSol != nil {
								subSol = *lkhSol
								lkhHandled = true
								sendProgressLog(iter, bestSol, startTime, "LKH:SUCCESS", "LKH3 sub-solve used (size=%d customers): %d vehicles, %.2f distance, took %v.", len(destroyedCustomers), subSol.TotalVehicles, subSol.TotalDistance, lkhElapsed)
							} else {
								sendProgressLog(iter, bestSol, startTime, "LKH:FALLBACK", "LKH3 sub-solve failed or returned an infeasible result (size=%d customers, took %v); falling back to the pure-Go sub-solver.", len(destroyedCustomers), lkhElapsed)
							}
						}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./solver/... -run TestShouldProbeLKHMinusOne -v`
Expected: PASS (all 5 subtests).

- [ ] **Step 5: Run the full solver test suite to confirm no regressions**

Run: `go test ./solver/...`
Expected: PASS (all existing tests plus the new one — 17 total, up from 16).

- [ ] **Step 6: Rebuild the native CLI binary**

Run: `./build_solver.sh`
Expected: builds cleanly, produces `solver_bin`.

- [ ] **Step 7: Commit**

```bash
git add solver/main.go solver/route_elimination_test.go solver_bin
git commit -m "Add LKH3 minus-one vehicle probe on the first stagnation attempt"
```

## After Task 1

- [ ] **Manual validation**: Re-run R204 with `-use-lkh` enabled (native `solver_bin` or the browser after a WASM rebuild) and check the log for `LKH:PROBE*` entries during stagnation. Confirm behavior otherwise matches the spec's validation plan.
- [ ] **Rebuild browser WASM artifacts** once validated: `./build_lkh_wasm.sh && ./build_solver_wasm.sh`, then commit `public/wasm/*`.
- [ ] Use superpowers:finishing-a-development-branch once both this and the earlier route-elimination-operator work are ready to integrate.
