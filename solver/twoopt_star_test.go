package main

import "testing"

// crossingRoutesCustomers builds two routes whose 2-customer TAILS are
// crossed: route A = [1(near depot,east), 2,3(a tight cluster far north)],
// route B = [4(near depot,north), 5,6(a tight cluster far east)]. Swapping
// tails (1 stays with A, but continues into 5,6; 4 stays with B, but
// continues into 2,3) uncrosses them and is a real improvement.
//
// Demands are set so each route sits exactly at the shared capacity (20):
// 6+7+7 and 6+7+7. Since every customer has positive demand, NO cross-route
// relocation can ever be feasible - only same-route reordering (which can't
// fix a between-route crossing) or the 2-opt* tail swap (which preserves
// each side's total load: 6+7+7 either way) can apply. This is what makes
// the fixture robust: it blocks single-customer Or-opt by hard infeasibility
// rather than by a distance argument that a lucky insertion point could
// undermine.
func crossingRoutesCustomers() map[int]Customer {
	return map[int]Customer{
		0: {ID: 0, X: 0, Y: -10, DueDate: 10000},
		1: {ID: 1, X: 1, Y: 0, Demand: 6, DueDate: 10000},
		2: {ID: 2, X: 0, Y: 20, Demand: 7, DueDate: 10000},
		3: {ID: 3, X: 0, Y: 21, Demand: 7, DueDate: 10000},
		4: {ID: 4, X: 0, Y: 1, Demand: 6, DueDate: 10000},
		5: {ID: 5, X: 20, Y: 0, Demand: 7, DueDate: 10000},
		6: {ID: 6, X: 21, Y: 0, Demand: 7, DueDate: 10000},
	}
}

// crossingRoutesCapacity is the capacity used with crossingRoutesCustomers -
// exactly equal to each route's total demand (20), so no customer can be
// relocated to the other route without exceeding it.
const crossingRoutesCapacity = 20.0

func TestTwoOptStarRoutePairBestMoveUncrossesRoutes(t *testing.T) {
	customers := crossingRoutesCustomers()
	depot := customers[0]
	capacity := crossingRoutesCapacity

	routeA := buildRoute(t, []int{1, 2, 3}, 1, customers, depot, capacity)
	routeB := buildRoute(t, []int{4, 5, 6}, 2, customers, depot, capacity)
	originalTotal := routeA.Distance + routeB.Distance

	newA, newB, improved := twoOptStarRoutePairBestMove(routeA, routeB, customers, depot, capacity)
	if !improved {
		t.Fatalf("twoOptStarRoutePairBestMove() reported no improving move, want the crossing tails to be swapped")
	}

	newTotal := newA.Distance + newB.Distance
	if newTotal >= originalTotal-1e-9 {
		t.Fatalf("twoOptStarRoutePairBestMove() distance %.4f not better than original %.4f", newTotal, originalTotal)
	}

	gotCounts := customerIDCounts([]Route{newA, newB})
	wantCounts := map[int]int{1: 1, 2: 1, 3: 1, 4: 1, 5: 1, 6: 1}
	assertSameCustomerMultiset(t, "twoOptStarRoutePairBestMove", gotCounts, wantCounts)

	if newA.VehicleID != 1 || newB.VehicleID != 2 {
		t.Fatalf("twoOptStarRoutePairBestMove() VehicleIDs = %d,%d, want 1,2 (preserved)", newA.VehicleID, newB.VehicleID)
	}
}

