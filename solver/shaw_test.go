package main

import (
	"testing"
)

// twoClusterCustomers builds an 8-customer fixture on a single line: a tight
// cluster near the depot (IDs 1-4, x=1..4) and a second tight cluster far
// away (IDs 5-8, x=101..104). Demands and time windows are uniform so the
// only real signal distinguishing customers is geography (and, since travel
// time dominates arrival time here, current-solution arrival time correlates
// with it) - exactly the setup needed to check that Shaw removal groups
// removals by relatedness rather than picking uniformly at random.
func twoClusterCustomers() map[int]Customer {
	customers := map[int]Customer{
		0: {ID: 0, X: 0, Y: 0, Demand: 0, ReadyTime: 0, DueDate: 10000, ServiceTime: 0},
	}
	for i := 1; i <= 4; i++ {
		customers[i] = Customer{ID: i, X: float64(i), Y: 0, Demand: 5, ReadyTime: 0, DueDate: 10000, ServiceTime: 0}
	}
	for i := 5; i <= 8; i++ {
		customers[i] = Customer{ID: i, X: float64(100 + i - 4), Y: 0, Demand: 5, ReadyTime: 0, DueDate: 10000, ServiceTime: 0}
	}
	return customers
}

func TestDestroyShawRemovesRequestedCount(t *testing.T) {
	customers := twoClusterCustomers()
	depot := customers[0]
	capacity := 1000.0

	sol := Solution{Routes: []Route{
		buildRoute(t, []int{1, 2, 3, 4, 5, 6, 7, 8}, 1, customers, depot, capacity),
	}}
	recalculateSolutionMetrics(&sol)

	partial, removed := destroyShaw(sol, 3, customers, depot)

	if len(removed) != 3 {
		t.Fatalf("destroyShaw removed %d customers, want 3 (removed=%v)", len(removed), removed)
	}

	want := customerIDCounts(sol.Routes)
	for _, id := range removed {
		want[id]--
		if want[id] == 0 {
			delete(want, id)
		}
	}
	assertSameCustomerMultiset(t, "destroyShaw partial solution", customerIDCounts(partial.Routes), want)
}

func TestDestroyShawClampsKToRoutedCount(t *testing.T) {
	customers := twoClusterCustomers()
	depot := customers[0]
	capacity := 1000.0

	sol := Solution{Routes: []Route{
		buildRoute(t, []int{1, 2, 3, 4, 5, 6, 7, 8}, 1, customers, depot, capacity),
	}}
	recalculateSolutionMetrics(&sol)

	partial, removed := destroyShaw(sol, 100, customers, depot)

	if len(removed) != 8 {
		t.Fatalf("destroyShaw(k=100) removed %d customers, want 8 (all routed customers)", len(removed))
	}
	if len(partial.Routes) != 0 {
		t.Fatalf("destroyShaw(k=100) left %d routes, want 0 (every customer removed)", len(partial.Routes))
	}
}

func TestDestroyShawNoRoutedCustomersIsNoOp(t *testing.T) {
	customers := twoClusterCustomers()
	depot := customers[0]

	sol := Solution{Routes: nil}
	partial, removed := destroyShaw(sol, 3, customers, depot)

	if removed != nil {
		t.Fatalf("destroyShaw on an empty solution removed %v, want nil", removed)
	}
	if len(partial.Routes) != 0 {
		t.Fatalf("destroyShaw on an empty solution produced %d routes, want 0", len(partial.Routes))
	}
}

// TestDestroyShawGroupsRemovalsByRelatedness is the key behavioral test:
// removing 3 of the 8 customers uniformly at random would land all 3 in the
// same 4-customer cluster only ~14% of the time (C(4,3)*2 / C(8,3) = 8/56).
// Shaw removal's whole point is to prefer related customers, so it should do
// this dramatically more often. 60% is a conservative bound comfortably
// above the random baseline, chosen to avoid test flakiness while still
// being a meaningful behavioral assertion (measured empirically around 73%
// over 1000 trials with shawRandomization=6).
func TestDestroyShawGroupsRemovalsByRelatedness(t *testing.T) {
	customers := twoClusterCustomers()
	depot := customers[0]
	capacity := 1000.0

	sol := Solution{Routes: []Route{
		buildRoute(t, []int{1, 2, 3, 4, 5, 6, 7, 8}, 1, customers, depot, capacity),
	}}
	recalculateSolutionMetrics(&sol)

	const trials = 300
	sameCluster := 0
	for i := 0; i < trials; i++ {
		_, removed := destroyShaw(sol, 3, customers, depot)
		if len(removed) != 3 {
			t.Fatalf("trial %d: destroyShaw removed %d customers, want 3", i, len(removed))
		}
		allLow := true  // all IDs <= 4 (cluster A)
		allHigh := true // all IDs >= 5 (cluster B)
		for _, id := range removed {
			if id > 4 {
				allLow = false
			}
			if id <= 4 {
				allHigh = false
			}
		}
		if allLow || allHigh {
			sameCluster++
		}
	}

	rate := float64(sameCluster) / float64(trials)
	const wantMinRate = 0.60
	if rate < wantMinRate {
		t.Fatalf("Shaw removal grouped all 3 removed customers into one cluster %.0f%% of %d trials, want at least %.0f%% (a uniformly random removal would only manage ~14%%)",
			rate*100, trials, wantMinRate*100)
	}
}

// TestCustomerRelatednessPrefersCloserPairs is a direct, deterministic check
// on the scoring function itself, independent of destroyShaw's randomized
// selection: with identical arrival time and demand, the geographically
// closer pair must score lower (more related).
func TestCustomerRelatednessPrefersCloserPairs(t *testing.T) {
	params := relatednessParams{maxDist: 100, maxTimeDiff: 1, maxDemandDiff: 1}

	near := Customer{X: 0, Y: 0, Demand: 5}
	nearNeighbor := Customer{X: 1, Y: 0, Demand: 5}
	far := Customer{X: 100, Y: 0, Demand: 5}

	closeScore := customerRelatedness(near, nearNeighbor, 0, 0, params)
	farScore := customerRelatedness(near, far, 0, 0, params)

	if closeScore >= farScore {
		t.Fatalf("customerRelatedness(near, nearNeighbor) = %.4f, customerRelatedness(near, far) = %.4f; want the closer pair to score strictly lower", closeScore, farScore)
	}
}
