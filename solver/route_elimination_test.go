package main

import (
	"testing"
	"time"
)

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

// customerIDCounts returns a count of every customer ID appearing across all
// routes' CustomerIDs - a multiset, not a set, so it can catch both a
// dropped customer (count goes from 1 to 0) and a duplicated one (count goes
// from 1 to 2), unlike a map[int]bool "seen" check.
func customerIDCounts(routes []Route) map[int]int {
	counts := make(map[int]int)
	for _, r := range routes {
		for _, cID := range r.CustomerIDs {
			counts[cID]++
		}
	}
	return counts
}

// assertSameCustomerMultiset fails the test if got and want don't contain
// exactly the same customer IDs with the same multiplicities.
func assertSameCustomerMultiset(t *testing.T, label string, got, want map[int]int) {
	t.Helper()
	for id, wantCount := range want {
		if got[id] != wantCount {
			t.Fatalf("%s: customer %d appears %d time(s), want %d (got=%v, want=%v)", label, id, got[id], wantCount, got, want)
		}
	}
	for id, gotCount := range got {
		if want[id] != gotCount {
			t.Fatalf("%s: customer %d appears %d time(s), want %d (got=%v, want=%v)", label, id, gotCount, want[id], got, want)
		}
	}
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

func TestDestroyRouteEliminationDoesNotAliasRetainedRoutesCustomerIDs(t *testing.T) {
	customers := testCustomers()
	depot := testDepot()
	capacity := 10.0

	original := Solution{Routes: []Route{
		buildRoute(t, []int{1}, 1, customers, depot, capacity),
		buildRoute(t, []int{2}, 2, customers, depot, capacity),
	}}

	partialSol, _ := destroyRouteElimination(original, 0)

	// Mutate the retained route's CustomerIDs slice element directly. If
	// destroyRouteElimination aliased the original's backing array instead
	// of deep-copying it (via cloneSolution), this write would be visible
	// through `original` too - this is the property repairGreedy's
	// in-place append-based insertion (solver/main.go) would otherwise
	// silently violate on any route whose CustomerIDs slice has spare
	// capacity (the normal case for routes built via repeated
	// single-element appends elsewhere in this file).
	partialSol.Routes[0].CustomerIDs[0] = 999

	if original.Routes[1].CustomerIDs[0] != 2 {
		t.Fatalf("destroyRouteElimination() aliased the retained route's CustomerIDs backing array: original.Routes[1].CustomerIDs[0] = %d, want 2 (unaffected by mutating partialSol)", original.Routes[1].CustomerIDs[0])
	}
}

func TestRepairGreedyBehaviorUnchangedByRefactor(t *testing.T) {
	customers := testCustomers()
	depot := testDepot()
	capacity := 10.0

	// Case 1: the removed customer fits in an existing route - no new route
	// should be opened.
	sol := Solution{Routes: []Route{
		buildRoute(t, []int{2}, 1, customers, depot, capacity), // demand 5, room for 5 more
		buildRoute(t, []int{3}, 2, customers, depot, capacity), // demand 5, room for 5 more
	}}
	recalculateSolutionMetrics(&sol)

	result := repairGreedy(sol, []int{1}, customers, depot, capacity)

	if len(result.Routes) != 2 {
		t.Fatalf("repairGreedy() opened a new route unexpectedly: got %d routes, want 2", len(result.Routes))
	}
	seen := map[int]bool{}
	for _, r := range result.Routes {
		for _, cID := range r.CustomerIDs {
			seen[cID] = true
		}
	}
	for _, want := range []int{1, 2, 3} {
		if !seen[want] {
			t.Fatalf("repairGreedy() result is missing customer %d: %+v", want, result.Routes)
		}
	}

	// Case 2: nothing fits anywhere - repairGreedy must fall back to
	// opening a new route (unlike repairGreedyNoNewRoute, it always
	// succeeds).
	sol2 := Solution{Routes: []Route{
		buildRoute(t, []int{1}, 1, customers, depot, capacity), // demand 5, only 5 of room left
	}}
	recalculateSolutionMetrics(&sol2)
	result2 := repairGreedy(sol2, []int{4}, customers, depot, capacity) // demand 9, can't fit
	if len(result2.Routes) != 2 {
		t.Fatalf("repairGreedy() did not open a new route when nothing fit: got %d routes, want 2", len(result2.Routes))
	}
}

func TestRepairGreedyNoNewRouteReinsertsWithoutOpeningNewRoute(t *testing.T) {
	customers := testCustomers()
	depot := testDepot()
	capacity := 10.0

	sol := Solution{Routes: []Route{
		buildRoute(t, []int{2}, 1, customers, depot, capacity), // demand 5, room for 5 more
		buildRoute(t, []int{3}, 2, customers, depot, capacity), // demand 5, room for 5 more
	}}
	recalculateSolutionMetrics(&sol)

	// Baseline: whatever was already routed in sol, plus what's being
	// reinserted, is exactly what the result must contain - no more, no
	// less.
	removed := []int{1}
	wantCounts := customerIDCounts(sol.Routes)
	for _, cID := range removed {
		wantCounts[cID]++
	}

	result, ok := repairGreedyNoNewRoute(sol, removed, customers, depot, capacity)
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

	// No customer silently dropped or duplicated across the repair.
	assertSameCustomerMultiset(t, "repairGreedyNoNewRoute() customer conservation", customerIDCounts(result.Routes), wantCounts)
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

// TestRepairGreedyNoNewRouteDoesNotPartiallyCommitOnLaterFailure covers a
// case TestRepairGreedyNoNewRouteFailsWithoutMutatingInput does not: more
// than one customer in `removed`, where the first is feasible and gets
// inserted before a later one turns out to have nowhere left to go. Because
// `sol.Routes` is a slice, passing sol by value only copies the slice
// header - `sol.Routes[i] = ...` still writes through to the same backing
// array the caller's Solution literal owns, so a naive implementation can
// commit the first customer's insertion into the caller's own routes
// *before* discovering the second customer is infeasible and returning
// ok=false. That violates "no partial commit" even though the single-write,
// single-customer case (the test above) never triggers it.
func TestRepairGreedyNoNewRouteDoesNotPartiallyCommitOnLaterFailure(t *testing.T) {
	customers := map[int]Customer{
		0: {ID: 0, X: 0, Y: 0, Demand: 0, ReadyTime: 0, DueDate: 1000, ServiceTime: 0},
		1: {ID: 1, X: 1, Y: 0, Demand: 10, ReadyTime: 0, DueDate: 1000, ServiceTime: 0},
		2: {ID: 2, X: 2, Y: 0, Demand: 6, ReadyTime: 0, DueDate: 1000, ServiceTime: 0},
		3: {ID: 3, X: 3, Y: 0, Demand: 5, ReadyTime: 0, DueDate: 1000, ServiceTime: 0},
	}
	depot := testDepot()
	capacity := 10.0

	original := Solution{Routes: []Route{
		buildRoute(t, []int{1}, 1, customers, depot, capacity), // demand 10, no room left
		{VehicleID: 2, CustomerIDs: []int{}},                   // empty route, room for 10
	}}
	recalculateSolutionMetrics(&original)

	// Customer 2 (demand 6) and customer 3 (demand 5) both individually fit
	// in the empty route2 (room 10), but not together (6+5=11 > 10) - and
	// route1 has no room for either. So whichever is inserted first
	// "succeeds" locally, but the attempt as a whole must still fail once
	// the second customer is found to have nowhere left to go.
	result, ok := repairGreedyNoNewRoute(original, []int{2, 3}, customers, depot, capacity)
	if ok {
		t.Fatalf("repairGreedyNoNewRoute() returned ok=true, want false (customers 2 and 3 together can't fit in the only open room)")
	}

	// The returned Solution must not be a partially-repaired one: route2
	// must still be empty, not holding whichever of {2,3} got inserted
	// before the failure was discovered.
	if len(result.Routes) != 2 || len(result.Routes[1].CustomerIDs) != 0 {
		t.Fatalf("repairGreedyNoNewRoute() returned a partially-committed solution on failure: %+v", result.Routes)
	}

	// The original argument must also be untouched.
	if len(original.Routes) != 2 || len(original.Routes[1].CustomerIDs) != 0 {
		t.Fatalf("repairGreedyNoNewRoute() mutated its input on failure: %+v", original.Routes)
	}
}

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
	wantCounts := customerIDCounts(sol.Routes)

	result, ok := tryRouteElimination(sol, customers, depot, capacity, 3)
	if !ok {
		t.Fatalf("tryRouteElimination() returned ok=false, want true (two demand-5 routes should merge)")
	}
	if result.TotalVehicles != 3 {
		t.Fatalf("tryRouteElimination() left %d vehicles, want 3", result.TotalVehicles)
	}

	// A successful elimination must not silently drop or duplicate a customer.
	assertSameCustomerMultiset(t, "tryRouteElimination() customer conservation", customerIDCounts(result.Routes), wantCounts)

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

// TestTryRouteEliminationShortCircuitStopsEvenWhenMergePossible distinguishes
// the minVehiclesLowerBound short-circuit from an ordinary repair failure.
// Every other short-circuit test's fixture happens to also fail the
// destroy+repair attempt on its own (capacity infeasibility right at the
// bound), so deleting the short-circuit line wouldn't change their outcome -
// this test's fixture is built so the merge would actually SUCCEED if
// attempted, proving the short-circuit itself is doing real work.
//
// customer 5 (demand 8) is deliberately never placed into any route - it
// exists only so minVehiclesLowerBound (which sums demand across the whole
// `customers` map, independent of what's actually routed in `sol`) computes
// a bound of 2 that matches sol.TotalVehicles, even though the two routed
// customers (1 and 2, demand 3 each) could easily share one vehicle. Don't
// "clean up" customer 5 as dead fixture data - it's load-bearing.
func TestTryRouteEliminationShortCircuitStopsEvenWhenMergePossible(t *testing.T) {
	customers := map[int]Customer{
		0: {ID: 0, X: 0, Y: 0, Demand: 0, ReadyTime: 0, DueDate: 1000},
		1: {ID: 1, X: 1, Y: 0, Demand: 3, ReadyTime: 0, DueDate: 1000},
		2: {ID: 2, X: 2, Y: 0, Demand: 3, ReadyTime: 0, DueDate: 1000},
		5: {ID: 5, X: 100, Y: 100, Demand: 8, ReadyTime: 0, DueDate: 1000}, // unrouted; see comment above
	}
	depot := customers[0]
	capacity := 10.0 // total demand across the map: 3+3+8=14, ceil(14/10)=2

	sol := Solution{Routes: []Route{
		buildRoute(t, []int{1}, 1, customers, depot, capacity), // demand 3
		buildRoute(t, []int{2}, 2, customers, depot, capacity), // demand 3
	}}
	recalculateSolutionMetrics(&sol)

	if got := minVehiclesLowerBound(customers, capacity); got != 2 {
		t.Fatalf("test assumption broken: lower bound = %d, want 2", got)
	}
	if sol.TotalVehicles != 2 {
		t.Fatalf("test assumption broken: sol.TotalVehicles = %d, want 2", sol.TotalVehicles)
	}

	// Positive control: prove the merge really would succeed without the
	// short-circuit, by driving destroy+repair directly. If this ever starts
	// failing, the fixture no longer discriminates and needs revisiting.
	partialSol, removed := destroyRouteElimination(sol, 0)
	if _, ok := repairGreedyNoNewRoute(partialSol, removed, customers, depot, capacity); !ok {
		t.Fatalf("test fixture broken: destroy+repair of route 0 should succeed (routes 1 and 2 together fit comfortably under capacity %v), but repairGreedyNoNewRoute returned ok=false", capacity)
	}

	result, ok := tryRouteElimination(sol, customers, depot, capacity, 3)
	if ok {
		t.Fatalf("tryRouteElimination() returned ok=true, want false - already at the capacity lower bound (2), short-circuit should have stopped it before attempting the merge")
	}
	if result.TotalVehicles != 2 || len(result.Routes) != 2 {
		t.Fatalf("tryRouteElimination() changed the solution despite the short-circuit: %+v", result.Routes)
	}
	if len(sol.Routes) != 2 || len(sol.Routes[0].CustomerIDs) != 1 || len(sol.Routes[1].CustomerIDs) != 1 {
		t.Fatalf("tryRouteElimination() mutated its input: %+v", sol.Routes)
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
