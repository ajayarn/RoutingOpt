# Route-Elimination Operator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the Go LNS solver (`solver/main.go`) a genuine, constructed way to reduce vehicle count, instead of relying on the current destroy/repair operators to stumble into it by luck — fixing the observed "lower distance but one extra vehicle vs. best-known" symptom.

**Architecture:** Add a hard-feasibility-only route-elimination destroy/repair pair (`destroyRouteElimination` + `repairGreedyNoNewRoute`), orchestrated by `tryRouteElimination` with a capacity-based lower-bound short-circuit (`minVehiclesLowerBound`). Fire it two ways: a bounded pre-phase right after construction while the solution is still loose (`runVehicleMinimizationPrePhase`, budget = 10% of `-iterations`), and a 20% opportunistic branch in the main loop's destroy-type choice (`chooseDestroyOperator`, replacing the existing 50/50 split).

**Tech Stack:** Go 1.24 (`solver/main.go`, package `main`, module `routingopt-solver`), Go's standard `testing` package.

## Global Constraints

- Spec of record: `docs/superpowers/specs/2026-07-27-route-elimination-operator-design.md`. Every task below implements one numbered section of that spec — read it first if anything here is ambiguous.
- All new code goes into `solver/main.go`, matching this codebase's existing one-big-file convention (only `solver/main_test.go`, `solver/lkh_native.go`, `solver/lkh_wasm.go` are split out, and those are split for build-tag/test reasons, not style).
- New tests go into a new file `solver/route_elimination_test.go` (package `main`, white-box tests, same pattern as the existing `solver/main_test.go`).
- Run tests from the `solver/` directory: `cd solver && go test ./...`.
- Do not touch `invokeLKHSubSolver`, `-use-lkh`, or any LKH3 code path — this feature is scoped to the default (non-LKH) main loop only.
- Do not add a CLI flag for the 20/40/40 split, `maxAttempts`, or the pre-phase budget — these are hardcoded per the spec's YAGNI section.
- Commit after every task.

---

### Task 1: Capture baseline results (before any code changes)

**Files:**
- None modified — this task only produces a reference file for later comparison.
- Create (scratch, not committed): `/tmp/route-elim-baseline/*.json`, `/tmp/route-elim-baseline/summary.txt`

**Interfaces:**
- Consumes: the committed `solver_bin` binary and `public/data/*.txt` instance files, as they exist on `main` before this branch's changes.
- Produces: `/tmp/route-elim-baseline/summary.txt`, read by Task 10 for the before/after comparison.

- [ ] **Step 1: Rebuild `solver_bin` from the current (unmodified) source, to guarantee the baseline reflects exactly the code about to be changed**

```bash
./build_solver.sh
```

Expected: `==> Compilation successful. Generated solver_bin.`

- [ ] **Step 2: Run the solver against 8 representative instances and save each final result**

```bash
mkdir -p /tmp/route-elim-baseline
for f in c101 c201 r101 r201 r204 rc101 rc201 rc204; do
  ./solver_bin -file "public/data/$f.txt" -iterations 800 -seed 42 2>/dev/null | tail -1 > "/tmp/route-elim-baseline/$f.json"
done
```

- [ ] **Step 3: Summarize vehicles/distance per instance against the published best-known values**

```bash
cat > /tmp/route-elim-baseline/summarize.py << 'EOF'
import json

BEST_KNOWN = {
    "c101": (10, 828.94),
    "c201": (3, 591.56),
    "r101": (19, 1650.8),
    "r201": (4, 1252.37),
    "r204": (2, 825.52),
    "rc101": (14, 1696.95),
    "rc201": (4, 1406.94),
    "rc204": (3, 798.46),
}

for name, (bk_vehicles, bk_distance) in BEST_KNOWN.items():
    with open(f"/tmp/route-elim-baseline/{name}.json") as f:
        result = json.load(f)
    vehicles = result["bestVehicles"]
    distance = result["bestDistance"]
    flag = " <-- EXTRA VEHICLE" if vehicles > bk_vehicles else ""
    print(f"{name}: got {vehicles}v/{distance:.2f}d, best-known {bk_vehicles}v/{bk_distance:.2f}d{flag}")
EOF
python3 /tmp/route-elim-baseline/summarize.py | tee /tmp/route-elim-baseline/summary.txt
```

Expected: a line per instance; note which ones are flagged `<-- EXTRA VEHICLE` — those are the ones Task 10 will re-check after the fix. (`c101` at only 20 iterations was already observed to produce 11 vehicles vs. the 10 best-known during design research, so at least `c101` is likely to show up here.)

- [ ] **Step 4: No commit for this task** (nothing under version control changed) — proceed directly to Task 2.

---

### Task 2: `minVehiclesLowerBound`

**Files:**
- Modify: `solver/main.go` (add function near `recalculateSolutionMetrics`, e.g. directly after it, ~line 793)
- Test: `solver/route_elimination_test.go` (new file)

**Interfaces:**
- Produces: `func minVehiclesLowerBound(customers map[int]Customer, capacity float64) int`

- [ ] **Step 1: Write the failing test in a new file**

