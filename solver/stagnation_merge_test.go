package main

import "testing"

// TestMergeStagnationSubSolutionPolishesAcrossSeam sets up the exact scenario
// the stagnation intervention produces: subSol has already been locally
// optimized in isolation (as the real non-LKH path does via
// localSearchImprove before merging), so no further improvement is possible
// looking only at subSol's own routes. But once merged with untouchedRoutes,
// customer 2 (X=0,Y=5) is far cheaper served alongside untouched customer 1
// (X=0,Y=4) than alongside its subSol routemate 3 (X=50,Y=0) - a cross-route
// relocation only visible once the seam between untouched and re-solved
// routes is actually examined by local search.
func TestMergeStagnationSubSolutionPolishesAcrossSeam(t *testing.T) {
	customers := map[int]Customer{
		0: {ID: 0, X: 0, Y: 0, DueDate: 1000},
		1: {ID: 1, X: 0, Y: 4, DueDate: 1000},
		2: {ID: 2, X: 0, Y: 5, DueDate: 1000},
		3: {ID: 3, X: 50, Y: 0, DueDate: 1000},
	}
	depot := customers[0]

	untouchedRoutes := []Route{buildRoute(t, []int{1}, 1, customers, depot, 1e9)}
	subSolRoute := buildRoute(t, []int{2, 3}, 99, customers, depot, 1e9)
	subSol := Solution{Routes: []Route{subSolRoute}}
	recalculateSolutionMetrics(&subSol)

	// Confirm the fixture is honest: subSol can't be improved by local search
	// looking only at its own routes - the only improving move requires the
	// untouched route.
	polishedInIsolation := localSearchImprove(subSol, customers, depot, 1e9)
	if polishedInIsolation.TotalDistance < subSol.TotalDistance-1e-9 {
		t.Fatalf("test fixture invalid: subSol improved in isolation (%.4f -> %.4f), want no improvement possible without the untouched route", subSol.TotalDistance, polishedInIsolation.TotalDistance)
	}

	naiveConcatDistance := untouchedRoutes[0].Distance + subSol.TotalDistance

	merged := mergeStagnationSubSolution(untouchedRoutes, subSol, customers, depot, 1e9)

	if merged.TotalDistance >= naiveConcatDistance-1e-9 {
		t.Fatalf("mergeStagnationSubSolution() distance %.4f not better than naive concatenation %.4f - merge didn't polish across the seam", merged.TotalDistance, naiveConcatDistance)
	}

	gotCounts := customerIDCounts(merged.Routes)
	wantCounts := map[int]int{1: 1, 2: 1, 3: 1}
	assertSameCustomerMultiset(t, "mergeStagnationSubSolution", gotCounts, wantCounts)
}

// TestMergeStagnationSubSolutionReindexesVehicleIDs uses a fixture with no
// polishing opportunity (routes are already each other's local optimum) so
// the only thing left to check is that vehicle IDs come out contiguous
// starting at 1, regardless of what arbitrary IDs untouchedRoutes/subSol
// carried in.
func TestMergeStagnationSubSolutionReindexesVehicleIDs(t *testing.T) {
	customers := map[int]Customer{
		0: {ID: 0, X: 0, Y: 0, DueDate: 1000},
		1: {ID: 1, X: 10, Y: 0, DueDate: 1000},
		2: {ID: 2, X: -10, Y: 0, DueDate: 1000},
	}
	depot := customers[0]

	untouchedRoutes := []Route{buildRoute(t, []int{1}, 7, customers, depot, 1e9)}
	subSol := Solution{Routes: []Route{buildRoute(t, []int{2}, 42, customers, depot, 1e9)}}
	recalculateSolutionMetrics(&subSol)

	merged := mergeStagnationSubSolution(untouchedRoutes, subSol, customers, depot, 1e9)

	if len(merged.Routes) != 2 {
		t.Fatalf("mergeStagnationSubSolution() returned %d routes, want 2", len(merged.Routes))
	}
	for idx, r := range merged.Routes {
		if r.VehicleID != idx+1 {
			t.Fatalf("mergeStagnationSubSolution() route %d has VehicleID %d, want %d (contiguous reindex)", idx, r.VehicleID, idx+1)
		}
	}
}
