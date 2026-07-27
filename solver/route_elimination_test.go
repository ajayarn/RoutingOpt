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
