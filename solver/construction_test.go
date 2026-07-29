package main

import (
	"math"
	"testing"
)

// customersFrom returns the given IDs from m as a []Customer slice, in the
// given order - the shape buildInitialSolution's `customers` parameter
// expects (as opposed to `customerMap`, which stays a map).
func customersFrom(m map[int]Customer, ids ...int) []Customer {
	out := make([]Customer, 0, len(ids))
	for _, id := range ids {
		out = append(out, m[id])
	}
	return out
}

func TestBuildInitialSolutionCoversEveryCustomerExactlyOnce(t *testing.T) {
	customers := testCustomers()
	depot := testDepot()
	capacity := 30.0 // total demand 5+5+5+9=24, fits in one route

	sol := buildInitialSolution(customersFrom(customers, 1, 2, 3, 4), depot, capacity, customers)

	got := customerIDCounts(sol.Routes)
	want := map[int]int{1: 1, 2: 1, 3: 1, 4: 1}
	assertSameCustomerMultiset(t, "buildInitialSolution", got, want)
}

func TestBuildInitialSolutionProducesOnlyFeasibleRoutes(t *testing.T) {
	customers := testCustomers()
	depot := testDepot()
	capacity := 10.0 // forces multiple routes given demands 5/5/5/9

	sol := buildInitialSolution(customersFrom(customers, 1, 2, 3, 4), depot, capacity, customers)

	if len(sol.Routes) == 0 {
		t.Fatalf("buildInitialSolution() returned no routes")
	}
	for _, r := range sol.Routes {
		if r.Load > capacity {
			t.Errorf("route %v load %.2f exceeds capacity %.2f", r.CustomerIDs, r.Load, capacity)
		}
		if _, feasible := calculateRouteDetails(r.CustomerIDs, customers, depot, capacity); !feasible {
			t.Errorf("route %v is infeasible under independent re-validation", r.CustomerIDs)
		}
	}
}

func TestBuildInitialSolutionDeterministic(t *testing.T) {
	customers := testCustomers()
	depot := testDepot()
	capacity := 10.0

	sol1 := buildInitialSolution(customersFrom(customers, 1, 2, 3, 4), depot, capacity, customers)
	sol2 := buildInitialSolution(customersFrom(customers, 1, 2, 3, 4), depot, capacity, customers)

	if len(sol1.Routes) != len(sol2.Routes) {
		t.Fatalf("route count differs across identical runs: %d vs %d", len(sol1.Routes), len(sol2.Routes))
	}
	for i := range sol1.Routes {
		a := sol1.Routes[i].CustomerIDs
		b := sol2.Routes[i].CustomerIDs
		if len(a) != len(b) {
			t.Fatalf("route %d length differs across runs: %v vs %v", i, a, b)
		}
		for j := range a {
			if a[j] != b[j] {
				t.Fatalf("route %d differs across runs at position %d: %v vs %v", i, j, a, b)
			}
		}
	}
}

// TestBuildInitialSolutionKnownSequenceOnCollinearLayout hand-traces I1 with
// the default parameters (i1Mu=1, i1Alpha1=i1Alpha2=0.5, i1Lambda=2) on
// testCustomers()' collinear layout (depot at x=0; customers at x=1,2,3,4;
// generous capacity and time windows so no infeasibility ever triggers).
// Seeding from the farthest customer (4) and repeatedly picking the
// unrouted customer that maximizes c2 = lambda*d(depot,u) - c1 converges to
// a single route visiting every customer in position order - the only
// non-backtracking tour on a line. This assertion is sensitive to the I1
// constants (i1Mu/i1Alpha1/i1Alpha2/i1Lambda); if those are retuned later,
// this test may need updating even though the coverage/feasibility tests
// above should not.
func TestBuildInitialSolutionKnownSequenceOnCollinearLayout(t *testing.T) {
	customers := testCustomers()
	depot := testDepot()
	capacity := 30.0

	sol := buildInitialSolution(customersFrom(customers, 1, 2, 3, 4), depot, capacity, customers)

	if len(sol.Routes) != 1 {
		t.Fatalf("expected a single route on this generous fixture, got %d routes: %v", len(sol.Routes), sol.Routes)
	}
	want := []int{1, 2, 3, 4}
	got := sol.Routes[0].CustomerIDs
	if len(got) != len(want) {
		t.Fatalf("route = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("route = %v, want %v", got, want)
		}
	}
}

