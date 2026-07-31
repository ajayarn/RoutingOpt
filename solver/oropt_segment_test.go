package main

import "testing"

// TestFindBestSegmentInsertionPrefersReversedOrientationWhenCheaper builds a
// segment [1,2] where 1 belongs on the "next" (customer-4) side and 2
// belongs on the "prev" (customer-3) side - so inserting the segment
// reversed ([2,1]) between 3 and 4 is strictly cheaper than inserting it as
// given ([1,2]). Coordinates are deliberately non-collinear (1 and 2 are
// lifted off the 3-4 axis) - on a perfectly straight line, inserting points
// that fall between two existing stops costs exactly zero extra distance
// regardless of orientation, which would make this fixture degenerate and
// prove nothing about orientation preference.
func TestFindBestSegmentInsertionPrefersReversedOrientationWhenCheaper(t *testing.T) {
	customers := map[int]Customer{
		0: {ID: 0, X: 0, Y: -30, DueDate: 10000},
		1: {ID: 1, X: 15, Y: 8, DueDate: 10000},
		2: {ID: 2, X: -5, Y: 8, DueDate: 10000},
		3: {ID: 3, X: -10, Y: 0, DueDate: 10000}, // prev neighbor, near customer 2
		4: {ID: 4, X: 20, Y: 0, DueDate: 10000},  // next neighbor, near customer 1
	}
	depot := customers[0]
	capacity := 1e9

	route := buildRoute(t, []int{3, 4}, 1, customers, depot, capacity)
	routes := []Route{route}

	routeIdx, pos, reversed, cost, feasible := findBestSegmentInsertion(routes, []int{1, 2}, customers, depot, capacity)
	if !feasible {
		t.Fatalf("findBestSegmentInsertion() feasible=false, want true")
	}
	if routeIdx != 0 || pos != 1 {
		t.Fatalf("findBestSegmentInsertion() routeIdx=%d pos=%d, want 0,1 (between customers 3 and 4)", routeIdx, pos)
	}
	if !reversed {
		t.Fatalf("findBestSegmentInsertion() reversed=false, want true - [2,1] is strictly cheaper between 3(near 2) and 4(near 1) than [1,2]")
	}
	if cost <= 0 {
		t.Fatalf("findBestSegmentInsertion() cost=%.4f, want a real positive insertion cost (adding customers lengthens the route)", cost)
	}
}

// segmentBeatsSingleCustomerFixture places a tight pair (2,3) as a spike off
// route A between two customers (1,4) that are otherwise close to each
// other - removing 2 AND 3 together collapses the spike into a cheap direct
// 1->4 hop, but removing just one still routes near the spike's location
// (via the other member), so the removal savings for either customer ALONE
// is far smaller than for the pair together - a classic case of
// non-additive removal savings that single-customer Or-opt, evaluating each
// customer's move independently, cannot discover. Coordinates were found by
// randomized search and confirmed empirically (not hand-derived) to survive
// a full 3-pass single-customer Or-opt convergence: it settles ~14 units
// worse than moving the pair as a unit.
func segmentBeatsSingleCustomerFixture(t *testing.T) (Solution, map[int]Customer) {
	t.Helper()
	customers := map[int]Customer{
		0: {ID: 0, X: 0, Y: -50, DueDate: 10000},
		1: {ID: 1, X: 0, Y: 0, DueDate: 10000},
		2: {ID: 2, X: 10, Y: 18, DueDate: 10000},
		3: {ID: 3, X: 9, Y: 20, DueDate: 10000},
		4: {ID: 4, X: 13, Y: 0, DueDate: 10000},
		5: {ID: 5, X: -8, Y: 23, DueDate: 10000},
		6: {ID: 6, X: -15, Y: 22, DueDate: 10000},
	}
	depot := customers[0]
	capacity := 1e9

	routeA := buildRoute(t, []int{1, 2, 3, 4}, 1, customers, depot, capacity)
	routeB := buildRoute(t, []int{5, 6}, 2, customers, depot, capacity)

	sol := Solution{Routes: []Route{routeA, routeB}}
	recalculateSolutionMetrics(&sol)
	return sol, customers
}

func TestOrOptSegmentImproveSolutionMovesTightPairAsAUnit(t *testing.T) {
	sol, customers := segmentBeatsSingleCustomerFixture(t)
	depot := customers[0]
	capacity := 1e9

	// Plain single-customer Or-opt alone should NOT find this (proves the
	// new operator adds real capability, not just re-finding the same
	// optimum a different way).
	plain := orOptImproveSolution(sol, customers, depot, capacity, 3)

	got := orOptSegmentImproveSolution(sol, customers, depot, capacity, 2, 3)

	if got.TotalDistance >= plain.TotalDistance-1e-6 {
		t.Fatalf("orOptSegmentImproveSolution() distance %.4f not better than plain Or-opt's %.4f", got.TotalDistance, plain.TotalDistance)
	}

	gotCounts := customerIDCounts(got.Routes)
	wantCounts := customerIDCounts(sol.Routes)
	assertSameCustomerMultiset(t, "orOptSegmentImproveSolution", gotCounts, wantCounts)

	for _, r := range got.Routes {
		if len(r.CustomerIDs) == 0 {
			t.Fatalf("orOptSegmentImproveSolution() left an empty route in the result: %+v", got.Routes)
		}
	}
}

// TestOrOptSegmentImproveSolutionNeverWorsensOrDropsCustomers is the same
// broad smoke test localsearch_test.go runs over localSearchImprove, applied
// to orOptSegmentImproveSolution directly: whatever it does, it must never
// increase distance or vehicle count, or lose/duplicate a customer.
func TestOrOptSegmentImproveSolutionNeverWorsensOrDropsCustomers(t *testing.T) {
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

	for _, segmentLen := range []int{2, 3} {
		got := orOptSegmentImproveSolution(sol, customers, depot, 1e9, segmentLen, 3)

		if got.TotalVehicles > sol.TotalVehicles {
			t.Fatalf("orOptSegmentImproveSolution(segmentLen=%d) increased vehicle count: got %d, want <= %d", segmentLen, got.TotalVehicles, sol.TotalVehicles)
		}
		if got.TotalDistance > sol.TotalDistance+1e-9 {
			t.Fatalf("orOptSegmentImproveSolution(segmentLen=%d) worsened distance: got %.4f, want <= %.4f", segmentLen, got.TotalDistance, sol.TotalDistance)
		}

		gotCounts := customerIDCounts(got.Routes)
		wantCounts := customerIDCounts(sol.Routes)
		assertSameCustomerMultiset(t, "orOptSegmentImproveSolution", gotCounts, wantCounts)
	}
}
