package main

import "testing"

// TestMostRelatedRoutesPrefersTimeAndDemandOverPureDistance is the key
// behavioral test for reusing customerRelatedness at the route level: it
// constructs a case where the geographically NEAREST route to the seed is a
// poor route-relatedness match (very different arrival time and demand),
// while a farther route is an excellent match (near-identical arrival time
// and demand) - the exact case pure centroid-distance selection (what this
// replaced) gets wrong, since it would have picked the geographically
// closer route regardless of how differently-timed or -loaded it is.
//
// Routes are built as single-customer routes with forced waiting (a late
// readyTime on an otherwise depot-adjacent customer) so arrival time can be
// decoupled from geographic position - see the wide 4th "far" route, whose
// only job is to inflate maxDist so the seed-to-candidate distance
// differences become small relative to it, letting the time/demand terms
// actually swing the ranking (worked through by hand in the design notes
// for this test; see the PR/commit that added it for the arithmetic).
func TestMostRelatedRoutesPrefersTimeAndDemandOverPureDistance(t *testing.T) {
	depot := Customer{ID: 0, X: 0, Y: 0, Demand: 0, ReadyTime: 0, DueDate: 10000, ServiceTime: 0}
	customers := map[int]Customer{
		0: depot,
		// Seed route: near the depot, served early, light demand.
		1: {ID: 1, X: 5, Y: 0, Demand: 5, ReadyTime: 0, DueDate: 10000, ServiceTime: 0},
		// Geographically CLOSEST to the seed (dx=1), but forced to wait
		// until t=900 and carries a heavy demand - a poor relatedness match
		// despite the short distance.
		2: {ID: 2, X: 6, Y: 0, Demand: 50, ReadyTime: 900, DueDate: 10000, ServiceTime: 0},
		// Geographically farther from the seed (dx=2) than route 2, but
		// served at almost the same time with the same light demand - the
		// better relatedness match.
		3: {ID: 3, X: 7, Y: 0, Demand: 5, ReadyTime: 0, DueDate: 10000, ServiceTime: 0},
		// Far outlier, present only to inflate maxDist so routes 2 and 3's
		// small distance difference from route 1 doesn't dominate the score
		// on distance alone.
		4: {ID: 4, X: 50, Y: 0, Demand: 5, ReadyTime: 0, DueDate: 10000, ServiceTime: 0},
	}
	capacity := 1000.0

	sol := Solution{Routes: []Route{
		buildRoute(t, []int{1}, 1, customers, depot, capacity),
		buildRoute(t, []int{2}, 2, customers, depot, capacity),
		buildRoute(t, []int{3}, 3, customers, depot, capacity),
		buildRoute(t, []int{4}, 4, customers, depot, capacity),
	}}
	recalculateSolutionMetrics(&sol)

	centroids := routeCentroidsFor(sol, customers)
	params := routeRelatednessParams(centroids)

	seedIdx := -1
	for i, c := range centroids {
		if c.RouteID == 1 {
			seedIdx = i
		}
	}
	if seedIdx == -1 {
		t.Fatalf("route 1 not found in centroids")
	}

	got := mostRelatedRoutes(centroids, params, seedIdx, 2)
	if len(got) != 2 || got[0] != 1 {
		t.Fatalf("mostRelatedRoutes(seed=route1, count=2) = %v, want [1, X]", got)
	}

	if got[1] != 3 {
		t.Fatalf("mostRelatedRoutes picked route %d as most related to route 1, want route 3 "+
			"(similar arrival time and demand despite being geographically farther than route 2, "+
			"which is closer but arrives ~900 time units later with 10x the demand) - "+
			"got %v", got[1], got)
	}
}
