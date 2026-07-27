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
