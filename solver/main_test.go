package main

import "testing"

// TestStagnationSolverRetriggersPeriodically reproduces the reported bug:
// the stagnation-solver intervention is expected to fire every llmThreshold
// iterations for as long as the global best solution stays unchanged (i.e.
// the search remains stagnated). It must not permanently disable itself
// after a single non-improving attempt.
func TestStagnationSolverRetriggersPeriodically(t *testing.T) {
	const llmThreshold = 20
	const totalIterations = 200

	stagnationCounter := 0
	fires := 0

	// Simulate `totalIterations` loop passes where the best solution never
	// improves (i.e. the solver is stuck at a plateau) - exactly mirroring
	// how main()'s loop increments stagnationCounter and resets it on fire.
	for iter := 1; iter <= totalIterations; iter++ {
		stagnationCounter++

		if shouldTriggerStagnationSolver(stagnationCounter, llmThreshold, totalIterations) {
			fires++
			stagnationCounter = 0
		}
	}

	// With a threshold of 20 over 200 stagnated iterations, the intervention
	// should be able to fire roughly totalIterations/llmThreshold times (10),
	// not just once.
	wantMinFires := 5
	if fires < wantMinFires {
		t.Fatalf("stagnation solver fired %d times over %d stagnated iterations (threshold %d); want at least %d - it should retrigger every ~%d iterations, not disable itself after the first non-improving call",
			fires, totalIterations, llmThreshold, wantMinFires, llmThreshold)
	}
}