```go
package main

import "testing"

func testCustomers() map[int]Customer {
	return map[int]Customer{
		0: {ID: 0, X: 0, Y: 0, Demand: 0, ReadyTime: 0, DueDate: 1000, ServiceTime: 0},
		1: {ID: 1, X: 1, Y: 0, Demand: 5, ReadyTime: 0, DueDate: 100, ServiceTime: 0},
		2: {ID: 2, X: 2, Y: 0, Demand: 5, ReadyTime: 0, DueDate: 100, ServiceTime: 0},
		3: {ID: 3, X: 3, Y: 0, Demand: 5, ReadyTime: 0, DueDate: 100, ServiceTime: 0},
		4: {ID: 4, X: 4, Y: 0, Demand: 9, ReadyTime: 0, DueDate: 100, ServiceTime: 0},
	}
}

func testDepot() Customer {
	return Customer{ID: 0, X: 0, Y: 0, Demand: 0, ReadyTime: 0, DueDate: 1000, ServiceTime: 0}
}

func TestMinVehiclesLowerBound(t *testing.T) {
	customers := testCustomers()
	got := minVehiclesLowerBound(customers, 10)
	want := 3 // total demand 5+5+5+9=24, ceil(24/10)=3
	if got != want {
		t.Fatalf("minVehiclesLowerBound() = %d, want %d", got, want)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd solver && go test ./... -run TestMinVehiclesLowerBound -v`
Expected: FAIL with `undefined: minVehiclesLowerBound` (compile error)

- [ ] **Step 3: Implement `minVehiclesLowerBound` in `solver/main.go`, directly after `recalculateSolutionMetrics`**

```go
// minVehiclesLowerBound is a closed-form LOWER bound on feasible vehicle
// count from capacity alone (bin-packing bound) - cheap to compute, no
// search required. Time windows can only push the true minimum UP from
// this floor, never below it, so once a solution's vehicle count equals
// this bound, further route-elimination attempts are provably pointless.
func minVehiclesLowerBound(customers map[int]Customer, capacity float64) int {
	if capacity <= 0 {
		return 1
	}

	totalDemand := 0.0
	for _, c := range customers {
		totalDemand += c.Demand
	}

	bound := int(math.Ceil(totalDemand / capacity))
	if bound < 1 {
		bound = 1
	}
	return bound
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd solver && go test ./... -run TestMinVehiclesLowerBound -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add solver/main.go solver/route_elimination_test.go
git commit -m "Add minVehiclesLowerBound: free capacity-based floor on vehicle count"
```

---

### Task 3: `selectWeakestRoutes`

**Files:**
- Modify: `solver/main.go` (add function directly after `minVehiclesLowerBound`)
- Test: `solver/route_elimination_test.go`

**Interfaces:**
- Consumes: `Solution`, `map[int]Customer` (as defined in `solver/main.go`)
- Produces: `func selectWeakestRoutes(sol Solution, customers map[int]Customer) []int` — a ranking of route indices, ascending by (customer count, total demand)

- [ ] **Step 1: Write the failing test**

Add to `solver/route_elimination_test.go`:

```go
// buildRoute constructs a feasible Route for the given customer IDs using
// the same calculateRouteDetails logic the solver itself uses, failing the
// test immediately if the fixture is accidentally infeasible.
func buildRoute(t *testing.T, ids []int, vehicleID int, customers map[int]Customer, depot Customer, capacity float64) Route {
	t.Helper()
	r, feasible := calculateRouteDetails(ids, customers, depot, capacity)
	if !feasible {
		t.Fatalf("test fixture route %v is infeasible - fix the test data", ids)
	}
	r.VehicleID = vehicleID
	return r
}

func TestSelectWeakestRoutesRanksLowestDemandFirst(t *testing.T) {
	customers := testCustomers()
	depot := testDepot()
	capacity := 10.0

	sol := Solution{Routes: []Route{
		buildRoute(t, []int{1}, 1, customers, depot, capacity), // demand 5
		buildRoute(t, []int{2}, 2, customers, depot, capacity), // demand 5
		buildRoute(t, []int{3}, 3, customers, depot, capacity), // demand 5
		buildRoute(t, []int{4}, 4, customers, depot, capacity), // demand 9
	}}

	ranked := selectWeakestRoutes(sol, customers)

	if len(ranked) != 4 {
		t.Fatalf("selectWeakestRoutes() returned %d entries, want 4", len(ranked))
	}
	// All routes have 1 customer, so ranking is by demand ascending - the
	// demand-9 route (index 3) must rank last.
	if ranked[3] != 3 {
		t.Fatalf("selectWeakestRoutes() = %v, want the demand-9 route (index 3) ranked last", ranked)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd solver && go test ./... -run TestSelectWeakestRoutes -v`
Expected: FAIL with `undefined: selectWeakestRoutes`

- [ ] **Step 3: Implement `selectWeakestRoutes` in `solver/main.go`**

```go
// selectWeakestRoutes ranks route indices ascending by "how easy this route
// is to eliminate" - fewest customers first (fewest things needing a new
// home), tie-broken by lowest total demand (most likely to fit into other
// routes' remaining capacity).
func selectWeakestRoutes(sol Solution, customers map[int]Customer) []int {
	type routeWeight struct {
		idx    int
		count  int
		demand float64
	}

	weights := make([]routeWeight, len(sol.Routes))
	for i, r := range sol.Routes {
		d := 0.0
		for _, cID := range r.CustomerIDs {
			d += customers[cID].Demand
		}
		weights[i] = routeWeight{idx: i, count: len(r.CustomerIDs), demand: d}
	}

	sort.Slice(weights, func(i, j int) bool {
		if weights[i].count != weights[j].count {
			return weights[i].count < weights[j].count
		}
		return weights[i].demand < weights[j].demand
	})

	ranked := make([]int, len(weights))
	for i, w := range weights {
		ranked[i] = w.idx
	}
	return ranked
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd solver && go test ./... -run TestSelectWeakestRoutes -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add solver/main.go solver/route_elimination_test.go
git commit -m "Add selectWeakestRoutes: rank routes by ease of elimination"
```

