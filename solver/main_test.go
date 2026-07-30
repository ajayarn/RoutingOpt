package main

import (
	"math"
	"testing"
)

// TestStagnationSolverRetriggersPeriodically reproduces the reported bug:
// the stagnation-solver intervention is expected to fire every
// stagnationThreshold iterations for as long as the global best solution
// stays unchanged (i.e. the search remains stagnated). It must not
// permanently disable itself after a single non-improving attempt.
func TestStagnationSolverRetriggersPeriodically(t *testing.T) {
	const stagnationThreshold = 20
	const totalIterations = 200

	stagnationCounter := 0
	fires := 0

	// Simulate `totalIterations` loop passes where the best solution never
	// improves (i.e. the solver is stuck at a plateau) - exactly mirroring
	// how main()'s loop increments stagnationCounter and resets it on fire.
	for iter := 1; iter <= totalIterations; iter++ {
		stagnationCounter++

		if shouldTriggerStagnationSolver(stagnationCounter, stagnationThreshold, totalIterations) {
			fires++
			stagnationCounter = 0
		}
	}

	// With a threshold of 20 over 200 stagnated iterations, the intervention
	// should be able to fire roughly totalIterations/stagnationThreshold
	// times (10), not just once.
	wantMinFires := 5
	if fires < wantMinFires {
		t.Fatalf("stagnation solver fired %d times over %d stagnated iterations (threshold %d); want at least %d - it should retrigger every ~%d iterations, not disable itself after the first non-improving call",
			fires, totalIterations, stagnationThreshold, wantMinFires, stagnationThreshold)
	}
}

func TestIsBetterSolutionFewerVehiclesWinsRegardlessOfDistance(t *testing.T) {
	fewerVehiclesLongerDistance := Solution{TotalVehicles: 2, TotalDistance: 5000}
	moreVehiclesShorterDistance := Solution{TotalVehicles: 3, TotalDistance: 100}

	if !isBetterSolution(fewerVehiclesLongerDistance, moreVehiclesShorterDistance) {
		t.Fatalf("isBetterSolution: a 2-vehicle/5000-distance solution should beat a 3-vehicle/100-distance one - vehicle count is hierarchical")
	}
	if isBetterSolution(moreVehiclesShorterDistance, fewerVehiclesLongerDistance) {
		t.Fatalf("isBetterSolution: a 3-vehicle solution must never beat a 2-vehicle one, no matter the distance")
	}
}

func TestIsBetterSolutionTiedVehiclesFallsBackToDistance(t *testing.T) {
	shorter := Solution{TotalVehicles: 10, TotalDistance: 800}
	longer := Solution{TotalVehicles: 10, TotalDistance: 900}

	if !isBetterSolution(shorter, longer) {
		t.Fatalf("isBetterSolution: with tied vehicle counts, the shorter-distance solution should win")
	}
	if isBetterSolution(longer, shorter) {
		t.Fatalf("isBetterSolution: with tied vehicle counts, the longer-distance solution should not win")
	}
	if isBetterSolution(shorter, shorter) {
		t.Fatalf("isBetterSolution: a solution must not be considered strictly better than an identical one")
	}
}

func TestSimulatedAnnealingAcceptRejectsAtZeroTemperature(t *testing.T) {
	// A temperature of 0 means the schedule has fully cooled - no worse
	// candidate should ever be accepted, regardless of how small delta or
	// roll are.
	if simulatedAnnealingAccept(0.001, 0, 0.0) {
		t.Fatalf("simulatedAnnealingAccept(delta=0.001, T=0, roll=0.0) = true, want false - zero temperature must reject every worse candidate")
	}
}

func TestSimulatedAnnealingAcceptRespectsRollAgainstProbability(t *testing.T) {
	// delta=10, T=10 -> probability = exp(-1) ~= 0.3679.
	delta, temperature := 10.0, 10.0
	wantProbability := math.Exp(-1)

	if !simulatedAnnealingAccept(delta, temperature, wantProbability-0.05) {
		t.Fatalf("simulatedAnnealingAccept(delta=%.1f, T=%.1f, roll=%.4f) = false, want true (roll is below the ~%.4f acceptance probability)", delta, temperature, wantProbability-0.05, wantProbability)
	}
	if simulatedAnnealingAccept(delta, temperature, wantProbability+0.05) {
		t.Fatalf("simulatedAnnealingAccept(delta=%.1f, T=%.1f, roll=%.4f) = true, want false (roll is above the ~%.4f acceptance probability)", delta, temperature, wantProbability+0.05, wantProbability)
	}
}

func TestSimulatedAnnealingAcceptCoolerTemperatureAcceptsLessOften(t *testing.T) {
	// Same delta and roll, only temperature changes: cooling the schedule
	// must never make an already-marginal candidate MORE likely to be
	// accepted.
	const delta = 10.0
	const roll = 0.30

	hot := simulatedAnnealingAccept(delta, 20.0, roll) // exp(-0.5) ~= 0.6065 -> accept
	cold := simulatedAnnealingAccept(delta, 2.0, roll) // exp(-5) ~= 0.0067 -> reject

	if !hot {
		t.Fatalf("simulatedAnnealingAccept(delta=%.1f, T=20, roll=%.2f) = false, want true", delta, roll)
	}
	if cold {
		t.Fatalf("simulatedAnnealingAccept(delta=%.1f, T=2, roll=%.2f) = true, want false - a cooler schedule should accept this delta less readily, not more", delta, roll)
	}
}
