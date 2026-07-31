package main

import "testing"

// TestSortedPairOrdersRegardlessOfArgumentOrder confirms sortedPair produces
// the same key whichever order the two IDs are passed in - needed so the
// firing-cap logic recognizes the same edge as "the same edge" regardless of
// which endpoint happened to be FromID vs ToID.
func TestSortedPairOrdersRegardlessOfArgumentOrder(t *testing.T) {
	got1 := sortedPair(5, 2)
	got2 := sortedPair(2, 5)
	want := [2]int{2, 5}
	if got1 != want || got2 != want {
		t.Fatalf("sortedPair(5,2)=%v, sortedPair(2,5)=%v, want both %v", got1, got2, want)
	}
}

// customerCustomerEdgesFixture builds a solution with one route of 11
// customers plus a depot at the origin - depot legs are long (50+ units) and
// must never appear in customerCustomerEdges' output. Ten customers sit 1
// unit apart (nine short edges), then the eleventh sits 40 units past the
// last one: with nine short edges dominating the mean/stddev, that one long
// edge is a clear statistical outlier - not just the largest edge, which a
// fixture with too few edges could produce by coincidence.
func customerCustomerEdgesFixture(t *testing.T) (Solution, map[int]Customer) {
	t.Helper()
	customers := map[int]Customer{0: {ID: 0, X: 0, Y: 0, DueDate: 10000}}
	ids := make([]int, 0, 11)
	for i := 1; i <= 10; i++ {
		id := i
		customers[id] = Customer{ID: id, X: float64(49 + i), Y: 0, DueDate: 10000}
		ids = append(ids, id)
	}
	customers[11] = Customer{ID: 11, X: 99, Y: 0, DueDate: 10000} // 40 past customer 10 (x=59)
	ids = append(ids, 11)

	depot := customers[0]
	route := buildRoute(t, ids, 1, customers, depot, 1e9)
	sol := Solution{Routes: []Route{route}}
	recalculateSolutionMetrics(&sol)
	return sol, customers
}

func TestCustomerCustomerEdgesExcludesDepotLegs(t *testing.T) {
	sol, customers := customerCustomerEdgesFixture(t)

	edges := customerCustomerEdges(sol, customers)

	// 11 customers in one route -> 10 customer-to-customer edges, never the
	// depot legs (depot->1, 11->depot).
	if len(edges) != 10 {
		t.Fatalf("customerCustomerEdges() returned %d edges, want 10 (depot legs must be excluded): %+v", len(edges), edges)
	}
	for _, e := range edges {
		if e.FromID == 0 || e.ToID == 0 {
			t.Fatalf("customerCustomerEdges() included a depot-adjacent edge: %+v", e)
		}
	}
}

func TestDetectLongEdgeOutlierFlagsTheClearOutlier(t *testing.T) {
	sol, customers := customerCustomerEdgesFixture(t)

	edge, found := detectLongEdgeOutlier(sol, customers)
	if !found {
		t.Fatalf("detectLongEdgeOutlier() found=false, want true - edge 10-11 (40 units) should clear the threshold against edges of ~1-2 units")
	}
	if sortedPair(edge.FromID, edge.ToID) != sortedPair(10, 11) {
		t.Fatalf("detectLongEdgeOutlier() flagged %d-%d, want 10-11", edge.FromID, edge.ToID)
	}
}

func TestDetectLongEdgeOutlierNoOutlierWhenEdgesAreUniform(t *testing.T) {
	customers := map[int]Customer{
		0: {ID: 0, X: 0, Y: 0, DueDate: 10000},
		1: {ID: 1, X: 10, Y: 0, DueDate: 10000},
		2: {ID: 2, X: 20, Y: 0, DueDate: 10000},
		3: {ID: 3, X: 30, Y: 0, DueDate: 10000},
		4: {ID: 4, X: 40, Y: 0, DueDate: 10000},
	}
	depot := customers[0]
	route := buildRoute(t, []int{1, 2, 3, 4}, 1, customers, depot, 1e9)
	sol := Solution{Routes: []Route{route}}
	recalculateSolutionMetrics(&sol)

	_, found := detectLongEdgeOutlier(sol, customers)
	if found {
		t.Fatalf("detectLongEdgeOutlier() found=true on perfectly uniform edge lengths, want false")
	}
}

func TestDetectLongEdgeOutlierNotEnoughEdges(t *testing.T) {
	customers := map[int]Customer{
		0: {ID: 0, X: 0, Y: 0, DueDate: 10000},
		1: {ID: 1, X: 10, Y: 0, DueDate: 10000},
	}
	depot := customers[0]
	route := buildRoute(t, []int{1}, 1, customers, depot, 1e9)
	sol := Solution{Routes: []Route{route}}
	recalculateSolutionMetrics(&sol)

	_, found := detectLongEdgeOutlier(sol, customers)
	if found {
		t.Fatalf("detectLongEdgeOutlier() found=true with 0 customer-to-customer edges, want false")
	}
}