---

### Task 4: `destroyRouteElimination`

**Files:**
- Modify: `solver/main.go` (add directly after `destroyRandom`, ~line 924, alongside the other destroy operators)
- Test: `solver/route_elimination_test.go`

**Interfaces:**
- Produces: `func destroyRouteElimination(sol Solution, routeIdx int) (Solution, []int)`

- [ ] **Step 1: Write the failing test**

```go
func TestDestroyRouteEliminationRemovesWholeRouteOnly(t *testing.T) {
	customers := testCustomers()
	depot := testDepot()
	capacity := 10.0

	original := Solution{Routes: []Route{
		buildRoute(t, []int{1}, 1, customers, depot, capacity),
		buildRoute(t, []int{2, 3}, 2, customers, depot, capacity),
	}}

	result, removed := destroyRouteElimination(original, 0)

	if len(result.Routes) != 1 {
		t.Fatalf("destroyRouteElimination() left %d routes, want 1", len(result.Routes))
	}
	if got := result.Routes[0].CustomerIDs; len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("destroyRouteElimination() left the wrong route intact: %v", got)
	}
	if len(removed) != 1 || removed[0] != 1 {
		t.Fatalf("destroyRouteElimination() removed = %v, want [1]", removed)
	}

	// The original Solution passed in must be untouched.
	if len(original.Routes) != 2 || len(original.Routes[0].CustomerIDs) != 1 || original.Routes[0].CustomerIDs[0] != 1 {
		t.Fatalf("destroyRouteElimination() mutated its input: %+v", original.Routes)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd solver && go test ./... -run TestDestroyRouteElimination -v`
Expected: FAIL with `undefined: destroyRouteElimination`

- [ ] **Step 3: Implement `destroyRouteElimination` in `solver/main.go`, directly after `destroyRandom`**

```go
// destroyRouteElimination removes the entire route at routeIdx (not a
// random subset) and returns the remaining solution plus every customer ID
// that was evicted. Same (Solution, []int) shape as
// destroyWorst/destroyRandom, but always empties one whole route rather
// than a random k customers scattered across many routes.
func destroyRouteElimination(sol Solution, routeIdx int) (Solution, []int) {
	removed := make([]int, len(sol.Routes[routeIdx].CustomerIDs))
	copy(removed, sol.Routes[routeIdx].CustomerIDs)

	newRoutes := make([]Route, 0, len(sol.Routes)-1)
	for i, r := range sol.Routes {
		if i != routeIdx {
			newRoutes = append(newRoutes, r)
		}
	}

	partialSol := Solution{Routes: newRoutes}
	recalculateSolutionMetrics(&partialSol)
	return partialSol, removed
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd solver && go test ./... -run TestDestroyRouteElimination -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add solver/main.go solver/route_elimination_test.go
git commit -m "Add destroyRouteElimination: empty one whole route, not a random subset"
```

---

### Task 5: `repairGreedyNoNewRoute`

**Files:**
- Modify: `solver/main.go` (add directly after `repairGreedy`, ~line 985)
- Test: `solver/route_elimination_test.go`

**Interfaces:**
- Consumes: `calculateRouteDetails` (existing, `solver/main.go:722`)
- Produces: `func repairGreedyNoNewRoute(sol Solution, removed []int, customers map[int]Customer, depot Customer, capacity float64) (Solution, bool)`

- [ ] **Step 1: Write the failing tests (success and failure cases)**

```go
func TestRepairGreedyNoNewRouteReinsertsWithoutOpeningNewRoute(t *testing.T) {
	customers := testCustomers()
	depot := testDepot()
	capacity := 10.0

	sol := Solution{Routes: []Route{
		buildRoute(t, []int{2}, 1, customers, depot, capacity), // demand 5, room for 5 more
		buildRoute(t, []int{3}, 2, customers, depot, capacity), // demand 5, room for 5 more
	}}
	recalculateSolutionMetrics(&sol)

	result, ok := repairGreedyNoNewRoute(sol, []int{1}, customers, depot, capacity)
	if !ok {
		t.Fatalf("repairGreedyNoNewRoute() returned ok=false, want true (customer 1 fits in either existing route)")
	}
	if len(result.Routes) != 2 {
		t.Fatalf("repairGreedyNoNewRoute() opened a new route: got %d routes, want 2", len(result.Routes))
	}

	seen := map[int]bool{}
	for _, r := range result.Routes {
		for _, cID := range r.CustomerIDs {
			seen[cID] = true
		}
		if r.Load > capacity {
			t.Fatalf("repairGreedyNoNewRoute() produced an over-capacity route: load %.1f > %.1f", r.Load, capacity)
		}
	}
	for _, want := range []int{1, 2, 3} {
		if !seen[want] {
			t.Fatalf("repairGreedyNoNewRoute() result is missing customer %d: %+v", want, result.Routes)
		}
	}
}

func TestRepairGreedyNoNewRouteFailsWithoutMutatingInput(t *testing.T) {
	customers := testCustomers()
	depot := testDepot()
	capacity := 10.0

	sol := Solution{Routes: []Route{
		buildRoute(t, []int{1}, 1, customers, depot, capacity), // demand 5, only 5 of room left
	}}
	recalculateSolutionMetrics(&sol)

	// Customer 4 has demand 9; 5 (existing) + 9 > capacity 10, so it cannot
	// fit in the only existing route - the attempt must fail outright.
	result, ok := repairGreedyNoNewRoute(sol, []int{4}, customers, depot, capacity)
	if ok {
		t.Fatalf("repairGreedyNoNewRoute() returned ok=true, want false (customer 4 cannot fit)")
	}
	if len(result.Routes) != 1 || len(result.Routes[0].CustomerIDs) != 1 || result.Routes[0].CustomerIDs[0] != 1 {
		t.Fatalf("repairGreedyNoNewRoute() returned a mutated solution on failure: %+v", result.Routes)
	}

	// The original sol argument itself must also be untouched.
	if len(sol.Routes) != 1 || len(sol.Routes[0].CustomerIDs) != 1 || sol.Routes[0].CustomerIDs[0] != 1 {
		t.Fatalf("repairGreedyNoNewRoute() mutated its input: %+v", sol.Routes)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd solver && go test ./... -run TestRepairGreedyNoNewRoute -v`
