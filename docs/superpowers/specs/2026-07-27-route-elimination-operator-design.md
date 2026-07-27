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

## A note on timing: why this can't just be reactive cleanup

Proving "K vehicles is enough" for a specific customer set is a feasibility
search with no closed-form shortcut (confirmed empirically: a from-scratch
LKH3 run on R204 needed ~170 seconds / 266 trials just to find *any*
feasible 2-vehicle tour, out of a 40-minute full solve — see the
conversation this spec came out of). That search gets *harder*, not easier,
against an already distance-optimized solution, since tightening routes for
distance consumes exactly the capacity/time-window slack that redistributing
customers into fewer routes needs. So this design fires the elimination
operator in two places: once aggressively while the solution is still loose
(right after construction), and then opportunistically for the rest of the
run as cheap ongoing insurance — see sections 6-7.

Deliberately not adopted: LKH3's approach of relaxing capacity/time-windows
into soft penalties and searching a combined objective. That's more
powerful (it's why LKH3 eventually succeeded), but it means redefining the
objective/acceptance model throughout the solver, not just adding an
operator — a much bigger change that re-implements a chunk of what LKH3
already does. Out of scope per the earlier decision to improve the Go LNS
itself rather than lean on LKH3; revisit only if sections 1-7 below prove
insufficient in testing.

## Design

### 1. Capacity lower bound (free signal)

```go
// minVehiclesLowerBound is a closed-form LOWER bound on feasible vehicle
// count from capacity alone (bin-packing bound) — cheap to compute, no
// search required. Time windows can only push the true minimum UP from
// this floor, never below it, so once len(sol.Routes) == this bound,
// further elimination attempts are provably pointless and can be skipped.
func minVehiclesLowerBound(customers map[int]Customer, capacity float64) int
```

`ceil(totalDemand / capacity)`. Used by both the pre-phase (section 6) and
the opportunistic operator (section 7) to skip futile attempts and to give
the logs a concrete stopping signal ("stuck at 3 vehicles, capacity floor is
2 — still room" vs. "stuck at 3, floor is 3 — capacity-optimal").

### 2. Route selection: `selectWeakestRoutes`

```go
// selectWeakestRoutes ranks route indices ascending by "how easy this route
// is to eliminate" — fewest customers first (fewest things needing a new
// home), tie-broken by lowest total demand (most likely to fit into other
// routes' remaining capacity).
func selectWeakestRoutes(sol Solution, customers map[int]Customer) []int
```

Returns a full ranking (not just the single weakest) so callers can retry
against the next-weakest route without recomputing.

### 3. Destroy: `destroyRouteElimination`

```go
// destroyRouteElimination removes the entire route at routeIdx (not a
// random subset) and returns the remaining solution plus every customer ID
// that was evicted. Same (Solution, []int) shape as destroyWorst/destroyRandom.
func destroyRouteElimination(sol Solution, routeIdx int) (Solution, []int)
```

### 4. Repair: `repairGreedyNoNewRoute`

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
  via a shared `findBestInsertion` helper extracted from `repairGreedy`
  during implementation (test every `(route, pos)`, keep the cheapest
  feasible one) — just without the "else open a new route" fallback. This
  is a deliberate, narrow exception to leaving existing code untouched: it
  removes duplication between the two repair functions at the cost of a
  small, behavior-preserving refactor of `repairGreedy` (locked in by a
  characterization test before the refactor lands).
- If a customer that had ≥1 feasible slot when the ranking was computed ends
  up with 0 by the time its turn comes (because earlier insertions consumed
  the slack), abort the whole attempt with `ok=false` rather than opening a
  new route.

### 5. Orchestration: `tryRouteElimination`

```go
// tryRouteElimination attempts to eliminate a route, trying up to
// maxAttempts of the weakest routes (in order) before giving up. Returns
// ok=false if none succeed, or immediately if len(sol.Routes) already
// equals minVehiclesLowerBound (section 1) — no point attempting further
// reduction. Bounds cost the same way the existing stagnation heuristic
// bounds its maxAttempts=3 retries.
func tryRouteElimination(sol Solution, customers map[int]Customer, depot Customer, capacity float64, maxAttempts int) (Solution, bool)
```

Default `maxAttempts = 3`.

### 6. Front-loaded vehicle-minimization pre-phase (primary mechanism)

Immediately after `buildInitialSolution` and before the main distance-focused
LNS loop starts, run a dedicated pre-phase: repeatedly call
`tryRouteElimination` against the current solution (accepting every success
unconditionally — fewer vehicles always wins), continuing until either:

- it stalls (`tryRouteElimination` fails to eliminate any of the
  `maxAttempts` (3) weakest routes — not a full pass over every route via
  `selectWeakestRoutes`, only the top `maxAttempts` of its ranking), or
- `len(sol.Routes) == minVehiclesLowerBound(...)` (capacity-optimal, section 1), or
- the pre-phase's iteration budget is exhausted.

**Budget: the first 10% of `-iterations`**, e.g. `prePhaseIterations :=
int(0.10 * float64(*iterations))`, with each pre-phase attempt counted
against that budget the same way a main-loop iteration is counted. This
keeps it bounded and proportional to however long the user asked the whole
solve to run, rather than a fixed constant that'd be wrong at both small and
large `-iterations` values — consistent with "bound the trial-and-error
cost" being the practical answer to "no closed-form formula" (same principle
LKH3's `MAX_TRIALS` uses).

This phase runs *while the solution is still loose* (freshly constructed,
not yet distance-optimized), which is exactly when redistributing customers
across routes is easiest — see the timing note above. The main loop's
iteration counter/progress reporting starts after this phase completes, so
existing progress-percentage semantics for the main loop are unaffected;
the pre-phase gets its own `sendProgressLog` category (e.g.
`VEHICLE-MIN:*`) so it's visible in the solver log as a distinct stage.

### 7. Opportunistic mid-loop firing (secondary, cheap insurance)

Keep the existing plan for the main loop: extend the destroy-type coin-flip
(line 130) from 2-way to 3-way — **20% Route Elimination / 40% Worst / 40%
Random**. Distance optimization can occasionally open new slack (e.g.
2-opt/or-opt tightening two routes might make a third eliminable that wasn't
before), so this stays in as low-cost ongoing insurance even though the
pre-phase (section 6) now carries the primary responsibility.

On failure, `candidateSol` is left equal to `currentSol` (a no-op) and falls
through the *existing* accept/reject/logging code unchanged. On success, the
existing unconditional-accept-on-fewer-vehicles check (line 145) takes it
immediately. `tryRouteElimination`'s own lower-bound short-circuit (section
5) keeps this cheap once capacity-optimal — it won't waste cycles retrying
elimination that's already provably impossible.

### 8. Logging

Reuse the existing generic `sendProgressLog` calls (`LNS:CHOOSE`,
`LNS:ACCEPT`/`LNS:REJECT`) for the mid-loop (section 7) firing — they
already key off the `destroyType` string, so `destroyType = "Route
Elimination"` flows through unchanged. The pre-phase (section 6) gets its
own category (`VEHICLE-MIN:*`) since it isn't part of the main iteration
loop.

## Out of scope (YAGNI)

- No regret-*k* insertion cost (most-constrained-first ordering is enough
  for this iteration; revisit only if empirical testing shows it's
  insufficient).
- No ejection chains / swap moves to manufacture room when reinsertion fails
  outright — a failed attempt just falls through to the next-weakest route
  or gives up.
- No penalty-relaxation / soft-constraint search mode (LKH3-style) — see
  "A note on timing" above.
- No CLI flag to tune the 20/40/40 split, `maxAttempts`, or the pre-phase's
  10% budget.
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
3. Confirm the pre-phase (section 6) actually reaches, or gets closer to,
   the capacity lower bound within its 10% budget on those instances — log
   output should show `VEHICLE-MIN` entries and a final pre-phase vehicle
   count.
4. Confirm no regression: distance-when-vehicle-count-already-matches
   shouldn't get worse (the new operators only ever help or no-op, since
   they're gated by the existing unconditional-accept-on-fewer-vehicles
   rule).
5. Once validated, rebuild the browser artifacts
   (`./build_lkh_wasm.sh && ./build_solver_wasm.sh`) so the actual deployed
   app picks up the fix — the native `solver_bin` binary itself is not on
   the running app's critical path (see root `CLAUDE.md`, "Commands").