// TestBuildInitialSolutionSeparatesUnreachableTimeWindows builds two
// customers positioned and windowed so that serving both in a single route
// is infeasible in EITHER order (arriving at the second customer always
// exceeds its due date), while each is trivially feasible alone. This
// exercises the c12/time-window feasibility path specifically, not just
// capacity.
func TestBuildInitialSolutionSeparatesUnreachableTimeWindows(t *testing.T) {
	depot := Customer{ID: 0, X: 0, Y: 0, Demand: 0, ReadyTime: 0, DueDate: 1000, ServiceTime: 0}
	custA := Customer{ID: 1, X: 5, Y: 0, Demand: 1, ReadyTime: 0, DueDate: 10, ServiceTime: 0}
	custB := Customer{ID: 2, X: -5, Y: 0, Demand: 1, ReadyTime: 0, DueDate: 10, ServiceTime: 0}
	customerMap := map[int]Customer{1: custA, 2: custB}
	capacity := 100.0

	sol := buildInitialSolution([]Customer{custA, custB}, depot, capacity, customerMap)

	if len(sol.Routes) != 2 {
		t.Fatalf("expected 2 separate routes (customers can't share a route given their windows), got %d: %v", len(sol.Routes), sol.Routes)
	}
	got := customerIDCounts(sol.Routes)
	want := map[int]int{1: 1, 2: 1}
	assertSameCustomerMultiset(t, "buildInitialSolution", got, want)
	for _, r := range sol.Routes {
		if len(r.CustomerIDs) != 1 {
			t.Errorf("expected each route to hold exactly 1 customer, got route %v", r.CustomerIDs)
		}
	}
}

func TestBuildInitialSolutionEmptyInput(t *testing.T) {
	depot := testDepot()
	customers := testCustomers()

	sol := buildInitialSolution(nil, depot, 100.0, customers)

	if len(sol.Routes) != 0 {
		t.Fatalf("buildInitialSolution(nil, ...) = %d routes, want 0", len(sol.Routes))
	}
}

// TestI1C1KnownCost hand-computes c1(depot, u, next) for a 3-4-5-triangle
// fixture chosen so c11 (distance) and c12 (time-shift) are both nonzero
// and distinct, verifying i1c1 assembles alpha1*c11 + alpha2*c12 correctly
// rather than just one term or the other.
//
// next (existing route customer, ID 20) sits at (6,8), 10 units from the
// depot, with ReadyTime=12 - far enough that its route-alone arrival (10)
// makes it wait 2 units (ready 12), but its arrival once u is inserted
// ahead of it (14) does not need to wait.
// u (ID 10) sits at (6,0), 6 units from the depot and 8 units from next
// (vertical leg of the 6-8-10 triangle), with ReadyTime=0.
func TestI1C1KnownCost(t *testing.T) {
	depot := Customer{ID: 0, X: 0, Y: 0, Demand: 0, ReadyTime: 0, DueDate: 1000, ServiceTime: 0}
	custNext := Customer{ID: 20, X: 6, Y: 8, Demand: 1, ReadyTime: 12, DueDate: 100, ServiceTime: 0}
	custU := Customer{ID: 10, X: 6, Y: 0, Demand: 1, ReadyTime: 0, DueDate: 100, ServiceTime: 0}
	customerMap := map[int]Customer{10: custU, 20: custNext}
	capacity := 100.0

	routeCusts := []int{20}
	baseRoute, baseOK := calculateRouteDetails(routeCusts, customerMap, depot, capacity)
	if !baseOK {
		t.Fatalf("base route fixture is infeasible - fix the test data")
	}
	if baseRoute.WaitingTimes[20] != 2 {
		t.Fatalf("fixture sanity check failed: base route wait at 20 = %.4f, want 2 (arrival 10, ready 12)", baseRoute.WaitingTimes[20])
	}

	candIDs := []int{10, 20}
	candRoute, candOK := calculateRouteDetails(candIDs, customerMap, depot, capacity)
	if !candOK {
		t.Fatalf("candidate route fixture is infeasible - fix the test data")
	}
	if candRoute.WaitingTimes[20] != 0 {
		t.Fatalf("fixture sanity check failed: candidate route wait at 20 = %.4f, want 0 (arrival 14, ready 12)", candRoute.WaitingTimes[20])
	}

	got := i1c1(routeCusts, 0, custU, customerMap, depot, baseRoute, candRoute)
	want := 3.0 // c11=4 (6+8-10), c12=2 (14-12), c1=0.5*4+0.5*2=3
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("i1c1() = %v, want %v", got, want)
	}
}

// TestServiceStartAtDepotSuccessor guards against a regression where
// serviceStartAt returns 0 (a zero-value map miss) instead of the true
// depot-arrival time when the "successor" is the depot return leg - the
// majority case for insertions at the tail of a short route, since
// calculateRouteDetails never records the depot itself in its time maps.
func TestServiceStartAtDepotSuccessor(t *testing.T) {
	depot := Customer{ID: 0, X: 0, Y: 0, Demand: 0, ReadyTime: 0, DueDate: 1000, ServiceTime: 0}
	cust := Customer{ID: 1, X: 5, Y: 0, Demand: 1, ReadyTime: 0, DueDate: 100, ServiceTime: 2}
	customerMap := map[int]Customer{1: cust}
	capacity := 100.0

	route, ok := calculateRouteDetails([]int{1}, customerMap, depot, capacity)
	if !ok {
		t.Fatalf("fixture route is infeasible - fix the test data")
	}

	got := serviceStartAt(route, []int{1}, 1, customerMap, depot) // succIdx == len(customerIDs): depot successor
	want := 12.0                                                  // depart(1) = arrival(5) + service(2) = 7; +dist(1,depot)=5 => 12
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("serviceStartAt() (depot successor) = %v, want %v", got, want)
	}
}