Expected: FAIL with `undefined: repairGreedyNoNewRoute`

- [ ] **Step 3: Implement `repairGreedyNoNewRoute` in `solver/main.go`, directly after `repairGreedy`**

```go
// repairGreedyNoNewRoute attempts to reinsert every customer in `removed`
// into sol's existing routes only - it may never open a new route. Returns
// ok=false (sol returned unchanged, no partial commit) if any customer
// cannot be placed feasibly.
//
// Insertion order is most-constrained-first, recomputed dynamically before
// each insertion: whichever not-yet-placed customer currently has the
// fewest feasible (route, position) slots goes next. This matters because
// inserting "easy" customers first can consume the capacity/time slack a
// "hard" customer needed - exactly why repairGreedy's random insertion
// order almost never manages a full-route reinsertion.
func repairGreedyNoNewRoute(sol Solution, removed []int, customers map[int]Customer, depot Customer, capacity float64) (Solution, bool) {
	remaining := make(map[int]bool, len(removed))
	for _, cID := range removed {
		remaining[cID] = true
	}

	for len(remaining) > 0 {
		bestCustID := -1
		bestRouteIdx := -1
		bestPos := -1
		bestSlotCount := -1

		for cID := range remaining {
			slotCount := 0
			custRouteIdx := -1
			custPos := -1
			custCost := 1e9

			for rIdx, r := range sol.Routes {
				for pos := 0; pos <= len(r.CustomerIDs); pos++ {
					testRoute := make([]int, len(r.CustomerIDs)+1)
					copy(testRoute[:pos], r.CustomerIDs[:pos])
					testRoute[pos] = cID
					copy(testRoute[pos+1:], r.CustomerIDs[pos:])

					rDetails, feasible := calculateRouteDetails(testRoute, customers, depot, capacity)
					if feasible {
						slotCount++
						cost := rDetails.Distance - r.Distance
						if cost < custCost {
							custCost = cost
							custRouteIdx = rIdx
							custPos = pos
						}
					}
				}
			}

			if slotCount == 0 {
				// This customer has nowhere feasible to go in the existing
				// routes - the whole elimination attempt fails. sol is
				// returned as received by the caller (see Task 5 test
				// TestRepairGreedyNoNewRouteFailsWithoutMutatingInput): no
				// insertion has been committed to sol.Routes yet on this
				// path because every accepted insertion below replaces a
				// route's CustomerIDs via append onto a fresh slice, which
				// this codebase's routes always have exactly enough
				// capacity to force a reallocation on (see calculateRouteDetails,
				// which always builds CustomerIDs via a fresh slice) - so
				// earlier accepted insertions in previous loop iterations
				// never alias sol's original backing arrays either.
				return sol, false
			}

			if bestSlotCount == -1 || slotCount < bestSlotCount {
				bestSlotCount = slotCount
				bestCustID = cID
				bestRouteIdx = custRouteIdx
				bestPos = custPos
			}
		}

		r := &sol.Routes[bestRouteIdx]
		newIDs := make([]int, len(r.CustomerIDs)+1)
		copy(newIDs[:bestPos], r.CustomerIDs[:bestPos])
		newIDs[bestPos] = bestCustID
		copy(newIDs[bestPos+1:], r.CustomerIDs[bestPos:])

		rDetails, _ := calculateRouteDetails(newIDs, customers, depot, capacity)
		rDetails.VehicleID = r.VehicleID
		sol.Routes[bestRouteIdx] = rDetails

		delete(remaining, bestCustID)
	}

	for idx := range sol.Routes {
		sol.Routes[idx].VehicleID = idx + 1
	}
	recalculateSolutionMetrics(&sol)
	return sol, true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd solver && go test ./... -run TestRepairGreedyNoNewRoute -v`
Expected: PASS (both tests)

- [ ] **Step 5: Commit**

```bash
git add solver/main.go solver/route_elimination_test.go
git commit -m "Add repairGreedyNoNewRoute: most-constrained-first insertion, never opens a route"
```

---

### Task 6: `tryRouteElimination`

**Files:**
- Modify: `solver/main.go` (add directly after `repairGreedyNoNewRoute`)
- Test: `solver/route_elimination_test.go`

**Interfaces:**
- Consumes: `minVehiclesLowerBound`, `selectWeakestRoutes`, `destroyRouteElimination`, `repairGreedyNoNewRoute` (Tasks 2-5)
- Produces: `func tryRouteElimination(sol Solution, customers map[int]Customer, depot Customer, capacity float64, maxAttempts int) (Solution, bool)`

- [ ] **Step 1: Write the failing tests**

