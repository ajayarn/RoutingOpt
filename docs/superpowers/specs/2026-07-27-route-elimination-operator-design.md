# Route-Elimination Operator — Design Spec

## Problem

`solver/main.go`'s main LNS loop (lines ~119-260) only has two destroy operators:
`destroyWorst` and `destroyRandom`, each removing a random `k` (5%-30% of
customers) scattered across arbitrary routes. Repair (`repairGreedy`,
line 927) always succeeds by opening a brand-new route whenever a customer
doesn't fit anywhere existing (line 967-972).

Consequence: the loop has essentially no reliable path to reduce vehicle
count. Doing so would require every customer removed from a route to
coincidentally reinsert into fewer routes than it started in, under random
shuffle order — vanishingly unlikely in practice. Observed symptom: final
solutions frequently have lower total distance than the published best-known
value but use one more vehicle, which is objectively worse under this
project's hierarchical objective (minimize vehicles first, then distance —
see root `CLAUDE.md`). The acceptance logic already handles this correctly
(`candidateSol.TotalVehicles < sol.TotalVehicles` triggers an unconditional
accept, line 145) — it's just never fed a candidate with fewer vehicles.

## Goal

Give the LNS loop a genuine, constructed (not lucky) path to produce
fewer-vehicle candidates whenever the underlying capacity/time-window slack
actually supports it.

## Design

### 1. Route selection: `selectWeakestRoutes`

```go
// selectWeakestRoutes ranks route indices ascending by "how easy this route
// is to eliminate" — fewest customers first (fewest things needing a new
// home), tie-broken by lowest total demand (most likely to fit into other
// routes' remaining capacity).
func selectWeakestRoutes(sol Solution, customers map[int]Customer) []int
```

Returns a full ranking (not just the single weakest) so the orchestration
step can retry against the next-weakest route without recomputing.

### 2. Destroy: `destroyRouteElimination`

```go
// destroyRouteElimination removes the entire route at routeIdx (not a
// random subset) and returns the remaining solution plus every customer ID
// that was evicted. Same (Solution, []int) shape as destroyWorst/destroyRandom.
func destroyRouteElimination(sol Solution, routeIdx int) (Solution, []int)
```

### 3. Repair: `repairGreedyNoNewRoute`

```go
// repairGreedyNoNewRoute attempts to reinsert every customer in `removed`
// into sol's existing routes only — it may never open a new route. Returns
// ok=false (no partial commit) if any customer cannot be placed feasibly.
//
// Insertion order is most-constrained-first, recomputed dynamically: before
// each insertion, count each not-yet-placed customer's number of feasible
// (route, position) slots given the CURRENT state (not a static pre-sort),
// and insert whichever customer has the fewest options next. This matters
// because inserting "easy" customers first can consume the capacity/time
// slack a "hard" customer needed — exactly why repairGreedy's random order
// almost never manages a full-route reinsertion today.
func repairGreedyNoNewRoute(sol Solution, removed []int, customers map[int]Customer, depot Customer, capacity float64) (Solution, bool)
```

Implementation notes:
- Reuses the same best-feasible-insertion-position search as `repairGreedy`
  (test every `(route, pos)`, keep the cheapest feasible one) — just without
  the "else open a new route" fallback.
- If a customer that had ≥1 feasible slot when the ranking was computed ends
  up with 0 by the time its turn comes (because earlier insertions consumed
  the slack), abort the whole attempt with `ok=false` rather than opening a
  new route.

### 4. Orchestration: `tryRouteElimination`

```go
// tryRouteElimination attempts to eliminate a route, trying up to
// maxAttempts of the weakest routes (in order) before giving up. Returns
// ok=false if none succeed. Bounds cost the same way the existing
// stagnation heuristic bounds its maxAttempts=3 retries.
func tryRouteElimination(sol Solution, customers map[int]Customer, depot Customer, capacity float64, maxAttempts int) (Solution, bool)
```

Default `maxAttempts = 3`.

### 5. Main-loop integration

Extend the existing destroy-type coin-flip (line 130) from a 2-way to a
3-way roll:

- **20%** → Route Elimination: call `tryRouteElimination`. On failure, set
  `candidateSol = currentSol` (no-op clone) — this falls through the
  *existing* accept/reject/logging code unchanged (it naturally rejects as
  "not better," and `stagnationCounter` increments as normal). No new
  branches needed in the accept-check block.
- **40%** → Worst Destroy (unchanged, `destroyWorst` + `repairGreedy`)
- **40%** → Random Destroy (unchanged, `destroyRandom` + `repairGreedy`)

On success, the candidate already has one fewer vehicle, so the existing
unconditional-accept-on-fewer-vehicles check (line 145) takes it immediately
regardless of resulting distance — correct per the hierarchical objective.

The 20/40/40 split is a tunable constant (currently hardcoded as a literal
in the `if/else if` chain, matching the existing style at line 130) — not
exposed as a CLI flag in this iteration.

### 6. Logging

Reuse the existing generic `sendProgressLog` calls (`LNS:CHOOSE`,
`LNS:ACCEPT`/`LNS:REJECT`) that already key off the `destroyType` string —
no new logging categories needed. `destroyType = "Route Elimination"` flows
through unchanged.

## Out of scope (YAGNI)

- No regret-*k* insertion cost (most-constrained-first ordering is enough
  for this iteration; revisit only if empirical testing shows it's
  insufficient).
- No ejection chains / swap moves to manufacture room when reinsertion fails
  outright — a failed attempt just falls through to the next-weakest route
  or gives up for that iteration.
- No CLI flag to tune the 20/40/40 split or `maxAttempts`.
- No changes to the LKH3 stagnation sub-solver path (`invokeLKHSubSolver`) —
  this is scoped to the default (non-LKH) main loop only, per the earlier
  scoping decision (see conversation: "Improve the Go LNS algorithm itself").

## Validation plan

1. Implement in `solver/main.go`, rebuild via `./build_solver.sh` (native
   `solver_bin` — fast CLI iteration loop, no WASM rebuild needed yet).
2. Pick 2-3 Solomon instances where the "lower distance, one extra vehicle
   vs. best-known" symptom has been observed. Run before/after with the same
   `-seed`, compare `TotalVehicles`/`TotalDistance` against
   `BEST_KNOWN_SOLUTIONS` (`src/App.tsx`).
3. Confirm no regression: distance-when-vehicle-count-already-matches
   shouldn't get worse (the new operator should only ever help or be a
   no-op, since it's gated by the existing unconditional-accept-on-fewer-
   vehicles rule).
4. Once validated, rebuild the browser artifacts
   (`./build_lkh_wasm.sh && ./build_solver_wasm.sh`) so the actual deployed
   app picks up the fix — the native `solver_bin` binary itself is not on
   the running app's critical path (see root `CLAUDE.md`, "Commands").
