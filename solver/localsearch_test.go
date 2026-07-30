package main

import (
	"reflect"
	"testing"
)

// TestTwoOptRouteFixesCrossingOrder uses four colinear customers where the
// input order zig-zags (1,3,2,4) - strictly longer than visiting them in
// coordinate order - and checks 2-opt untangles it back to (1,2,3,4).
func TestTwoOptRouteFixesCrossingOrder(t *testing.T) {
	customers := map[int]Customer{
		0: {ID: 0, X: 0, Y: 0, DueDate: 1000},
		1: {ID: 1, X: 1, Y: 0, DueDate: 1000},
		2: {ID: 2, X: 2, Y: 0, DueDate: 1000},
		3: {ID: 3, X: 3, Y: 0, DueDate: 1000},
		4: {ID: 4, X: 4, Y: 0, DueDate: 1000},
	}
	depot := customers[0]
	route := buildRoute(t, []int{1, 3, 2, 4}, 7, customers, depot, 1e9)

	got := twoOptRoute(route, customers, depot, 1e9)

	want := []int{1, 2, 3, 4}
	if !reflect.DeepEqual(got.CustomerIDs, want) {
		t.Fatalf("twoOptRoute() CustomerIDs = %v, want %v", got.CustomerIDs, want)
	}
	if got.Distance >= route.Distance {
		t.Fatalf("twoOptRoute() distance %.2f not better than input %.2f", got.Distance, route.Distance)
	}
	if got.VehicleID != 7 {
		t.Fatalf("twoOptRoute() VehicleID = %d, want 7 (preserved)", got.VehicleID)
	}
}

// TestOrOptImproveSolutionRelocatesAcrossRoutes places customer 2 right next
// to customer 1 (in a different route) but geometrically far from its own
// routemate 3, so relocating 2 into 1's route is a strict net win. Verifies
// orOptImproveSolution finds cross-route moves, not just within-route ones.
func TestOrOptImproveSolutionRelocatesAcrossRoutes(t *testing.T) {
	customers := map[int]Customer{
		0: {ID: 0, X: 0, Y: 0, DueDate: 1000},
		1: {ID: 1, X: 0, Y: 4, DueDate: 1000},
		2: {ID: 2, X: 0, Y: 5, DueDate: 1000},
		3: {ID: 3, X: 50, Y: 0, DueDate: 1000},
	}
	depot := customers[0]

	routeA := buildRoute(t, []int{1}, 1, customers, depot, 1e9)
	routeB := buildRoute(t, []int{2, 3}, 2, customers, depot, 1e9)
	sol := Solution{Routes: []Route{routeA, routeB}}
	recalculateSolutionMetrics(&sol)

	got := orOptImproveSolution(sol, customers, depot, 1e9, 5)

	gotCounts := customerIDCounts(got.Routes)
	wantCounts := map[int]int{1: 1, 2: 1, 3: 1}
	assertSameCustomerMultiset(t, "orOptImproveSolution", gotCounts, wantCounts)

	if got.TotalDistance >= sol.TotalDistance {
		t.Fatalf("orOptImproveSolution() distance %.2f not better than input %.2f", got.TotalDistance, sol.TotalDistance)
	}

	// Customer 2 (X=0,Y=5) is far cheaper to serve alongside customer 1
	// (X=0,Y=4) than alongside its original routemate 3 (X=50,Y=0) - a
	// feasible cross-route relocation must have happened, whether that
	// lands 2 in 1's route or - even better, since capacity here is
	// unbounded - merges everything into one route and drops the other to
	// empty (which orOptImproveSolution must then prune, not leave as a
	// phantom zero-customer vehicle).
	for _, r := range got.Routes {
		if len(r.CustomerIDs) == 0 {
			t.Fatalf("orOptImproveSolution() left an empty route in the result: %+v", got.Routes)
		}
	}
	if got.TotalVehicles > sol.TotalVehicles {
		t.Fatalf("orOptImproveSolution() increased vehicle count: got %d, want <= %d", got.TotalVehicles, sol.TotalVehicles)
	}
}

// TestLocalSearchImproveNeverWorsensOrDropsCustomers is a broader smoke test
// over a bigger, less hand-tuned instance: whatever localSearchImprove does,
// it must never increase distance, change vehicle count, or lose/duplicate a
// customer.
func TestLocalSearchImproveNeverWorsensOrDropsCustomers(t *testing.T) {
	customers := map[int]Customer{
		0:  {ID: 0, X: 40, Y: 50, DueDate: 1000},
		1:  {ID: 1, X: 45, Y: 68, DueDate: 967, ReadyTime: 912},
		2:  {ID: 2, X: 45, Y: 70, DueDate: 964, ReadyTime: 825},
		3:  {ID: 3, X: 42, Y: 66, DueDate: 973, ReadyTime: 65},
		4:  {ID: 4, X: 42, Y: 68, DueDate: 963, ReadyTime: 727},
		5:  {ID: 5, X: 42, Y: 65, DueDate: 973, ReadyTime: 15},
		6:  {ID: 6, X: 40, Y: 69, DueDate: 940, ReadyTime: 621},
		7:  {ID: 7, X: 40, Y: 66, DueDate: 923, ReadyTime: 170},
		8:  {ID: 8, X: 38, Y: 68, DueDate: 940, ReadyTime: 255},
		9:  {ID: 9, X: 38, Y: 70, DueDate: 999, ReadyTime: 534},
	}
	depot := customers[0]

	ids := []int{1, 2, 3, 4, 5, 6, 7, 8, 9}
	routeA := buildRoute(t, ids[:5], 1, customers, depot, 1e9)
	routeB := buildRoute(t, ids[5:], 2, customers, depot, 1e9)
	sol := Solution{Routes: []Route{routeA, routeB}}
	recalculateSolutionMetrics(&sol)

	got := localSearchImprove(sol, customers, depot, 1e9)

	if got.TotalVehicles != sol.TotalVehicles {
		t.Fatalf("localSearchImprove() changed vehicle count: got %d, want %d", got.TotalVehicles, sol.TotalVehicles)
	}
	if got.TotalDistance > sol.TotalDistance+1e-9 {
		t.Fatalf("localSearchImprove() worsened distance: got %.4f, want <= %.4f", got.TotalDistance, sol.TotalDistance)
	}

	gotCounts := customerIDCounts(got.Routes)
	wantCounts := customerIDCounts(sol.Routes)
	assertSameCustomerMultiset(t, "localSearchImprove", gotCounts, wantCounts)
}