```go
func TestTryRouteEliminationReducesVehicleCount(t *testing.T) {
	customers := testCustomers()
	depot := testDepot()
	capacity := 10.0

	sol := Solution{Routes: []Route{
		buildRoute(t, []int{1}, 1, customers, depot, capacity), // demand 5
		buildRoute(t, []int{2}, 2, customers, depot, capacity), // demand 5
		buildRoute(t, []int{3}, 3, customers, depot, capacity), // demand 5
		buildRoute(t, []int{4}, 4, customers, depot, capacity), // demand 9
	}}
	recalculateSolutionMetrics(&sol)

	result, ok := tryRouteElimination(sol, customers, depot, capacity, 3)
	if !ok {
		t.Fatalf("tryRouteElimination() returned ok=false, want true (two demand-5 routes should merge)")
	}
	if result.TotalVehicles != 3 {
		t.Fatalf("tryRouteElimination() left %d vehicles, want 3", result.TotalVehicles)
	}

	lowerBound := minVehiclesLowerBound(customers, capacity)
	if lowerBound != 3 {
		t.Fatalf("test assumption broken: lower bound = %d, want 3", lowerBound)
	}

	// Now at the capacity lower bound - a further attempt must short-circuit.
	_, ok = tryRouteElimination(result, customers, depot, capacity, 3)
	if ok {
		t.Fatalf("tryRouteElimination() succeeded again at the capacity lower bound - should have short-circuited")
	}
}

func TestTryRouteEliminationShortCircuitsAtLowerBound(t *testing.T) {
	customers := map[int]Customer{
		0: {ID: 0, X: 0, Y: 0, Demand: 0, ReadyTime: 0, DueDate: 1000},
		1: {ID: 1, X: 1, Y: 0, Demand: 5, ReadyTime: 0, DueDate: 100},
	}
	depot := customers[0]
	capacity := 10.0 // lower bound = ceil(5/10) = 1

	sol := Solution{Routes: []Route{
		buildRoute(t, []int{1}, 1, customers, depot, capacity),
	}}
	recalculateSolutionMetrics(&sol)

	result, ok := tryRouteElimination(sol, customers, depot, capacity, 3)
	if ok {
		t.Fatalf("tryRouteElimination() returned ok=true, want false - already at the 1-vehicle capacity floor")
	}
	if result.TotalVehicles != 1 {
		t.Fatalf("tryRouteElimination() changed vehicle count: got %d, want 1", result.TotalVehicles)
	}
}

func TestTryRouteEliminationFailsWhenTimeWindowsPreventMerging(t *testing.T) {
	depot := Customer{ID: 0, X: 0, Y: 0, Demand: 0, ReadyTime: 0, DueDate: 1000, ServiceTime: 0}
	custA := Customer{ID: 1, X: 40, Y: 0, Demand: 1, ReadyTime: 0, DueDate: 40, ServiceTime: 0}
	custB := Customer{ID: 2, X: 0, Y: 40, Demand: 1, ReadyTime: 0, DueDate: 40, ServiceTime: 0}
	customers := map[int]Customer{0: depot, 1: custA, 2: custB}
	capacity := 100.0

	sol := Solution{Routes: []Route{
		buildRoute(t, []int{1}, 1, customers, depot, capacity),
		buildRoute(t, []int{2}, 2, customers, depot, capacity),
	}}
	recalculateSolutionMetrics(&sol)

	if lb := minVehiclesLowerBound(customers, capacity); sol.TotalVehicles <= lb {
		t.Fatalf("test setup invalid: TotalVehicles=%d must be > lower bound=%d so the short-circuit doesn't mask the real repair failure", sol.TotalVehicles, lb)
	}

	result, ok := tryRouteElimination(sol, customers, depot, capacity, 3)
	if ok {
		t.Fatalf("tryRouteElimination() returned ok=true, want false - the two customers' time windows make merging infeasible in both orders")
	}
	if result.TotalVehicles != 2 {
		t.Fatalf("tryRouteElimination() changed vehicle count on failure: got %d, want 2", result.TotalVehicles)
	}
	if len(sol.Routes) != 2 || len(sol.Routes[0].CustomerIDs) != 1 || len(sol.Routes[1].CustomerIDs) != 1 {
		t.Fatalf("tryRouteElimination() mutated its input on failure: %+v", sol.Routes)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd solver && go test ./... -run TestTryRouteElimination -v`
Expected: FAIL with `undefined: tryRouteElimination`

- [ ] **Step 3: Implement `tryRouteElimination` in `solver/main.go`**

```go
// tryRouteElimination attempts to eliminate a route, trying up to
// maxAttempts of the weakest routes (in order, see selectWeakestRoutes)
// before giving up. Returns ok=false (sol unchanged) if none succeed, or
// immediately if sol's vehicle count already equals the capacity lower
// bound - no point attempting further reduction. Bounds cost the same way
// the existing stagnation heuristic bounds its maxAttempts=3 retries.
func tryRouteElimination(sol Solution, customers map[int]Customer, depot Customer, capacity float64, maxAttempts int) (Solution, bool) {
	if sol.TotalVehicles <= minVehiclesLowerBound(customers, capacity) {
		return sol, false
	}

	ranked := selectWeakestRoutes(sol, customers)
	if maxAttempts > len(ranked) {
		maxAttempts = len(ranked)
	}

	for i := 0; i < maxAttempts; i++ {
		partialSol, removed := destroyRouteElimination(sol, ranked[i])
		repaired, ok := repairGreedyNoNewRoute(partialSol, removed, customers, depot, capacity)
		if ok {
			return repaired, true
		}
	}

	return sol, false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd solver && go test ./... -run TestTryRouteElimination -v`
Expected: PASS (all three tests)

- [ ] **Step 5: Commit**

```bash
git add solver/main.go solver/route_elimination_test.go
git commit -m "Add tryRouteElimination: orchestrate weakest-route retries with lower-bound short-circuit"
```

