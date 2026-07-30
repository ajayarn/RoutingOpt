package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Customer struct {
	ID          int     `json:"id"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Demand      float64 `json:"demand"`
	ReadyTime   float64 `json:"readyTime"`
	DueDate     float64 `json:"dueDate"`
	ServiceTime float64 `json:"serviceTime"`
}

type Route struct {
	VehicleID      int             `json:"vehicleId"`
	CustomerIDs    []int           `json:"customerIds"`
	Distance       float64         `json:"distance"`
	Load           float64         `json:"load"`
	ArrivalTimes   map[int]float64 `json:"arrivalTimes"`
	WaitingTimes   map[int]float64 `json:"waitingTimes"`
	DepartureTimes map[int]float64 `json:"departureTimes"`
}

type Solution struct {
	Routes        []Route `json:"routes"`
	TotalDistance float64 `json:"totalDistance"`
	TotalVehicles int     `json:"totalVehicles"`
}

type ProgressMessage struct {
	Type              string  `json:"type"`
	Iteration         int     `json:"iteration,omitempty"`
	BestDistance      float64 `json:"bestDistance,omitempty"`
	BestVehicles      int     `json:"bestVehicles,omitempty"`
	Routes            []Route `json:"routes,omitempty"`
	ComputationTimeMs int64   `json:"computationTimeMs,omitempty"`
	Message           string  `json:"message,omitempty"`
}

// shouldTriggerStagnationSolver decides whether to invoke the stagnation-solver
// intervention this iteration: whenever the global best has gone unchanged for
// llmThreshold iterations. stagnationCounter is reset to 0 by the caller each
// time this fires (and each time a new global best is found), which is what
// throttles repeat firings - no additional gating is needed here.
func shouldTriggerStagnationSolver(stagnationCounter, llmThreshold, totalIterations int) bool {
	return llmThreshold > 0 && stagnationCounter >= llmThreshold && totalIterations >= llmThreshold
}

// chooseDestroyOperator maps a uniform random roll in [0, 1) to a destroy
// operator name using the 20% Route Elimination / 40% Worst / 40% Random
// split. Extracted as a pure function so the split itself is unit-testable
// without running a full solve.
func chooseDestroyOperator(roll float64) string {
	switch {
	case roll < 0.20:
		return "Route Elimination"
	case roll < 0.60:
		return "Worst Destroy"
	default:
		return "Random Destroy"
	}
}

func main() {
	filePath := flag.String("file", "", "Path to the Solomon instance file")
	iterations := flag.Int("iterations", 1000, "Number of LNS iterations")
	seed := flag.Int64("seed", 42, "Random seed")
	llmThreshold := flag.Int("llm-threshold", 20, "Iteration threshold for stagnation intervention")
	useLKH := flag.Bool("use-lkh", false, "Use the native LKH3 binary instead of the pure-Go I1+LNS sub-solver for stagnation sub-solving")
	flag.Parse()

	if *filePath == "" {
		sendError("File path is required")
		return
	}

	rand.Seed(*seed)

	startTime := time.Now()

	// 1. Parse Solomon File
	name, _, capacity, depot, customers, err := parseSolomonFile(*filePath)
	if err != nil {
		sendError(fmt.Sprintf("Failed to parse file: %v", err))
		return
	}

	sendStart(fmt.Sprintf("Loaded instance %s. Starting solver with %d iterations.", name, *iterations))

	// Create customer map for easy lookup
	customerMap := make(map[int]Customer)
	for _, c := range customers {
		customerMap[c.ID] = c
	}
	customerMap[depot.ID] = depot

	// 2. Build Initial Feasible Solution
	sol := buildInitialSolution(customers, depot, capacity, customerMap)
	if len(sol.Routes) == 0 {
		sendError("Failed to build a feasible initial solution")
		return
	}

	// 2a. Tighten the raw I1 construction with 2-opt/Or-opt before anything
	// else touches it - route-elimination attempts below succeed more often
	// against routes that aren't carrying distance/time slack the insertion
	// heuristic left behind.
	sol = localSearchImprove(sol, customerMap, depot, capacity)

	// 2b. Vehicle-minimization pre-phase: while the solution is still loose
	// (freshly constructed, not yet distance-optimized), aggressively try
	// to eliminate routes before the main distance-focused loop starts. See
	// docs/superpowers/specs/2026-07-27-route-elimination-operator-design.md.
	prePhaseBudget := int(0.10 * float64(*iterations))
	sol = runVehicleMinimizationPrePhase(sol, customerMap, depot, capacity, prePhaseBudget, startTime)
	sol = localSearchImprove(sol, customerMap, depot, capacity)

	// Send initial progress
	sendProgress(0, sol, startTime)

	bestSol := cloneSolution(sol)

	// Determine destroy sizes
	numCustomers := len(customers)
	minDestroy := int(math.Max(2, float64(numCustomers)*0.05))
	maxDestroy := int(math.Max(5, float64(numCustomers)*0.30))

	// Stagnation and adaptive LLM intervention tracking
	stagnationCounter := 0

	// 3. Solver Loop (LNS)
	for iter := 1; iter <= *iterations; iter++ {
		currentSol := cloneSolution(sol)

		// Decide how many customers to destroy
		k := rand.Intn(maxDestroy-minDestroy+1) + minDestroy

		// 1. Destroy + 2. Repair
		destroyType := chooseDestroyOperator(rand.Float64())
		var candidateSol Solution

		if destroyType == "Route Elimination" {
			eliminated, ok := tryRouteElimination(currentSol, customerMap, depot, capacity, 3)
			sendProgressLog(iter, bestSol, startTime, "LNS:CHOOSE", "Neighborhood '%s' attempted (success=%v)", destroyType, ok)
			if ok {
				candidateSol = eliminated
			} else {
				candidateSol = currentSol
			}
		} else {
			var removed []int
			var partialSol Solution
			if destroyType == "Worst Destroy" {
				partialSol, removed = destroyWorst(currentSol, k, customerMap, depot)
			} else {
				partialSol, removed = destroyRandom(currentSol, k, customerMap, depot)
			}
			sendProgressLog(iter, bestSol, startTime, "LNS:CHOOSE", "Neighborhood '%s' selected to remove %d customers: %v", destroyType, k, removed)
			candidateSol = repairGreedy(partialSol, removed, customerMap, depot, capacity)
			// Tighten every repaired candidate before it's judged for
			// acceptance - greedy insertion alone routinely leaves crossing
			// edges and out-of-order visits that 2-opt/Or-opt can remove for
			// free (Route Elimination's candidate is already tightened
			// inside tryRouteElimination itself).
			candidateSol = localSearchImprove(candidateSol, customerMap, depot, capacity)
		}

		// 3. Evaluate & Decide (Acceptance criterion)
		accept := false
		acceptReason := "candidate worse than current"

		if candidateSol.TotalVehicles < sol.TotalVehicles {
			accept = true
			acceptReason = "reduced fleet size"
		} else if candidateSol.TotalVehicles == sol.TotalVehicles && candidateSol.TotalDistance < sol.TotalDistance {
			accept = true
			acceptReason = "reduced route distance"
		}

		if !accept && destroyType == "Random Destroy" {
			accept = true
			acceptReason = "always accept Random Destroy to escape local optima"
		}

		improvedThisIter := false
		if accept {
			sol = candidateSol
			// Check if it is the absolute best found so far
			if sol.TotalVehicles < bestSol.TotalVehicles || (sol.TotalVehicles == bestSol.TotalVehicles && sol.TotalDistance < bestSol.TotalDistance) {
				improvedThisIter = true
				bestSol = cloneSolution(sol)
				sendProgressLog(iter, bestSol, startTime, "LNS:DECISION", "[NEW BEST] Found better global solution: %d vehicles, %.2f distance (Reason: %s)!", bestSol.TotalVehicles, bestSol.TotalDistance, acceptReason)
			} else {
				sendProgressLog(iter, bestSol, startTime, "LNS:ACCEPT", "[ACCEPTED] Candidate accepted: %d vehicles, %.2f distance (Reason: %s)", sol.TotalVehicles, sol.TotalDistance, acceptReason)
			}
		} else {
			sendProgressLog(iter, bestSol, startTime, "LNS:REJECT", "[REJECTED] Candidate rejected: %d vehicles, %.2f distance vs current %.2f (Reason: %s)", candidateSol.TotalVehicles, candidateSol.TotalDistance, sol.TotalDistance, acceptReason)
		}

		if improvedThisIter {
			stagnationCounter = 0
		} else {
			stagnationCounter++
		}

		// Smart Heuristic stagnation-solver intervention when we are stuck (stagnated for *llmThreshold iterations)
		triggerHeuristic := shouldTriggerStagnationSolver(stagnationCounter, *llmThreshold, *iterations)

		if triggerHeuristic {
			stagnationCounter = 0 // Reset stagnation counter since we are invoking heuristic now
			
			var history []DestructionAttempt
			improved := false
			maxAttempts := 3
			originalBestSol := cloneSolution(bestSol)

			for attempt := 1; attempt <= maxAttempts; attempt++ {
				// Kept at a fixed 2-3 routes across all attempts (no escalation to
				// 4-5) to keep subproblem sizes manageable for the LKH3 sub-solver.
				minDestroyRoutes := 2
				maxDestroyRoutes := 3

				// Cap destruction sizes by actual number of routes
				numRoutes := len(bestSol.Routes)
				if minDestroyRoutes > numRoutes {
					minDestroyRoutes = numRoutes
				}
				if maxDestroyRoutes > numRoutes {
					maxDestroyRoutes = numRoutes
				}
				if minDestroyRoutes < 1 {
					minDestroyRoutes = 1
				}
				if maxDestroyRoutes < minDestroyRoutes {
					maxDestroyRoutes = minDestroyRoutes
				}

				triggerCategory := "HEURISTIC:TRIGGER"
				if attempt > 1 {
					sendProgressLog(iter, bestSol, startTime, triggerCategory, "Stagnation solver Attempt %d: Retrying with alternate seed routes (%d-%d routes).", attempt, minDestroyRoutes, maxDestroyRoutes)
				} else {
					sendProgressLog(iter, bestSol, startTime, triggerCategory, "Stagnation detected (stagnated for %d iters). Invoking Smart Heuristic routing analyzer (suggesting %d-%d routes to destroy).", stagnationCounter, minDestroyRoutes, maxDestroyRoutes)
				}

				decisionCategory := "HEURISTIC:DECISION"
				finalDestroyIDs := selectStagnationRoutesHeuristically(bestSol, history, minDestroyRoutes, maxDestroyRoutes, customerMap, attempt)
				
				if len(finalDestroyIDs) > 0 {
					// Collect customer IDs of destroyed routes to add to history if it fails
					var destroyedCustIDs []int
					for _, r := range bestSol.Routes {
						for _, id := range finalDestroyIDs {
							if r.VehicleID == id {
								destroyedCustIDs = append(destroyedCustIDs, r.CustomerIDs...)
							}
						}
					}
					
					sendProgressLog(iter, bestSol, startTime, decisionCategory, "Selected overlapping/inefficient vehicles %v for destruction (containing %d Customers %v).", finalDestroyIDs, len(destroyedCustIDs), destroyedCustIDs)
					
					// Identify untouched routes vs destroyed routes
					var untouchedRoutes []Route
					var destroyedCustomers []Customer
					destroyIDMap := make(map[int]bool)
					for _, id := range finalDestroyIDs {
						destroyIDMap[id] = true
					}
					
					for _, r := range bestSol.Routes {
						if destroyIDMap[r.VehicleID] {
							// Add all customers in this route to destroyedCustomers
							for _, cID := range r.CustomerIDs {
								if c, exists := customerMap[cID]; exists {
									destroyedCustomers = append(destroyedCustomers, c)
								}
							}
						} else {
							untouchedRoutes = append(untouchedRoutes, r)
						}
					}
					
					if len(destroyedCustomers) > 0 {
						var subSol Solution
						lkhHandled := false

						if *useLKH {
							lkhStart := time.Now()
							var lkhSol *Solution

							if probeVehicles, probeOK := shouldProbeLKHMinusOne(attempt, len(finalDestroyIDs)); probeOK {
								sendProgressLog(iter, bestSol, startTime, "LKH:PROBE", "Attempt %d: probing whether %d vehicles suffice for %d removed customers (down from %d)...", attempt, probeVehicles, len(destroyedCustomers), len(finalDestroyIDs))
								lkhSol = invokeLKHSubSolver(destroyedCustomers, depot, capacity, customerMap, probeVehicles)
								if lkhSol != nil {
									sendProgressLog(iter, bestSol, startTime, "LKH:PROBE-SUCCESS", "Probe succeeded: %d vehicles sufficient (reduced from %d) - skipping the %d-vehicle fallback.", probeVehicles, len(finalDestroyIDs), len(finalDestroyIDs))
								} else {
									sendProgressLog(iter, bestSol, startTime, "LKH:PROBE-FAILED", "Probe failed: %d vehicles not sufficient - falling back to %d.", probeVehicles, len(finalDestroyIDs))
								}
							}

							if lkhSol == nil {
								sendProgressLog(iter, bestSol, startTime, "LKH:TRIGGER", "Attempt %d: Invoking LKH3 on %d removed customers (vehicles cap = %d, no timeout)...", attempt, len(destroyedCustomers), len(finalDestroyIDs))
								lkhSol = invokeLKHSubSolver(destroyedCustomers, depot, capacity, customerMap, len(finalDestroyIDs))
							}
							lkhElapsed := time.Since(lkhStart)

							if lkhSol != nil {
								subSol = *lkhSol
								lkhHandled = true
								sendProgressLog(iter, bestSol, startTime, "LKH:SUCCESS", "LKH3 sub-solve used (size=%d customers): %d vehicles, %.2f distance, took %v.", len(destroyedCustomers), subSol.TotalVehicles, subSol.TotalDistance, lkhElapsed)
							} else {
								sendProgressLog(iter, bestSol, startTime, "LKH:FALLBACK", "LKH3 sub-solve failed or returned an infeasible result (size=%d customers, took %v); falling back to the pure-Go sub-solver.", len(destroyedCustomers), lkhElapsed)
							}
						}

						if !lkhHandled {
							sendProgressLog(iter, bestSol, startTime, "HEURISTIC:SUB-SOLVER", "Attempt %d: Re-routing %d removed customers. Phase 1: Solomon I1 Sequential Insertion...", attempt, len(destroyedCustomers))

							// Re-solve with our approach: I1 insertion -> LNS (run on the subset)
							subSol = buildInitialSolution(destroyedCustomers, depot, capacity, customerMap)
							sendProgressLog(iter, bestSol, startTime, "HEURISTIC:SUB-SOLVER", "Phase 1 Complete. Initial subproblem routing: %d vehicles, %.2f distance.", subSol.TotalVehicles, subSol.TotalDistance)

							if len(subSol.Routes) > 0 {
								numSubCust := len(destroyedCustomers)
								minSubDestroy := int(math.Max(1, float64(numSubCust)*0.10))
								maxSubDestroy := int(math.Max(2, float64(numSubCust)*0.40))
								if maxSubDestroy < minSubDestroy {
									maxSubDestroy = minSubDestroy
								}

								sendProgressLog(iter, bestSol, startTime, "HEURISTIC:SUB-SOLVER", "Phase 2: Optimizing subproblem routing using LNS on subset for 50 sub-iterations (destroying %d-%d customers per sub-iter)...", minSubDestroy, maxSubDestroy)
								// Run LNS on this sub-solution for 50 sub-iterations
								subImprovements := 0
								for subIter := 1; subIter <= 50; subIter++ {
									currentSubSol := cloneSolution(subSol)
									subK := minSubDestroy
									if maxSubDestroy > minSubDestroy {
										subK = rand.Intn(maxSubDestroy-minSubDestroy+1) + minSubDestroy
									}

									var subRemoved []int
									var partialSubSol Solution
									if rand.Float64() < 0.5 {
										partialSubSol, subRemoved = destroyWorst(currentSubSol, subK, customerMap, depot)
									} else {
										partialSubSol, subRemoved = destroyRandom(currentSubSol, subK, customerMap, depot)
									}

									candidateSubSol := repairGreedy(partialSubSol, subRemoved, customerMap, depot, capacity)

									acceptSub := false
									if candidateSubSol.TotalVehicles < subSol.TotalVehicles {
										acceptSub = true
									} else if candidateSubSol.TotalVehicles == subSol.TotalVehicles && candidateSubSol.TotalDistance < subSol.TotalDistance {
										acceptSub = true
									}

									if acceptSub {
										subSol = candidateSubSol
										subImprovements++
									}
								}
								sendProgressLog(iter, bestSol, startTime, "HEURISTIC:SUB-SOLVER", "Phase 2 Complete. Subset LNS performed %d improvements. Final subset routing: %d vehicles, %.2f distance.", subImprovements, subSol.TotalVehicles, subSol.TotalDistance)
							}

							// subSol came out of buildInitialSolution + destroy/repair, neither
							// of which does any intra/inter-route tightening - polish it before
							// merging back into the full solution. Skipped for an LKH-sourced
							// subSol (lkhHandled) since LKH already searches this far more
							// thoroughly than a 2-opt/Or-opt pass over its output would add.
							if !lkhHandled {
								subSol = localSearchImprove(subSol, customerMap, depot, capacity)
							}
						}

						// Merge back
						var mergedRoutes []Route
						for _, r := range untouchedRoutes {
							mergedRoutes = append(mergedRoutes, r)
						}
						for _, r := range subSol.Routes {
							mergedRoutes = append(mergedRoutes, r)
						}
						
						// Re-index vehicle IDs
						for idx := range mergedRoutes {
							mergedRoutes[idx].VehicleID = idx + 1
						}
						
						mergedSol := Solution{Routes: mergedRoutes}
						recalculateSolutionMetrics(&mergedSol)
						
						sendProgressLog(iter, bestSol, startTime, "HEURISTIC:MERGE", "Merged subproblem routes back. Merged full candidate: %d vehicles, %.2f distance.", mergedSol.TotalVehicles, mergedSol.TotalDistance)
						
						// Check if this improved the pre-heuristic best solution
						if mergedSol.TotalVehicles < originalBestSol.TotalVehicles || (mergedSol.TotalVehicles == originalBestSol.TotalVehicles && mergedSol.TotalDistance < originalBestSol.TotalDistance) {
							// Successfully improved!
							sol = mergedSol
							bestSol = cloneSolution(mergedSol)
							improved = true
							sendProgressLog(iter, bestSol, startTime, "HEURISTIC:SUCCESS", "[SUCCESS] Attempt %d successfully improved solution! New best: %d vehicles, %.2f distance. Resuming global LNS.", attempt, bestSol.TotalVehicles, bestSol.TotalDistance)
							break
						} else {
							// Did not improve! Log and add to history, then retry
							sendProgressLog(iter, bestSol, startTime, "HEURISTIC:FAILURE", "[FAILED] Attempt %d did not improve upon best known solution (%.2f). Retrying...", attempt, originalBestSol.TotalDistance)
							history = append(history, DestructionAttempt{
								VehicleIDs: finalDestroyIDs,
							})
						}
					}
				} else {
					sendProgressLog(iter, bestSol, startTime, "HEURISTIC:FAILURE", "[FAILED] Attempt %d generated no valid vehicles. Retrying...", attempt)
					history = append(history, DestructionAttempt{
						VehicleIDs: finalDestroyIDs,
					})
				}
			}

			if !improved {
				sendProgressLog(iter, bestSol, startTime, "HEURISTIC:FAILURE", "All %d heuristic attempts completed without improvement. Reverting to original best (%.2f) and resuming global LNS...", maxAttempts, originalBestSol.TotalDistance)
				sol = cloneSolution(originalBestSol)
				bestSol = cloneSolution(originalBestSol)
			}
		}

		// Send progress updates
		if iter%50 == 0 || iter == 1 || iter == *iterations {
			sendProgress(iter, bestSol, startTime)
		}
	}

	// Send final results
	sendResult(bestSol, startTime, *iterations)
}

// Distance helper
func distance(c1, c2 Customer) float64 {
	return math.Sqrt(math.Pow(c1.X-c2.X, 2) + math.Pow(c1.Y-c2.Y, 2))
}

// Parse Solomon benchmark text format
func parseSolomonFile(path string) (string, int, float64, Customer, []Customer, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, 0, Customer{}, nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var name string
	var vehicleNumber int
	var capacity float64
	var depot Customer
	var customers []Customer

	state := 0 // 0: header, 1: vehicle, 2: customer-header, 3: customer-data

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Extract instance name from first line if empty
		if name == "" && state == 0 {
			name = line
			continue
		}

		if strings.Contains(line, "VEHICLE") {
			state = 1
			continue
		}
		if strings.Contains(line, "CUSTOMER") {
			state = 2
			continue
		}

		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}

		if state == 1 {
			// Expected: NUMBER CAPACITY, then on next line the values
			if fields[0] == "NUMBER" || fields[0] == "CAPACITY" {
				continue
			}
			num, err1 := strconv.Atoi(fields[0])
			capVal, err2 := strconv.ParseFloat(fields[1], 64)
			if err1 == nil && err2 == nil {
				vehicleNumber = num
				capacity = capVal
				state = 0 // back to header, waiting for CUSTOMER
			}
		} else if state == 2 {
			if strings.Contains(line, "CUST NO.") || strings.Contains(line, "XCOORD") {
				continue
			}
			state = 3 // start reading customer data
		}

		if state == 3 {
			if len(fields) < 7 {
				continue
			}
			id, err1 := strconv.Atoi(fields[0])
			x, err2 := strconv.ParseFloat(fields[1], 64)
			y, err3 := strconv.ParseFloat(fields[2], 64)
			demand, err4 := strconv.ParseFloat(fields[3], 64)
			ready, err5 := strconv.ParseFloat(fields[4], 64)
			due, err6 := strconv.ParseFloat(fields[5], 64)
			service, err7 := strconv.ParseFloat(fields[6], 64)

			if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil || err6 != nil || err7 != nil {
				continue
			}

			c := Customer{
				ID:          id,
				X:           x,
				Y:           y,
				Demand:      demand,
				ReadyTime:   ready,
				DueDate:     due,
				ServiceTime: service,
			}

			if id == 0 {
				depot = c
			} else {
				customers = append(customers, c)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return "", 0, 0, Customer{}, nil, err
	}

	return name, vehicleNumber, capacity, depot, customers, nil
}

// Solomon I1 parameters (Solomon 1987). c1 = alpha1*c11 + alpha2*c12 is the
// cost of inserting a customer between a specific (i,j) pair; c2 selects,
// among all unrouted customers' best insertion points for the CURRENT
// route, which one to actually insert. Fixed constants, not CLI flags,
// consistent with this file's existing style for internal tuning knobs
// (e.g. chooseDestroyOperator's 20/40/40 split).
const (
	i1Mu     = 1.0 // route-shape weight in c11 = d(i,u) + d(u,j) - mu*d(i,j)
	i1Alpha1 = 0.5 // weight on the distance term c11 within c1
	i1Alpha2 = 0.5 // weight on the time-shift term c12 within c1
	i1Lambda = 2.0 // depot-distance regret weight in c2 = lambda*d(depot,u) - c1
)

// buildInitialSolution constructs a feasible initial solution using
// Solomon's I1 sequential insertion heuristic (Solomon, 1987): routes are
// built one at a time from the full unrouted customer pool - each new
// route is seeded with the unrouted customer farthest from the depot, then
// filled by repeatedly selecting, among every unrouted customer's cheapest
// feasible insertion point in the CURRENT route (c1, minimized), whichever
// customer maximizes the depot-distance regret measure c2 - until no
// unrouted customer fits feasibly anywhere in the route, at which point
// the route closes and a new one begins. Does not cluster customers first:
// c1's distance/time-window terms and c2's regret term already encode the
// geographic and temporal locality clustering would otherwise approximate,
// and a customer that doesn't fit the current route can be picked up by
// ANY later route, not just one confined to a pre-assigned cluster.
func buildInitialSolution(customers []Customer, depot Customer, capacity float64, customerMap map[int]Customer) Solution {
	unrouted := make(map[int]Customer, len(customers))
	for _, c := range customers {
		unrouted[c.ID] = c
	}

	var routes []Route
	vehicleID := 1

	for len(unrouted) > 0 {
		unroutedIDs := sortedIDs(unrouted)

		seedID := selectSeedCustomer(unroutedIDs, unrouted, depot)
		delete(unrouted, seedID)
		routeCusts := []int{seedID}

		for {
			baseRoute, baseOK := calculateRouteDetails(routeCusts, customerMap, depot, capacity)
			if !baseOK {
				break // unreachable: routeCusts only grows via feasibility-checked insertion
			}

			bestCustID := -1
			bestPos := -1
			bestC2 := -math.MaxFloat64

			for _, id := range sortedIDs(unrouted) {
				u := unrouted[id]

				custBestC1 := math.MaxFloat64
				custBestPos := -1

				for pos := 0; pos <= len(routeCusts); pos++ {
					candIDs := make([]int, len(routeCusts)+1)
					copy(candIDs[:pos], routeCusts[:pos])
					candIDs[pos] = id
					copy(candIDs[pos+1:], routeCusts[pos:])

					candRoute, feasible := calculateRouteDetails(candIDs, customerMap, depot, capacity)
					if !feasible {
						continue
					}

					c1 := i1c1(routeCusts, pos, u, customerMap, depot, baseRoute, candRoute)
					if c1 < custBestC1 {
						custBestC1 = c1
						custBestPos = pos
					}
				}

				if custBestPos == -1 {
					continue
				}

				c2 := i1Lambda*distance(depot, u) - custBestC1
				if c2 > bestC2 || (c2 == bestC2 && id < bestCustID) {
					bestC2 = c2
					bestCustID = id
					bestPos = custBestPos
				}
			}

			if bestCustID == -1 {
				break
			}

			routeCusts = append(routeCusts, 0)
			copy(routeCusts[bestPos+1:], routeCusts[bestPos:])
			routeCusts[bestPos] = bestCustID
			delete(unrouted, bestCustID)
		}

		rDetails, _ := calculateRouteDetails(routeCusts, customerMap, depot, capacity)
		rDetails.VehicleID = vehicleID
		routes = append(routes, rDetails)
		vehicleID++
	}

	sol := Solution{Routes: routes}
	recalculateSolutionMetrics(&sol)
	return sol
}

// sortedIDs returns a map's keys in ascending order, giving deterministic
// iteration order and deterministic tie-breaks wherever this file would
// otherwise range over a map directly.
func sortedIDs(m map[int]Customer) []int {
	ids := make([]int, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

// selectSeedCustomer picks the unrouted customer farthest from the depot
// (ties broken by lowest ID) to start a new route - the standard I1
// convention, consistent with c2's own depot-distance regret term.
func selectSeedCustomer(unroutedIDs []int, unrouted map[int]Customer, depot Customer) int {
	bestID := unroutedIDs[0]
	bestDist := -1.0
	for _, id := range unroutedIDs {
		d := distance(depot, unrouted[id])
		if d > bestDist || (d == bestDist && id < bestID) {
			bestDist = d
			bestID = id
		}
	}
	return bestID
}

// serviceStartAt returns the service-start time at the customer occupying
// position succIdx in a route, given that route's precomputed details. If
// succIdx is out of range, the "successor" is the depot on the return leg
// - calculateRouteDetails never records the depot in its time maps, so
// that case is derived explicitly from the last customer's departure time.
func serviceStartAt(r Route, customerIDs []int, succIdx int, customers map[int]Customer, depot Customer) float64 {
	if succIdx < len(customerIDs) {
		succID := customerIDs[succIdx]
		return r.ArrivalTimes[succID] + r.WaitingTimes[succID]
	}
	if len(customerIDs) == 0 {
		return 0
	}
	lastID := customerIDs[len(customerIDs)-1]
	return r.DepartureTimes[lastID] + distance(customers[lastID], depot)
}

// i1c1 computes Solomon's c1(i,u,j) cost for inserting customer u at
// position pos of routeCusts. baseRoute is routeCusts' precomputed details
// (before insertion); candRoute is the already feasibility-checked route
// WITH u inserted - both reused as-is, no recomputation.
func i1c1(routeCusts []int, pos int, u Customer, customers map[int]Customer, depot Customer, baseRoute, candRoute Route) float64 {
	prev := depot
	if pos > 0 {
		prev = customers[routeCusts[pos-1]]
	}
	next := depot
	if pos < len(routeCusts) {
		next = customers[routeCusts[pos]]
	}

	c11 := distance(prev, u) + distance(u, next) - i1Mu*distance(prev, next)
	c12 := serviceStartAt(candRoute, candRoute.CustomerIDs, pos+1, customers, depot) -
		serviceStartAt(baseRoute, routeCusts, pos, customers, depot)

	return i1Alpha1*c11 + i1Alpha2*c12
}

// Calculate precise details of a route including distances, arrivals, service, wait times and feasibility
func calculateRouteDetails(customerIDs []int, customers map[int]Customer, depot Customer, capacity float64) (Route, bool) {
	route := Route{
		CustomerIDs:    customerIDs,
		ArrivalTimes:   make(map[int]float64),
		WaitingTimes:   make(map[int]float64),
		DepartureTimes: make(map[int]float64),
	}

	if len(customerIDs) == 0 {
		return route, true
	}

	currentLoad := 0.0
	currentTime := 0.0
	currentX := depot.X
	currentY := depot.Y
	totalDistance := 0.0

	for _, cID := range customerIDs {
		cust := customers[cID]
		currentLoad += cust.Demand
		if currentLoad > capacity {
			return route, false // Capacity exceeded
		}

		dist := math.Sqrt(math.Pow(currentX-cust.X, 2) + math.Pow(currentY-cust.Y, 2))
		totalDistance += dist
		arrivalTime := currentTime + dist

		waitingTime := 0.0
		if arrivalTime < cust.ReadyTime {
			waitingTime = cust.ReadyTime - arrivalTime
		}

		startServiceTime := arrivalTime + waitingTime
		if startServiceTime > cust.DueDate {
			return route, false // Time window violated
		}

		departureTime := startServiceTime + cust.ServiceTime

		route.ArrivalTimes[cID] = arrivalTime
		route.WaitingTimes[cID] = waitingTime
		route.DepartureTimes[cID] = departureTime

		currentTime = departureTime
		currentX = cust.X
		currentY = cust.Y
	}

	// Back to depot
	distToDepot := math.Sqrt(math.Pow(currentX-depot.X, 2) + math.Pow(currentY-depot.Y, 2))
	totalDistance += distToDepot
	arrivalTimeAtDepot := currentTime + distToDepot
	if arrivalTimeAtDepot > depot.DueDate {
		return route, false // Depot return time window violated
	}

	route.Distance = totalDistance
	route.Load = currentLoad
	return route, true
}

func recalculateSolutionMetrics(sol *Solution) {
	sol.TotalDistance = 0
	sol.TotalVehicles = len(sol.Routes)

	for i := range sol.Routes {
		sol.TotalDistance += sol.Routes[i].Distance
	}
}

// minVehiclesLowerBound is a closed-form LOWER bound on feasible vehicle
// count from capacity alone (bin-packing bound) - cheap to compute, no
// search required. Time windows can only push the true minimum UP from
// this floor, never below it, so once a solution's vehicle count equals
// this bound, further route-elimination attempts are provably pointless.
func minVehiclesLowerBound(customers map[int]Customer, capacity float64) int {
	if capacity <= 0 {
		return 1
	}

	totalDemand := 0.0
	for _, c := range customers {
		totalDemand += c.Demand
	}

	bound := int(math.Ceil(totalDemand / capacity))
	if bound < 1 {
		bound = 1
	}
	return bound
}

// selectWeakestRoutes ranks route indices ascending by "how easy this route
// is to eliminate" - fewest customers first (fewest things needing a new
// home), tie-broken by lowest total demand (most likely to fit into other
// routes' remaining capacity).
func selectWeakestRoutes(sol Solution, customers map[int]Customer) []int {
	type routeWeight struct {
		idx    int
		count  int
		demand float64
	}

	weights := make([]routeWeight, len(sol.Routes))
	for i, r := range sol.Routes {
		d := 0.0
		for _, cID := range r.CustomerIDs {
			d += customers[cID].Demand
		}
		weights[i] = routeWeight{idx: i, count: len(r.CustomerIDs), demand: d}
	}

	sort.Slice(weights, func(i, j int) bool {
		if weights[i].count != weights[j].count {
			return weights[i].count < weights[j].count
		}
		return weights[i].demand < weights[j].demand
	})

	ranked := make([]int, len(weights))
	for i, w := range weights {
		ranked[i] = w.idx
	}
	return ranked
}

// LNS Destroy: Remove worst-performing customers
func destroyWorst(sol Solution, k int, customers map[int]Customer, depot Customer) (Solution, []int) {
	type CostRecord struct {
		custID   int
		routeIdx int
		pos      int
		cost     float64
	}

	var costs []CostRecord

	for rIdx, r := range sol.Routes {
		if len(r.CustomerIDs) <= 1 {
			for idx, cID := range r.CustomerIDs {
				costs = append(costs, CostRecord{custID: cID, routeIdx: rIdx, pos: idx, cost: r.Distance})
			}
			continue
		}

		for idx, cID := range r.CustomerIDs {
			// Cost is difference in distance when customer is removed
			subset := make([]int, 0, len(r.CustomerIDs)-1)
			for i, v := range r.CustomerIDs {
				if i != idx {
					subset = append(subset, v)
				}
			}
			details, _ := calculateRouteDetails(subset, customers, depot, 1e9)
			costDiff := r.Distance - details.Distance
			costs = append(costs, CostRecord{custID: cID, routeIdx: rIdx, pos: idx, cost: costDiff})
		}
	}

	// Remove k worst
	removed := make([]int, 0, k)
	removedSet := make(map[int]bool)

	for len(removed) < k && len(costs) > 0 {
		// Sort costs, but pick with some randomized noise to escape local minima
		// Find index with highest cost (possibly randomized)
		bestIdx := 0
		maxCost := -1e9
		for i, record := range costs {
			if removedSet[record.custID] {
				continue
			}
			// Add noise to cost
			noise := rand.Float64() * 5.0
			score := record.cost + noise
			if score > maxCost {
				maxCost = score
				bestIdx = i
			}
		}

		cID := costs[bestIdx].custID
		removed = append(removed, cID)
		removedSet[cID] = true
	}

	// Re-construct routes without removed customers
	var newRoutes []Route
	for _, r := range sol.Routes {
		var activeIDs []int
		for _, cID := range r.CustomerIDs {
			if !removedSet[cID] {
				activeIDs = append(activeIDs, cID)
			}
		}

		if len(activeIDs) > 0 {
			rDetails, _ := calculateRouteDetails(activeIDs, customers, depot, 1e9)
			rDetails.VehicleID = r.VehicleID
			newRoutes = append(newRoutes, rDetails)
		}
	}

	partialSol := Solution{Routes: newRoutes}
	recalculateSolutionMetrics(&partialSol)
	return partialSol, removed
}

// LNS Destroy: Random Removal
func destroyRandom(sol Solution, k int, customers map[int]Customer, depot Customer) (Solution, []int) {
	// Collect all routed customer IDs
	var allCustIDs []int
	for _, r := range sol.Routes {
		allCustIDs = append(allCustIDs, r.CustomerIDs...)
	}

	if len(allCustIDs) == 0 {
		return sol, nil
	}

	rand.Shuffle(len(allCustIDs), func(i, j int) {
		allCustIDs[i], allCustIDs[j] = allCustIDs[j], allCustIDs[i]
	})

	if k > len(allCustIDs) {
		k = len(allCustIDs)
	}

	removed := allCustIDs[:k]
	removedSet := make(map[int]bool)
	for _, id := range removed {
		removedSet[id] = true
	}

	// Build new routes without removed customers
	var newRoutes []Route
	for _, r := range sol.Routes {
		var activeIDs []int
		for _, cID := range r.CustomerIDs {
			if !removedSet[cID] {
				activeIDs = append(activeIDs, cID)
			}
		}

		if len(activeIDs) > 0 {
			// Calculate precise route details with real customer coords
			rDetails, _ := calculateRouteDetails(activeIDs, customers, depot, 1e9)
			rDetails.VehicleID = r.VehicleID
			newRoutes = append(newRoutes, rDetails)
		}
	}

	partialSol := Solution{Routes: newRoutes}
	recalculateSolutionMetrics(&partialSol)
	return partialSol, removed
}

// destroyRouteElimination removes the entire route at routeIdx (not a
// random subset) and returns the remaining solution plus every customer ID
// that was evicted. Same (Solution, []int) shape as
// destroyWorst/destroyRandom, but always empties one whole route rather
// than a random k customers scattered across many routes.
//
// Builds the retained routes from cloneSolution(sol) rather than copying
// sol.Routes directly: a plain range-copy only copies each Route struct by
// value, but CustomerIDs (and the time maps) are reference types, so the
// copy would still alias sol's original backing arrays. Since routes are
// built incrementally via single-element appends elsewhere in this file,
// their CustomerIDs slices routinely end up with spare capacity (Go's
// append growth strategy over-allocates) - a later in-place append onto an
// aliased slice (as repairGreedy's insertion does) can silently corrupt
// sol's original data even though sol itself is never directly assigned
// to. cloneSolution already deep-copies every route's CustomerIDs and time
// maps, so starting from it guarantees the caller's sol stays pristine
// across repeated destroy/repair attempts against it (see tryRouteElimination,
// Task 7, which relies on exactly this to retry against the same original
// sol when one attempt fails).
func destroyRouteElimination(sol Solution, routeIdx int) (Solution, []int) {
	removed := make([]int, len(sol.Routes[routeIdx].CustomerIDs))
	copy(removed, sol.Routes[routeIdx].CustomerIDs)

	cloned := cloneSolution(sol)
	newRoutes := make([]Route, 0, len(cloned.Routes)-1)
	for i, r := range cloned.Routes {
		if i != routeIdx {
			newRoutes = append(newRoutes, r)
		}
	}

	partialSol := Solution{Routes: newRoutes}
	recalculateSolutionMetrics(&partialSol)
	return partialSol, removed
}

// findBestInsertion searches every (route, position) pair across routes for
// the cheapest feasible place to insert customer cID, and also counts how
// many positions are feasible in total (slotCount) - used by
// repairGreedyNoNewRoute's most-constrained-first ordering. Shared by
// repairGreedy and repairGreedyNoNewRoute so this insertion-cost search
// exists in exactly one place.
func findBestInsertion(routes []Route, cID int, customers map[int]Customer, depot Customer, capacity float64) (routeIdx int, pos int, cost float64, slotCount int, feasible bool) {
	routeIdx = -1
	pos = -1
	cost = 1e9

	for rIdx, r := range routes {
		for p := 0; p <= len(r.CustomerIDs); p++ {
			testRoute := make([]int, len(r.CustomerIDs)+1)
			copy(testRoute[:p], r.CustomerIDs[:p])
			testRoute[p] = cID
			copy(testRoute[p+1:], r.CustomerIDs[p:])

			rDetails, ok := calculateRouteDetails(testRoute, customers, depot, capacity)
			if ok {
				slotCount++
				c := rDetails.Distance - r.Distance
				if c < cost {
					cost = c
					routeIdx = rIdx
					pos = p
					feasible = true
				}
			}
		}
	}

	return routeIdx, pos, cost, slotCount, feasible
}

// LNS Repair: Greedy Insertion with Best Fit Time-Window Feasibility
func repairGreedy(sol Solution, removed []int, customers map[int]Customer, depot Customer, capacity float64) Solution {
	// Shuffle removed list to avoid order bias
	rand.Shuffle(len(removed), func(i, j int) {
		removed[i], removed[j] = removed[j], removed[i]
	})

	for _, cID := range removed {
		bestRouteIdx, bestPos, _, _, feasible := findBestInsertion(sol.Routes, cID, customers, depot, capacity)

		// Insert into existing route if found
		if feasible {
			r := &sol.Routes[bestRouteIdx]
			r.CustomerIDs = append(r.CustomerIDs, 0)
			copy(r.CustomerIDs[bestPos+1:], r.CustomerIDs[bestPos:])
			r.CustomerIDs[bestPos] = cID

			rDetails, _ := calculateRouteDetails(r.CustomerIDs, customers, depot, capacity)
			rDetails.VehicleID = r.VehicleID
			sol.Routes[bestRouteIdx] = rDetails
		} else {
			// Feasible spot not found in any existing routes, must create a new route
			newRouteCusts := []int{cID}
			rDetails, _ := calculateRouteDetails(newRouteCusts, customers, depot, capacity)
			rDetails.VehicleID = len(sol.Routes) + 1
			sol.Routes = append(sol.Routes, rDetails)
		}

		recalculateSolutionMetrics(&sol)
	}

	// Re-index Vehicle IDs
	for idx := range sol.Routes {
		sol.Routes[idx].VehicleID = idx + 1
	}

	recalculateSolutionMetrics(&sol)
	return sol
}

// repairGreedyNoNewRoute attempts to reinsert every customer in `removed`
// into sol's existing routes only - it may never open a new route. Returns
// ok=false (sol - the original, unmodified argument - returned, no partial
// commit) if any customer cannot be placed feasibly.
//
// Insertion order is most-constrained-first, recomputed dynamically before
// each insertion: whichever not-yet-placed customer currently has the
// fewest feasible (route, position) slots (findBestInsertion's slotCount)
// goes next. This matters because inserting "easy" customers first can
// consume the capacity/time slack a "hard" customer needed - exactly why
// repairGreedy's random insertion order almost never manages a full-route
// reinsertion.
//
// All work happens on a `working` clone, never on `sol.Routes` directly.
// sol is passed by value, but Solution.Routes is a slice - a struct copy
// only copies the slice header, not its backing array, so writing through
// sol.Routes[i] would still mutate the same backing array the caller's own
// Solution owns. That matters across more than one successful insertion in
// the same call: if customer A is placed into an existing route and only
// later does customer B turn out to have nowhere feasible left, returning
// early must not have already committed A's insertion into anything the
// caller can still see - not the input, and not a partially-repaired
// return value either. Operating on `working` throughout, and only
// returning it once every customer in `removed` has been placed, gives
// both guarantees at once (see
// TestRepairGreedyNoNewRouteDoesNotPartiallyCommitOnLaterFailure).
func repairGreedyNoNewRoute(sol Solution, removed []int, customers map[int]Customer, depot Customer, capacity float64) (Solution, bool) {
	working := cloneSolution(sol)

	remaining := make(map[int]bool, len(removed))
	for _, cID := range removed {
		remaining[cID] = true
	}

	for len(remaining) > 0 {
		bestCustID := -1
		bestRouteIdx := -1
		bestPos := -1
		bestSlotCount := -1

		for cID := range remaining {
			routeIdx, pos, _, slotCount, feasible := findBestInsertion(working.Routes, cID, customers, depot, capacity)
			if !feasible {
				// This customer has nowhere feasible to go in the existing
				// routes - the whole elimination attempt fails. Every
				// insertion made so far in this call went into `working`,
				// never into `sol`, so `sol` (the caller's original,
				// returned here unchanged) was never written through - see
				// TestRepairGreedyNoNewRouteFailsWithoutMutatingInput and
				// TestRepairGreedyNoNewRouteDoesNotPartiallyCommitOnLaterFailure.
				return sol, false
			}

			if bestSlotCount == -1 || slotCount < bestSlotCount {
				bestSlotCount = slotCount
				bestCustID = cID
				bestRouteIdx = routeIdx
				bestPos = pos
			}
		}

		r := &working.Routes[bestRouteIdx]
		newIDs := make([]int, len(r.CustomerIDs)+1)
		copy(newIDs[:bestPos], r.CustomerIDs[:bestPos])
		newIDs[bestPos] = bestCustID
		copy(newIDs[bestPos+1:], r.CustomerIDs[bestPos:])

		rDetails, _ := calculateRouteDetails(newIDs, customers, depot, capacity)
		rDetails.VehicleID = r.VehicleID
		// Reshape the route with 2-opt right after this single insertion,
		// not just once at the very end: a route left in whatever order
		// greedy insertion happened to build can be so time-window-tight
		// that the NEXT customer has nowhere feasible to go, even though a
		// reordered version of the same route would have room. Measured on
		// R204: without this, route elimination could never get below 3
		// vehicles (200/200 consecutive attempts failed even after adding
		// the retry loop below); with it, insertion has a chance to
		// discover the slack 2-opt would have found anyway, before it's
		// needed for the next customer rather than after.
		rDetails = twoOptRoute(rDetails, customers, depot, capacity)
		working.Routes[bestRouteIdx] = rDetails

		delete(remaining, bestCustID)
	}

	for idx := range working.Routes {
		working.Routes[idx].VehicleID = idx + 1
	}
	recalculateSolutionMetrics(&working)
	return working, true
}

// reversedSegment returns a copy of ids with the [i, j] (inclusive) slice
// reversed - the core move of 2-opt.
func reversedSegment(ids []int, i, j int) []int {
	out := make([]int, len(ids))
	copy(out, ids)
	for lo, hi := i, j; lo < hi; lo, hi = lo+1, hi-1 {
		out[lo], out[hi] = out[hi], out[lo]
	}
	return out
}

// twoOptRoute repeatedly applies the best-improving 2-opt move (reversing a
// contiguous segment of the route) until a full pass over every segment
// finds no further improvement. Reversing a segment changes visit order,
// which time windows can turn infeasible even when the raw distance
// improves - so every candidate is re-validated via calculateRouteDetails
// rather than accepted on the distance delta alone. This is what the
// solver was missing entirely before: destroy/repair only ever appended
// customers at their cheapest insertion point, with nothing to untangle a
// route afterward.
func twoOptRoute(route Route, customers map[int]Customer, depot Customer, capacity float64) Route {
	vehicleID := route.VehicleID
	ids := route.CustomerIDs

	improved := true
	for improved {
		improved = false
		n := len(ids)
		for i := 0; i < n-1; i++ {
			for j := i + 1; j < n; j++ {
				candidate := reversedSegment(ids, i, j)
				details, ok := calculateRouteDetails(candidate, customers, depot, capacity)
				if ok && details.Distance < route.Distance-1e-9 {
					ids = candidate
					route = details
					improved = true
				}
			}
		}
	}

	route.VehicleID = vehicleID
	return route
}

// twoOptImproveSolution applies twoOptRoute independently to every route in
// sol and recomputes solution-level totals. Vehicle count is unaffected -
// this only reorders customers within each existing route, never moves one
// across routes (that's orOptImproveSolution's job).
func twoOptImproveSolution(sol Solution, customers map[int]Customer, depot Customer, capacity float64) Solution {
	improved := cloneSolution(sol)
	for i := range improved.Routes {
		improved.Routes[i] = twoOptRoute(improved.Routes[i], customers, depot, capacity)
	}
	recalculateSolutionMetrics(&improved)
	return improved
}

// orOptImproveSolution repeatedly looks for a single customer whose best
// feasible reinsertion point (via findBestInsertion, searched across every
// route including its own current one) is cheaper than leaving it where it
// is, and relocates it there. Unlike twoOptRoute, this can move a customer
// into a completely different route - the "cross-route relocate" that
// DESIGN.md notes the TS engine has and the Go engine, until now, didn't.
// Runs to convergence (a full pass with no improving move) or maxPasses
// sweeps, whichever comes first: with time windows, one relocation can
// occasionally re-open a move that looked unprofitable earlier in the same
// pass, so a single pass isn't always enough to reach a local optimum.
func orOptImproveSolution(sol Solution, customers map[int]Customer, depot Customer, capacity float64, maxPasses int) Solution {
	improved := cloneSolution(sol)

	for pass := 0; pass < maxPasses; pass++ {
		anyImprovement := false

		var allCustIDs []int
		for _, r := range improved.Routes {
			allCustIDs = append(allCustIDs, r.CustomerIDs...)
		}

		for _, cID := range allCustIDs {
			fromRouteIdx := -1
			for rIdx, r := range improved.Routes {
				for _, id := range r.CustomerIDs {
					if id == cID {
						fromRouteIdx = rIdx
						break
					}
				}
				if fromRouteIdx != -1 {
					break
				}
			}
			if fromRouteIdx == -1 {
				continue // shouldn't happen - defensive only
			}

			originalRoute := improved.Routes[fromRouteIdx]
			withoutIDs := make([]int, 0, len(originalRoute.CustomerIDs)-1)
			for _, id := range originalRoute.CustomerIDs {
				if id != cID {
					withoutIDs = append(withoutIDs, id)
				}
			}

			withoutDetails, ok := calculateRouteDetails(withoutIDs, customers, depot, capacity)
			if !ok {
				continue // removing a customer can't break feasibility; defensive only
			}
			removalSavings := originalRoute.Distance - withoutDetails.Distance

			trial := make([]Route, len(improved.Routes))
			copy(trial, improved.Routes)
			withoutDetails.VehicleID = originalRoute.VehicleID
			trial[fromRouteIdx] = withoutDetails

			bestRouteIdx, bestPos, insCost, _, feasible := findBestInsertion(trial, cID, customers, depot, capacity)
			if !feasible || insCost >= removalSavings-1e-9 {
				continue // no feasible or no net-improving relocation
			}

			r := &trial[bestRouteIdx]
			newIDs := make([]int, len(r.CustomerIDs)+1)
			copy(newIDs[:bestPos], r.CustomerIDs[:bestPos])
			newIDs[bestPos] = cID
			copy(newIDs[bestPos+1:], r.CustomerIDs[bestPos:])
			rDetails, _ := calculateRouteDetails(newIDs, customers, depot, capacity)
			rDetails.VehicleID = r.VehicleID
			trial[bestRouteIdx] = rDetails

			improved.Routes = trial
			anyImprovement = true
		}

		recalculateSolutionMetrics(&improved)
		if !anyImprovement {
			break
		}
	}

	// Relocating every customer out of a route (e.g. because merging into
	// one bigger route was cheaper than keeping two) leaves that route with
	// zero customers - recalculateSolutionMetrics still counts it as a
	// vehicle (TotalVehicles = len(Routes)), which would silently inflate
	// the solution's primary objective. Drop empty routes and reindex
	// before handing the result back; this also means Or-opt can discover
	// route elimination as a side effect, not just distance improvements.
	nonEmpty := make([]Route, 0, len(improved.Routes))
	for _, r := range improved.Routes {
		if len(r.CustomerIDs) > 0 {
			nonEmpty = append(nonEmpty, r)
		}
	}
	for idx := range nonEmpty {
		nonEmpty[idx].VehicleID = idx + 1
	}
	improved.Routes = nonEmpty
	recalculateSolutionMetrics(&improved)

	return improved
}

// localSearchImprove alternates twoOptRoute (intra-route reordering) and
// orOptImproveSolution (cross-route relocation) until a full round of both
// yields no further distance improvement, or maxRounds is hit. Alternating
// rather than running either just once matters because each can re-open
// opportunities for the other: an Or-opt relocation can leave a route in a
// shape 2-opt can now untangle further, and vice versa.
func localSearchImprove(sol Solution, customers map[int]Customer, depot Customer, capacity float64) Solution {
	improved := sol
	const maxRounds = 5
	for round := 0; round < maxRounds; round++ {
		before := improved.TotalDistance
		improved = twoOptImproveSolution(improved, customers, depot, capacity)
		improved = orOptImproveSolution(improved, customers, depot, capacity, 3)
		if improved.TotalDistance >= before-1e-6 {
			break
		}
	}
	return improved
}

// routeEliminationShuffleBudget bounds how many "yank some survivors back
// out too" retries eliminateOneRoute makes after a direct one-shot
// reinsertion fails. See eliminateOneRoute for why this is needed at all.
const routeEliminationShuffleBudget = 5

// eliminateOneRoute tries to fold routeIdx's customers into the OTHER
// routes of sol without opening a new one. It first tries the direct,
// cheap path: reinsert the evicted customers into the survivors exactly as
// they are (repairGreedyNoNewRoute). On R204 (100 customers, 2 routes of
// ~50 each at the target fleet size) this direct path was measured to fail
// 200/200 times even after adding 2-opt reshaping inside the insertion
// loop itself - because the survivors' own customers never move, "no
// slack anywhere in either fixed survivor order" is a real dead end, not
// just an unlucky insertion order, however many times you retry it as-is.
// So on failure, this also frees a random slice of the survivors' OWN
// customers back into the same repair alongside the evictees, giving the
// search room to reshuffle both sides at once rather than only ever
// inserting into an immovable skeleton.
func eliminateOneRoute(sol Solution, routeIdx int, customers map[int]Customer, depot Customer, capacity float64) (Solution, bool) {
	partialSol, removed := destroyRouteElimination(sol, routeIdx)

	if repaired, ok := repairGreedyNoNewRoute(partialSol, removed, customers, depot, capacity); ok {
		return localSearchImprove(repaired, customers, depot, capacity), true
	}

	survivorCount := 0
	for _, r := range partialSol.Routes {
		survivorCount += len(r.CustomerIDs)
	}
	k := int(math.Max(3, float64(survivorCount)*0.15))

	for shuffle := 0; shuffle < routeEliminationShuffleBudget; shuffle++ {
		var shuffledPartial Solution
		var extraRemoved []int
		if shuffle%2 == 0 {
			shuffledPartial, extraRemoved = destroyWorst(partialSol, k, customers, depot)
		} else {
			shuffledPartial, extraRemoved = destroyRandom(partialSol, k, customers, depot)
		}

		allRemoved := make([]int, 0, len(removed)+len(extraRemoved))
		allRemoved = append(allRemoved, removed...)
		allRemoved = append(allRemoved, extraRemoved...)

		if repaired, ok := repairGreedyNoNewRoute(shuffledPartial, allRemoved, customers, depot, capacity); ok {
			return localSearchImprove(repaired, customers, depot, capacity), true
		}
	}

	return sol, false
}

// tryRouteElimination attempts to eliminate a route, trying up to
// maxAttempts of the weakest routes (in order, see selectWeakestRoutes)
// before giving up. Returns ok=false (sol unchanged) if none succeed, or
// immediately if sol's vehicle count already equals the capacity lower
// bound - no point attempting further reduction. Bounds cost the same way
// the existing stagnation heuristic bounds its maxAttempts=3 retries.
func tryRouteElimination(sol Solution, customers map[int]Customer, depot Customer, capacity float64, maxAttempts int) (Solution, bool) {
	if sol.TotalVehicles <= minVehiclesLowerBound(customers, capacity) {
		return sol, false
	}

	ranked := selectWeakestRoutes(sol, customers)
	if maxAttempts > len(ranked) {
		maxAttempts = len(ranked)
	}

	for i := 0; i < maxAttempts; i++ {
		if repaired, ok := eliminateOneRoute(sol, ranked[i], customers, depot, capacity); ok {
			return repaired, true
		}
	}

	return sol, false
}

// vehicleMinMaxConsecutiveFailures bounds how many attempts in a row
// runVehicleMinimizationPrePhase will retry against the SAME vehicle count
// before concluding elimination is genuinely stuck and giving up early,
// independent of how large the caller's `budget` is. Without this, a
// `budget` derived from a huge -iterations value (10% of it) could spin for
// the entire prephase on a truly-infeasible elimination.
//
// This was originally 200, reasoned to be "cheap to retry" - true for a
// single fixed-skeleton repairGreedyNoNewRoute call, but eliminateOneRoute
// now escalates to routeEliminationShuffleBudget extra full repair attempts
// per failure, and measured on R204 (100 customers, routes of ~13-47) each
// eliminateOneRoute call can take up to several seconds. At 200 that
// measured as the prephase consuming several *minutes* on a partition that
// - per TestR204EliminationPerRoute - fails for every route in the
// solution regardless of retry count: it's a genuine structural dead end
// for this specific customer-to-route split, not bad luck, so more retries
// just burn the time budget the main LNS loop needs to reach a DIFFERENT
// split (via its unrestricted destroy/repair) where elimination might
// actually succeed. 10 keeps a failing case cheap without giving up
// instantly.
const vehicleMinMaxConsecutiveFailures = 10

// runVehicleMinimizationPrePhase attempts to reduce vehicle count as far as
// possible while the solution is still loose (freshly constructed, not yet
// distance-optimized) - see
// docs/superpowers/specs/2026-07-27-route-elimination-operator-design.md
// for why this runs up front rather than only reactively. A single
// route-elimination failure is NOT proof no further reduction is possible -
// repairGreedyNoNewRoute picks its insertion order via Go map iteration,
// which is runtime-randomized even under a fixed -seed, so an immediate
// retry against the identical solution can succeed where the last one
// didn't (this was previously treated as terminal, which left runs stuck
// one vehicle above what a few more retries would have found). Stops when
// the capacity lower bound is reached, `budget` attempts are used up, or
// vehicleMinMaxConsecutiveFailures attempts in a row fail at the same
// vehicle count.
func runVehicleMinimizationPrePhase(sol Solution, customers map[int]Customer, depot Customer, capacity float64, budget int, startTime time.Time) Solution {
	lowerBound := minVehiclesLowerBound(customers, capacity)
	consecutiveFailures := 0

	for attempt := 1; attempt <= budget; attempt++ {
		if sol.TotalVehicles <= lowerBound {
			sendProgressLog(0, sol, startTime, "VEHICLE-MIN", "Reached capacity lower bound (%d vehicles) after %d attempt(s) - stopping pre-phase", lowerBound, attempt-1)
			return sol
		}

		newSol, ok := tryRouteElimination(sol, customers, depot, capacity, 3)
		if !ok {
			consecutiveFailures++
			if consecutiveFailures >= vehicleMinMaxConsecutiveFailures {
				sendProgressLog(0, sol, startTime, "VEHICLE-MIN", "No further route elimination possible after %d consecutive failed attempt(s); stalled at %d vehicles (capacity floor %d)", consecutiveFailures, sol.TotalVehicles, lowerBound)
				return sol
			}
			continue
		}
		consecutiveFailures = 0

		sol = newSol
		sendProgressLog(0, sol, startTime, "VEHICLE-MIN", "Eliminated a route on attempt %d - now %d vehicles, %.2f distance", attempt, sol.TotalVehicles, sol.TotalDistance)
	}

	sendProgressLog(0, sol, startTime, "VEHICLE-MIN", "Pre-phase budget (%d attempts) exhausted at %d vehicles (capacity floor %d)", budget, sol.TotalVehicles, lowerBound)
	return sol
}

func cloneSolution(sol Solution) Solution {
	clonedRoutes := make([]Route, len(sol.Routes))
	for i, r := range sol.Routes {
		cIDs := make([]int, len(r.CustomerIDs))
		copy(cIDs, r.CustomerIDs)

		arrTimes := make(map[int]float64)
		for k, v := range r.ArrivalTimes {
			arrTimes[k] = v
		}

		waitTimes := make(map[int]float64)
		for k, v := range r.WaitingTimes {
			waitTimes[k] = v
		}

		depTimes := make(map[int]float64)
		for k, v := range r.DepartureTimes {
			depTimes[k] = v
		}

		clonedRoutes[i] = Route{
			VehicleID:      r.VehicleID,
			CustomerIDs:    cIDs,
			Distance:       r.Distance,
			Load:           r.Load,
			ArrivalTimes:   arrTimes,
			WaitingTimes:   waitTimes,
			DepartureTimes: depTimes,
		}
	}

	return Solution{
		Routes:        clonedRoutes,
		TotalDistance: sol.TotalDistance,
		TotalVehicles: sol.TotalVehicles,
	}
}

// Communication helpers with server (JSON streams)
func sendStart(message string) {
	msg := ProgressMessage{
		Type:    "start",
		Message: message,
	}
	bytes, _ := json.Marshal(msg)
	fmt.Println(string(bytes))
}

func sendProgress(iter int, sol Solution, startTime time.Time) {
	msg := ProgressMessage{
		Type:              "progress",
		Iteration:         iter,
		BestDistance:      sol.TotalDistance,
		BestVehicles:      sol.TotalVehicles,
		Routes:            sol.Routes,
		ComputationTimeMs: time.Since(startTime).Milliseconds(),
	}
	bytes, _ := json.Marshal(msg)
	fmt.Println(string(bytes))
}

func sendResult(sol Solution, startTime time.Time, finalIteration int) {
	msg := ProgressMessage{
		Type:              "result",
		Iteration:         finalIteration,
		BestDistance:      sol.TotalDistance,
		BestVehicles:      sol.TotalVehicles,
		Routes:            sol.Routes,
		ComputationTimeMs: time.Since(startTime).Milliseconds(),
		Message:           "Optimization completed successfully.",
	}
	bytes, _ := json.Marshal(msg)
	fmt.Println(string(bytes))
}

func sendProgressLog(iter int, sol Solution, startTime time.Time, category string, format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	msg := ProgressMessage{
		Type:              "progress",
		Iteration:         iter,
		BestDistance:      sol.TotalDistance,
		BestVehicles:      sol.TotalVehicles,
		Routes:            sol.Routes,
		ComputationTimeMs: time.Since(startTime).Milliseconds(),
		Message:           fmt.Sprintf("[%s] %s", category, message),
	}
	bytes, _ := json.Marshal(msg)
	fmt.Println(string(bytes))
}

type DestructionAttempt struct {
	VehicleIDs []int `json:"vehicleIds"`
}

func selectStagnationRoutesHeuristically(
	bestSol Solution,
	history []DestructionAttempt,
	minDestroy, maxDestroy int,
	customerMap map[int]Customer,
	attempt int,
) []int {
	numRoutes := len(bestSol.Routes)
	if numRoutes == 0 {
		return nil
	}

	// We want to pick a target number of routes to destroy:
	countToDestroy := minDestroy + rand.Intn(maxDestroy-minDestroy+1)
	if countToDestroy > numRoutes {
		countToDestroy = numRoutes
	}
	if countToDestroy < 1 {
		countToDestroy = 1
	}

	// Calculate centroid and metrics for each route
	type RouteCentroid struct {
		RouteID   int
		CentroidX float64
		CentroidY float64
		AvgDist   float64
		Load      float64
		RouteIdx  int
	}

	centroids := make([]RouteCentroid, numRoutes)
	for idx, r := range bestSol.Routes {
		sumX := 0.0
		sumY := 0.0
		cnt := 0
		for _, cID := range r.CustomerIDs {
			if c, exists := customerMap[cID]; exists {
				sumX += c.X
				sumY += c.Y
				cnt++
			}
		}
		avgX := 0.0
		avgY := 0.0
		avgDist := r.Distance
		if cnt > 0 {
			avgX = sumX / float64(cnt)
			avgY = sumY / float64(cnt)
			avgDist = r.Distance / float64(cnt)
		}
		centroids[idx] = RouteCentroid{
			RouteID:   r.VehicleID,
			CentroidX: avgX,
			CentroidY: avgY,
			AvgDist:   avgDist,
			Load:      r.Load,
			RouteIdx:  idx,
		}
	}

	// Helper to find the closest routes to a given seed route
	getClosestRoutes := func(seedIdx int, count int) []int {
		seed := centroids[seedIdx]
		type DistWithID struct {
			RouteID int
			Dist    float64
		}
		var list []DistWithID
		for idx, other := range centroids {
			if idx == seedIdx {
				continue
			}
			dx := seed.CentroidX - other.CentroidX
			dy := seed.CentroidY - other.CentroidY
			dist := math.Sqrt(dx*dx + dy*dy)
			list = append(list, DistWithID{RouteID: other.RouteID, Dist: dist})
		}
		// Sort list by distance ascending (closest centroids)
		sort.Slice(list, func(i, j int) bool {
			return list[i].Dist < list[j].Dist
		})

		res := []int{seed.RouteID}
		for i := 0; i < count-1 && i < len(list); i++ {
			res = append(res, list[i].RouteID)
		}
		return res
	}

	isAlreadyAttempted := func(ids []int) bool {
		// Create a sorted copy of ids
		sortedIDs := make([]int, len(ids))
		copy(sortedIDs, ids)
		sort.Ints(sortedIDs)

		for _, att := range history {
			if len(att.VehicleIDs) == len(sortedIDs) {
				matched := true
				sortedAttempt := make([]int, len(att.VehicleIDs))
				copy(sortedAttempt, att.VehicleIDs)
				sort.Ints(sortedAttempt)
				for k, v := range sortedIDs {
					if sortedAttempt[k] != v {
						matched = false
						break
					}
				}
				if matched {
					return true
				}
			}
		}
		return false
	}

	// Try multiple seeds and strategies to find a unique combination not in history
	for trial := 0; trial < 20; trial++ {
		// Choose strategy
		strategy := (attempt + trial) % 3
		var seedIdx int

		if strategy == 0 {
			// Random seed route
			seedIdx = rand.Intn(numRoutes)
		} else if strategy == 1 {
			// Worst performing / highest average distance per customer
			sort.Slice(centroids, func(i, j int) bool {
				return centroids[i].AvgDist > centroids[j].AvgDist
			})
			seedIdx = centroids[0].RouteIdx
		} else {
			// Under-utilized / lowest load
			sort.Slice(centroids, func(i, j int) bool {
				return centroids[i].Load < centroids[j].Load
			})
			seedIdx = centroids[0].RouteIdx
		}

		// Find original index in centroids of the selected seedIdx
		currentIdx := 0
		for idx, c := range centroids {
			if c.RouteIdx == seedIdx {
				currentIdx = idx
				break
			}
		}

		candidateIDs := getClosestRoutes(currentIdx, countToDestroy)
		if !isAlreadyAttempted(candidateIDs) {
			return candidateIDs
		}
	}

	// Fallback
	return getClosestRoutes(rand.Intn(numRoutes), countToDestroy)
}

// shouldProbeLKHMinusOne reports whether the LKH3 minus-one vehicle probe
// should fire for this stagnation attempt, and if so, the vehicle count to
// probe. Probing only fires on the first attempt of a stagnation trigger -
// this bounds the added LKH3 call cost to at most one extra solve per
// trigger, not per attempt (attempts 2-3 keep today's unprobed behavior).
// destroyedRouteCount <= 1 skips the probe entirely: asking LKH3 for 0
// vehicles is meaningless.
func shouldProbeLKHMinusOne(attempt int, destroyedRouteCount int) (probeVehicles int, ok bool) {
	if attempt != 1 || destroyedRouteCount <= 1 {
		return 0, false
	}
	return destroyedRouteCount - 1, true
}

// invokeLKHSubSolver re-solves a set of destroyed customers using the native LKH3
// binary (lkh_bin) instead of the pure-Go I1+LNS sub-solver. LKH3 handles
// CVRPTW via a soft violation-penalty model, not hard constraints, so its output
// is never trusted directly: every returned route is re-validated (and its
// timing/load fields repopulated) through calculateRouteDetails, the same
// feasibility function every other insertion/repair decision in this file uses.
// Returns nil on any failure (binary missing, timeout, parse error, reported
// violation, or a route that fails re-validation) so the caller can fall back to
// the existing sub-solver unchanged.
func invokeLKHSubSolver(destroyedCustomers []Customer, depot Customer, capacity float64, customerMap map[int]Customer, maxVehicles int) *Solution {
	n := len(destroyedCustomers)
	if n == 0 {
		return nil
	}

	// LKH node IDs are 1-indexed with node 1 reserved for the depot.
	// idOf[k-1] maps LKH node k back to the real Customer.ID.
	idOf := make([]int, n+1)
	idOf[0] = depot.ID
	for i, c := range destroyedCustomers {
		idOf[i+1] = c.ID
	}

	// Unlike the pure-Go sub-solver (which can always open another route when a
	// cluster doesn't fit), LKH3's VEHICLES is a hard cap - it must partition
	// customers into exactly that many routes, modeled internally as a TSP over
	// N + (VEHICLES-1) *coincident* depot copies. Setting this to n (one per
	// customer) was tried and measured ~75x slower overall (degenerate
	// alpha-nearness candidate generation over many zero-distance depot copies)
	// with no quality gain. maxVehicles should instead be the number of routes
	// destroyed to produce this subproblem - a feasible maxVehicles-route cover
	// is already known to exist (bestSol had one moments earlier), so this both
	// avoids the artificial-infeasibility problem a capacity-only estimate had
	// and keeps the internal graph small. LKH reports any genuinely-unneeded
	// vehicles as trivial empty depot-to-depot routes (filtered out below).
	vehicles := maxVehicles
	if vehicles < 1 {
		vehicles = 1
	}
	if vehicles > n {
		vehicles = n
	}

	instanceText := buildLKHInstanceText(destroyedCustomers, depot, capacity, n, vehicles)

	solText, ok := runLKHSolver(instanceText, rand.Int63n(1<<31))
	if !ok {
		return nil
	}

	lines := strings.Split(strings.TrimSpace(solText), "\n")
	if len(lines) < 2 {
		return nil
	}

	// First line: "<name>, Cost: <violation>_<cost>" - a nonzero violation means
	// LKH's soft penalty model did not find a fully time-window/capacity-feasible
	// tour, so the whole result is discarded rather than trusted.
	costIdx := strings.Index(lines[0], "Cost:")
	if costIdx == -1 {
		return nil
	}
	costParts := strings.SplitN(strings.TrimSpace(lines[0][costIdx+len("Cost:"):]), "_", 2)
	if len(costParts) != 2 {
		return nil
	}
	violation, err := strconv.ParseFloat(costParts[0], 64)
	if err != nil || violation != 0 {
		return nil
	}

	var routes []Route
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		parenIdx := strings.Index(line, "(")
		if line == "" || parenIdx == -1 {
			continue
		}

		var custIDs []int
		for _, tok := range strings.Fields(line[:parenIdx]) {
			nodeID, err := strconv.Atoi(tok)
			if err != nil || nodeID == 1 {
				continue // parse error or depot bookend
			}
			if nodeID-1 < 1 || nodeID-1 >= len(idOf) {
				continue
			}
			custIDs = append(custIDs, idOf[nodeID-1])
		}
		if len(custIDs) == 0 {
			continue // unused vehicle, reported as an empty depot-to-depot route
		}

		rDetails, feasible := calculateRouteDetails(custIDs, customerMap, depot, capacity)
		if !feasible {
			// LKH reported zero violation under its own (scaled/penalty) model but
			// this route fails the solver's own ground-truth feasibility check -
			// reject the whole result rather than merge a partially-trusted one.
			return nil
		}
		routes = append(routes, rDetails)
	}

	if len(routes) == 0 {
		return nil
	}
	for idx := range routes {
		routes[idx].VehicleID = idx + 1
	}

	subSol := Solution{Routes: routes}
	recalculateSolutionMetrics(&subSol)
	return &subSol
}

// buildLKHInstanceText renders the CVRPTW subproblem (destroyedCustomers) into
// LKH3's instance-file text format. Platform-independent - both the native
// (exec.Command) and wasm (JS-bridged) runLKHSolver implementations call this
// for identical instance content.
func buildLKHInstanceText(destroyedCustomers []Customer, depot Customer, capacity float64, n, vehicles int) string {
	var instance strings.Builder
	fmt.Fprintf(&instance, "NAME : sub\n")
	fmt.Fprintf(&instance, "TYPE : CVRPTW\n")
	fmt.Fprintf(&instance, "DIMENSION : %d\n", n+1)
	fmt.Fprintf(&instance, "VEHICLES : %d\n", vehicles)
	fmt.Fprintf(&instance, "CAPACITY : %d\n", int(math.Round(capacity)))
	fmt.Fprintf(&instance, "EDGE_WEIGHT_TYPE : EXACT_2D\n")

	fmt.Fprintf(&instance, "NODE_COORD_SECTION\n")
	fmt.Fprintf(&instance, "1 %.6f %.6f\n", depot.X, depot.Y)
	for i, c := range destroyedCustomers {
		fmt.Fprintf(&instance, "%d %.6f %.6f\n", i+2, c.X, c.Y)
	}

	fmt.Fprintf(&instance, "DEMAND_SECTION\n")
	fmt.Fprintf(&instance, "1 0\n")
	for i, c := range destroyedCustomers {
		fmt.Fprintf(&instance, "%d %d\n", i+2, int(math.Round(c.Demand)))
	}

	// This LKH3 build only recognizes a single scalar SERVICE_TIME for the whole
	// instance (SERVICE_TIME_SECTION is not registered as a top-level keyword in
	// ReadProblem here) - safe because Solomon/Homberger instances use a uniform
	// per-customer service time (verified against data/rc201.txt: only 0/depot
	// and one shared nonzero value occur).
	fmt.Fprintf(&instance, "SERVICE_TIME : %.6f\n", destroyedCustomers[0].ServiceTime)

	// 6 fields per row: id, earliest, latest, then 3 pickup-delivery fields this
	// parser always consumes regardless of problem type (unused here, hence 0 0 0).
	fmt.Fprintf(&instance, "TIME_WINDOW_SECTION\n")
	fmt.Fprintf(&instance, "1 %.6f %.6f 0 0 0\n", depot.ReadyTime, depot.DueDate)
	for i, c := range destroyedCustomers {
		fmt.Fprintf(&instance, "%d %.6f %.6f 0 0 0\n", i+2, c.ReadyTime, c.DueDate)
	}

	fmt.Fprintf(&instance, "DEPOT_SECTION\n1\n-1\nEOF\n")

	return instance.String()
}

func sendError(err string) {
	msg := ProgressMessage{
		Type:    "error",
		Message: err,
	}
	bytes, _ := json.Marshal(msg)
	fmt.Println(string(bytes))
}
