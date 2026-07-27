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