---

### Task 7: `chooseDestroyOperator` + wire the 3-way operator choice into the main loop

**Files:**
- Modify: `solver/main.go` (add `chooseDestroyOperator` near `shouldTriggerStagnationSolver`, ~line 63; modify the main loop's destroy-type selection at ~line 126-139)
- Test: `solver/route_elimination_test.go`

**Interfaces:**
- Consumes: `tryRouteElimination` (Task 6), `destroyWorst`, `destroyRandom`, `repairGreedy` (existing)
- Produces: `func chooseDestroyOperator(roll float64) string` returning `"Route Elimination"`, `"Worst Destroy"`, or `"Random Destroy"`

- [ ] **Step 1: Write the failing test**

```go
func TestChooseDestroyOperatorSplit(t *testing.T) {
	cases := []struct {
		roll float64
		want string
	}{
		{0.0, "Route Elimination"},
		{0.19, "Route Elimination"},
		{0.20, "Worst Destroy"},
		{0.59, "Worst Destroy"},
		{0.60, "Random Destroy"},
		{0.999, "Random Destroy"},
	}
	for _, c := range cases {
		if got := chooseDestroyOperator(c.roll); got != c.want {
			t.Errorf("chooseDestroyOperator(%.3f) = %q, want %q", c.roll, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd solver && go test ./... -run TestChooseDestroyOperatorSplit -v`
Expected: FAIL with `undefined: chooseDestroyOperator`

- [ ] **Step 3: Implement `chooseDestroyOperator` in `solver/main.go`, directly after `shouldTriggerStagnationSolver`**

```go
// chooseDestroyOperator maps a uniform random roll in [0, 1) to a destroy
// operator name using the 20% Route Elimination / 40% Worst / 40% Random
// split. Extracted as a pure function so the split itself is unit-testable
// without running a full solve.
func chooseDestroyOperator(roll float64) string {
	switch {
	case roll < 0.20:
		return "Route Elimination"
	case roll < 0.60:
		return "Worst Destroy"
	default:
		return "Random Destroy"
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd solver && go test ./... -run TestChooseDestroyOperatorSplit -v`
Expected: PASS

- [ ] **Step 5: Wire it into the main loop, replacing the existing 50/50 destroy-type selection**

In `solver/main.go`, find this exact block (inside the `for iter := 1; iter <= *iterations; iter++ {` loop):

```go
		// 1. Destroy
		var removed []int
		var partialSol Solution
		destroyType := "Worst Destroy"
		if rand.Float64() < 0.5 {
			partialSol, removed = destroyWorst(currentSol, k, customerMap, depot)
		} else {
			destroyType = "Random Destroy"
			partialSol, removed = destroyRandom(currentSol, k, customerMap, depot)
		}
		sendProgressLog(iter, bestSol, startTime, "LNS:CHOOSE", "Neighborhood '%s' selected to remove %d customers: %v", destroyType, k, removed)

		// 2. Repair
		candidateSol := repairGreedy(partialSol, removed, customerMap, depot, capacity)
```

Replace it with:

```go
		// 1. Destroy + 2. Repair
		destroyType := chooseDestroyOperator(rand.Float64())
		var candidateSol Solution

		if destroyType == "Route Elimination" {
			eliminated, ok := tryRouteElimination(currentSol, customerMap, depot, capacity, 3)
			sendProgressLog(iter, bestSol, startTime, "LNS:CHOOSE", "Neighborhood '%s' attempted (success=%v)", destroyType, ok)
			if ok {
				candidateSol = eliminated
			} else {
				candidateSol = currentSol
			}
		} else {
			var removed []int
			var partialSol Solution
			if destroyType == "Worst Destroy" {
				partialSol, removed = destroyWorst(currentSol, k, customerMap, depot)
			} else {
				partialSol, removed = destroyRandom(currentSol, k, customerMap, depot)
			}
			sendProgressLog(iter, bestSol, startTime, "LNS:CHOOSE", "Neighborhood '%s' selected to remove %d customers: %v", destroyType, k, removed)
			candidateSol = repairGreedy(partialSol, removed, customerMap, depot, capacity)
		}
```

This preserves everything downstream unchanged: `candidateSol.TotalVehicles`/`TotalDistance` comparisons still work identically, and `destroyType == "Random Destroy"`'s always-accept special case (a few lines further down) still matches only that exact string, so "Route Elimination" failures correctly fall through to a normal rejection rather than being force-accepted.

- [ ] **Step 6: Build to confirm the integration compiles**

```bash
cd solver && go build ./...
```

Expected: no output, exit code 0

- [ ] **Step 7: Run the full test suite**

Run: `cd solver && go test ./... -v`
Expected: PASS (all tests, including the pre-existing `TestStagnationSolverRetriggersPeriodically`)

- [ ] **Step 8: Commit**

```bash
git add solver/main.go solver/route_elimination_test.go
git commit -m "Wire Route Elimination into the main loop as a 20% probabilistic operator"
```

---

### Task 8: `runVehicleMinimizationPrePhase` + wire it into `main()` after construction

**Files:**
- Modify: `solver/main.go` (add `runVehicleMinimizationPrePhase` directly after `tryRouteElimination`; modify `main()` right after `buildInitialSolution`, ~line 99-107)
- Test: `solver/route_elimination_test.go`

**Interfaces:**
- Consumes: `minVehiclesLowerBound`, `tryRouteElimination` (Tasks 2, 6), `sendProgressLog` (existing)
- Produces: `func runVehicleMinimizationPrePhase(sol Solution, customers map[int]Customer, depot Customer, capacity float64, budget int, startTime time.Time) Solution`

- [ ] **Step 1: Write the failing tests**

`solver/route_elimination_test.go` currently starts with a single-line import
(`import "testing"`, added in Task 2). Change it to a grouped import that
also pulls in `"time"`:

```go
import (
	"testing"
	"time"
)
```

Then add:

```go
func TestVehicleMinimizationPrePhaseStopsAtLowerBound(t *testing.T) {
	customers := testCustomers() // demand 5,5,5,9 -> lower bound 3 @ capacity 10
	depot := testDepot()
	capacity := 10.0

	sol := Solution{Routes: []Route{
		buildRoute(t, []int{1}, 1, customers, depot, capacity),
		buildRoute(t, []int{2}, 2, customers, depot, capacity),
		buildRoute(t, []int{3}, 3, customers, depot, capacity),
		buildRoute(t, []int{4}, 4, customers, depot, capacity),
	}}
	recalculateSolutionMetrics(&sol)

	result := runVehicleMinimizationPrePhase(sol, customers, depot, capacity, 50, time.Now())

	if result.TotalVehicles != 3 {
		t.Fatalf("runVehicleMinimizationPrePhase() left %d vehicles, want 3 (the capacity lower bound)", result.TotalVehicles)
	}
}

func TestVehicleMinimizationPrePhaseRespectsZeroBudget(t *testing.T) {
	customers := testCustomers()
	depot := testDepot()
	capacity := 10.0

	sol := Solution{Routes: []Route{
		buildRoute(t, []int{1}, 1, customers, depot, capacity),
		buildRoute(t, []int{2}, 2, customers, depot, capacity),
		buildRoute(t, []int{3}, 3, customers, depot, capacity),
		buildRoute(t, []int{4}, 4, customers, depot, capacity),
	}}
	recalculateSolutionMetrics(&sol)

	// A budget of 0 must be a complete no-op.
	result := runVehicleMinimizationPrePhase(sol, customers, depot, capacity, 0, time.Now())
	if result.TotalVehicles != 4 {
		t.Fatalf("runVehicleMinimizationPrePhase() with budget=0 changed vehicle count: got %d, want 4 (unchanged)", result.TotalVehicles)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd solver && go test ./... -run TestVehicleMinimizationPrePhase -v`
Expected: FAIL with `undefined: runVehicleMinimizationPrePhase`

- [ ] **Step 3: Implement `runVehicleMinimizationPrePhase` in `solver/main.go`, directly after `tryRouteElimination`**

```go
// runVehicleMinimizationPrePhase attempts to reduce vehicle count as far as
// possible while the solution is still loose (freshly constructed, not yet
// distance-optimized) - see
// docs/superpowers/specs/2026-07-27-route-elimination-operator-design.md
// for why this runs up front rather than only reactively. Stops as soon as
// any of the following happens: a route-elimination attempt fails
// (deterministic given the same solution, so retrying immediately can't
// succeed), the capacity lower bound is reached, or budget attempts are
// used up.
func runVehicleMinimizationPrePhase(sol Solution, customers map[int]Customer, depot Customer, capacity float64, budget int, startTime time.Time) Solution {
	lowerBound := minVehiclesLowerBound(customers, capacity)

	for attempt := 1; attempt <= budget; attempt++ {
		if sol.TotalVehicles <= lowerBound {
			sendProgressLog(0, sol, startTime, "VEHICLE-MIN", "Reached capacity lower bound (%d vehicles) after %d attempt(s) - stopping pre-phase", lowerBound, attempt-1)
			return sol
		}

		newSol, ok := tryRouteElimination(sol, customers, depot, capacity, 3)
		if !ok {
			sendProgressLog(0, sol, startTime, "VEHICLE-MIN", "No further route elimination possible after %d attempt(s); stalled at %d vehicles (capacity floor %d)", attempt, sol.TotalVehicles, lowerBound)
			return sol
		}

		sol = newSol
		sendProgressLog(0, sol, startTime, "VEHICLE-MIN", "Eliminated a route on attempt %d - now %d vehicles, %.2f distance", attempt, sol.TotalVehicles, sol.TotalDistance)
	}

	sendProgressLog(0, sol, startTime, "VEHICLE-MIN", "Pre-phase budget (%d attempts) exhausted at %d vehicles (capacity floor %d)", budget, sol.TotalVehicles, lowerBound)
	return sol
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd solver && go test ./... -run TestVehicleMinimizationPrePhase -v`
Expected: PASS (both tests)

- [ ] **Step 5: Wire the pre-phase into `main()`, right after construction and before the first progress report**

In `solver/main.go`, find this exact block:

```go
	// 2. Build Initial Feasible Solution
	sol := buildInitialSolution(customers, depot, capacity, customerMap)
	if len(sol.Routes) == 0 {
		sendError("Failed to build a feasible initial solution")
		return
	}

	// Send initial progress
	sendProgress(0, sol, startTime)
```

Replace it with:

```go
	// 2. Build Initial Feasible Solution
	sol := buildInitialSolution(customers, depot, capacity, customerMap)
	if len(sol.Routes) == 0 {
		sendError("Failed to build a feasible initial solution")
		return
	}

	// 2b. Vehicle-minimization pre-phase: while the solution is still loose
	// (freshly constructed, not yet distance-optimized), aggressively try
	// to eliminate routes before the main distance-focused loop starts. See
	// docs/superpowers/specs/2026-07-27-route-elimination-operator-design.md.
	prePhaseBudget := int(0.10 * float64(*iterations))
	sol = runVehicleMinimizationPrePhase(sol, customerMap, depot, capacity, prePhaseBudget, startTime)

	// Send initial progress
	sendProgress(0, sol, startTime)
```

- [ ] **Step 6: Build to confirm the integration compiles**

```bash
cd solver && go build ./...
```

Expected: no output, exit code 0

- [ ] **Step 7: Run the full test suite**

Run: `cd solver && go test ./... -v`
Expected: PASS (all tests)

- [ ] **Step 8: Commit**

```bash
git add solver/main.go solver/route_elimination_test.go
git commit -m "Front-load vehicle-minimization as a 10%-of-iterations pre-phase after construction"
```

---

### Task 9: Full build and native-binary smoke test

**Files:**
- None modified — this task only builds and does a manual smoke run.

- [ ] **Step 1: Run the full test suite one more time from the repo root's perspective**

```bash
cd solver && go test ./... -v && cd ..
```

Expected: PASS (all tests)

- [ ] **Step 2: Rebuild the native CLI binary**

```bash
./build_solver.sh
```

Expected: `==> Compilation successful. Generated solver_bin.`

- [ ] **Step 3: Smoke-test a single instance and confirm the output is still well-formed JSON with the expected fields**

```bash
./solver_bin -file public/data/c101.txt -iterations 50 -seed 42 2>&1 | tail -1 | python3 -c "import json,sys; d=json.load(sys.stdin); print('type:', d['type']); print('vehicles:', d['bestVehicles']); print('distance:', d['bestDistance'])"
```

Expected: prints `type: result`, a `vehicles:` line, and a `distance:` line, with no errors/tracebacks.

- [ ] **Step 4: Commit the rebuilt binary**

```bash
git add solver_bin
git commit -m "Rebuild solver_bin with the route-elimination operator"
```

---

### Task 10: Compare against baseline, rebuild WASM artifacts, final commit

**Files:**
- Modify: `public/wasm/solver.wasm`, `public/wasm/wasm_exec.js` (rebuilt binaries)

**Interfaces:**
- Consumes: `/tmp/route-elim-baseline/summary.txt` (Task 1)

- [ ] **Step 1: Re-run the same 8-instance sweep with the new code and compare against the Task 1 baseline**

```bash
mkdir -p /tmp/route-elim-after
for f in c101 c201 r101 r201 r204 rc101 rc201 rc204; do
  ./solver_bin -file "public/data/$f.txt" -iterations 800 -seed 42 2>/dev/null | tail -1 > "/tmp/route-elim-after/$f.json"
done

cat > /tmp/route-elim-after/compare.py << 'EOF'
import json

BEST_KNOWN = {
    "c101": (10, 828.94),
    "c201": (3, 591.56),
    "r101": (19, 1650.8),
    "r201": (4, 1252.37),
    "r204": (2, 825.52),
    "rc101": (14, 1696.95),
    "rc201": (4, 1406.94),
    "rc204": (3, 798.46),
}

for name, (bk_vehicles, bk_distance) in BEST_KNOWN.items():
    with open(f"/tmp/route-elim-baseline/{name}.json") as f:
        before = json.load(f)
    with open(f"/tmp/route-elim-after/{name}.json") as f:
        after = json.load(f)

    bv, bd = before["bestVehicles"], before["bestDistance"]
    av, ad = after["bestVehicles"], after["bestDistance"]
    verdict = "IMPROVED" if av < bv or (av == bv and ad < bd) else ("REGRESSED" if av > bv or (av == bv and ad > bd) else "SAME")
    print(f"{name}: before={bv}v/{bd:.2f}d after={av}v/{ad:.2f}d best-known={bk_vehicles}v/{bk_distance:.2f}d [{verdict}]")
EOF
python3 /tmp/route-elim-after/compare.py
```

Expected: instances flagged `<-- EXTRA VEHICLE` in the Task 1 baseline should now show `av <= bv` (same or fewer vehicles) with no instance showing `REGRESSED`. If any instance regresses, stop and investigate before proceeding — do not rebuild WASM on top of a regression.

- [ ] **Step 2: Rebuild the browser (WASM) artifacts**

```bash
./build_solver_wasm.sh
```

Expected: `==> Compilation successful. Generated public/wasm/{solver.wasm, wasm_exec.js, lkh_wasm.js, lkh_wasm.wasm}.` (this only needs a plain Go toolchain, not Emscripten/emsdk - it reuses the already-committed `lkh_wasm.cjs`/`lkh_wasm.wasm`, which are unaffected by this change)

- [ ] **Step 3: Confirm only the expected files changed**

```bash
git status --short
```

Expected: `public/wasm/solver.wasm` modified (and possibly `public/wasm/wasm_exec.js` if the Go toolchain version differs from whatever built the last committed copy); `public/wasm/lkh_wasm.js`/`public/wasm/lkh_wasm.wasm` should be unchanged since this task didn't touch LKH3.

- [ ] **Step 4: Commit the rebuilt WASM artifacts**

```bash
git add public/wasm/solver.wasm public/wasm/wasm_exec.js
git commit -m "Rebuild WASM solver artifacts with the route-elimination operator"
```

- [ ] **Step 5: Manually verify in the browser**

```bash
npm run dev
```

Open http://localhost:3000, pick one of the instances flagged in Task 1 (e.g. c101), start a solve, and confirm the solver log shows `[VEHICLE-MIN]` entries near the start and, if applicable, `[LNS:CHOOSE] Neighborhood 'Route Elimination' attempted` entries during the run. Confirm the final vehicle count matches or improves on what Task 1's baseline captured for that instance. Stop the dev server (Ctrl+C) when done.