// TestDestroyShawSeededRemovesBothSeeds confirms the extracted growth loop
// keeps both explicitly seeded customers in the removed set (not just one),
// and still removes exactly k customers total.
func TestDestroyShawSeededRemovesBothSeeds(t *testing.T) {
	customers := twoClusterCustomers()
	depot := customers[0]
	capacity := 1000.0

	sol := Solution{Routes: []Route{
		buildRoute(t, []int{1, 2, 3, 4, 5, 6, 7, 8}, 1, customers, depot, capacity),
	}}
	recalculateSolutionMetrics(&sol)

	_, removed := destroyShawSeeded(sol, 3, customers, depot, []int{1, 5})

	if len(removed) != 3 {
		t.Fatalf("destroyShawSeeded() removed %d customers, want 3 (removed=%v)", len(removed), removed)
	}
	seen := map[int]bool{}
	for _, id := range removed {
		seen[id] = true
	}
	if !seen[1] || !seen[5] {
		t.Fatalf("destroyShawSeeded(seeds=[1,5]) removed=%v, want both seeds present", removed)
	}
}

// TestDestroyShawSeededClampsKUpToSeedCount confirms k is raised (never
// lowered) to at least len(seedIDs), so both seeds always survive into the
// result even if the caller passed a k smaller than the seed count.
func TestDestroyShawSeededClampsKUpToSeedCount(t *testing.T) {
	customers := twoClusterCustomers()
	depot := customers[0]
	capacity := 1000.0

	sol := Solution{Routes: []Route{
		buildRoute(t, []int{1, 2, 3, 4, 5, 6, 7, 8}, 1, customers, depot, capacity),
	}}
	recalculateSolutionMetrics(&sol)

	_, removed := destroyShawSeeded(sol, 1, customers, depot, []int{1, 5})

	if len(removed) != 2 {
		t.Fatalf("destroyShawSeeded(k=1, seeds=[1,5]) removed %d customers, want 2 (k raised to seed count)", len(removed))
	}
}

// TestDestroyShawSeededFallsBackWhenSeedsNotRouted confirms a defensive
// fallback to a random seed rather than removing nothing, if none of the
// requested seed IDs are actually routed in sol.
func TestDestroyShawSeededFallsBackWhenSeedsNotRouted(t *testing.T) {
	customers := twoClusterCustomers()
	depot := customers[0]
	capacity := 1000.0

	sol := Solution{Routes: []Route{
		buildRoute(t, []int{1, 2, 3, 4, 5, 6, 7, 8}, 1, customers, depot, capacity),
	}}
	recalculateSolutionMetrics(&sol)

	_, removed := destroyShawSeeded(sol, 2, customers, depot, []int{999})

	if len(removed) != 2 {
		t.Fatalf("destroyShawSeeded() with an unrouted seed removed %d customers, want 2 (fallback to a random seed)", len(removed))
	}
}

// TestLongEdgeShouldForceCapsAtOneAttemptPerEdge exercises the firing-cap
// policy directly: the same flagged edge gets exactly one forced
// intervention, then falls through to the roulette wheel on every
// subsequent iteration it's still flagged - it doesn't get starved forever,
// but it also doesn't dominate every iteration.
func TestLongEdgeShouldForceCapsAtOneAttemptPerEdge(t *testing.T) {
	key := sortedPair(3, 7)
	lastFlagged := [2]int{}
	firingCount := 0

	force, newLast, newCount := longEdgeShouldForce(true, key, lastFlagged, firingCount)
	if !force {
		t.Fatalf("longEdgeShouldForce() first sighting of a flagged edge should force, got force=false")
	}
	lastFlagged, firingCount = newLast, newCount

	force, newLast, newCount = longEdgeShouldForce(true, key, lastFlagged, firingCount)
	if force {
		t.Fatalf("longEdgeShouldForce() same edge flagged again after using its one attempt should NOT force, got force=true")
	}
	lastFlagged, firingCount = newLast, newCount

	// A different edge re-arms immediately with a fresh budget.
	otherKey := sortedPair(9, 12)
	force, _, _ = longEdgeShouldForce(true, otherKey, lastFlagged, firingCount)
	if !force {
		t.Fatalf("longEdgeShouldForce() a newly-flagged different edge should force immediately, got force=false")
	}
}

// TestLongEdgeShouldForceResetsWhenResolved confirms that once an edge stops
// being flagged at all, the tracking state resets - so if it recurs later
// it's treated as new, not as still using an old budget.
func TestLongEdgeShouldForceResetsWhenResolved(t *testing.T) {
	key := sortedPair(3, 7)

	force, lastFlagged, firingCount := longEdgeShouldForce(true, key, [2]int{}, 0)
	if !force {
		t.Fatalf("longEdgeShouldForce() first sighting should force")
	}

	// Not found this iteration - resets.
	_, lastFlagged, firingCount = longEdgeShouldForce(false, [2]int{}, lastFlagged, firingCount)
	if lastFlagged != ([2]int{}) || firingCount != 0 {
		t.Fatalf("longEdgeShouldForce() with found=false should reset tracking, got lastFlagged=%v firingCount=%d", lastFlagged, firingCount)
	}

	// Same edge flagged again after resolving - fresh budget, forces again.
	force, _, _ = longEdgeShouldForce(true, key, lastFlagged, firingCount)
	if !force {
		t.Fatalf("longEdgeShouldForce() a recurring edge after resolution should force again with a fresh budget")
	}
}