// TestTwoOptStarSucceedsWherePlainLocalSearchCannot proves the new operator
// adds real capability rather than just re-finding what 2-opt/Or-opt already
// reach: running twoOptImproveSolution + orOptImproveSolution alone (i.e.
// localSearchImprove's pre-existing repertoire, without 2-opt*) on the
// crossing fixture leaves it far short of what twoOptStarImproveSolution
// reaches - capacity blocks every cross-route single-customer relocation
// (see crossingRoutesCustomers), so at best 2-opt+Or-opt can only nudge
// each route's own internal order by fractions of a unit; only the tail
// swap closes the ~2-unit crossing gap.
func TestTwoOptStarSucceedsWherePlainLocalSearchCannot(t *testing.T) {
	customers := crossingRoutesCustomers()
	depot := customers[0]
	capacity := crossingRoutesCapacity

	routeA := buildRoute(t, []int{1, 2, 3}, 1, customers, depot, capacity)
	routeB := buildRoute(t, []int{4, 5, 6}, 2, customers, depot, capacity)
	sol := Solution{Routes: []Route{routeA, routeB}}
	recalculateSolutionMetrics(&sol)

	plain := twoOptImproveSolution(sol, customers, depot, capacity)
	plain = orOptImproveSolution(plain, customers, depot, capacity, 3)

	starImproved := twoOptStarImproveSolution(sol, customers, depot, capacity, 3)

	const meaningfulMargin = 1.0 // comfortably above the <0.01 same-route noise 2-opt/Or-opt alone can find here
	if starImproved.TotalDistance >= plain.TotalDistance-meaningfulMargin {
		t.Fatalf("twoOptStarImproveSolution() distance %.4f not meaningfully better than plain 2-opt+Or-opt's %.4f (want a gap of at least %.1f)", starImproved.TotalDistance, plain.TotalDistance, meaningfulMargin)
	}

	gotCounts := customerIDCounts(starImproved.Routes)
	wantCounts := customerIDCounts(sol.Routes)
	assertSameCustomerMultiset(t, "twoOptStarImproveSolution", gotCounts, wantCounts)
}

// TestTwoOptStarRoutePairBestMoveNoOpWhenNothingImproves confirms the two
// true no-op cuts (full swap of both routes' entire contents, and leaving
// both routes exactly as they are) are never reported as an improving move
// on a fixture with no crossing to fix.
func TestTwoOptStarRoutePairBestMoveNoOpWhenNothingImproves(t *testing.T) {
	customers := map[int]Customer{
		0: {ID: 0, X: 0, Y: 0, DueDate: 10000},
		1: {ID: 1, X: 1, Y: 0, DueDate: 10000},
		2: {ID: 2, X: 2, Y: 0, DueDate: 10000},
		3: {ID: 3, X: -1, Y: 0, DueDate: 10000},
		4: {ID: 4, X: -2, Y: 0, DueDate: 10000},
	}
	depot := customers[0]
	capacity := 1e9

	routeA := buildRoute(t, []int{1, 2}, 1, customers, depot, capacity)
	routeB := buildRoute(t, []int{3, 4}, 2, customers, depot, capacity)

	_, _, improved := twoOptStarRoutePairBestMove(routeA, routeB, customers, depot, capacity)
	if improved {
		t.Fatalf("twoOptStarRoutePairBestMove() reported an improving move on two already-optimal, non-crossing routes")
	}
}

// TestTwoOptStarImproveSolutionNeverWorsensOrDropsCustomers is the same
// broad smoke test localsearch_test.go runs over localSearchImprove, applied
// to twoOptStarImproveSolution: whatever it does, it must never increase
// distance or lose/duplicate a customer.
func TestTwoOptStarImproveSolutionNeverWorsensOrDropsCustomers(t *testing.T) {
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

	got := twoOptStarImproveSolution(sol, customers, depot, 1e9, 3)

	if got.TotalDistance > sol.TotalDistance+1e-9 {
		t.Fatalf("twoOptStarImproveSolution() worsened distance: got %.4f, want <= %.4f", got.TotalDistance, sol.TotalDistance)
	}

	gotCounts := customerIDCounts(got.Routes)
	wantCounts := customerIDCounts(sol.Routes)
	assertSameCustomerMultiset(t, "twoOptStarImproveSolution", gotCounts, wantCounts)

	for _, r := range got.Routes {
		if len(r.CustomerIDs) == 0 {
			t.Fatalf("twoOptStarImproveSolution() left an empty route in the result: %+v", got.Routes)
		}
	}
}
