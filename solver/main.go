package main

import (
	"bytes"
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"net/http"
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
	IsFeasible    bool    `json:"isFeasible"`
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

func main() {
	filePath := flag.String("file", "", "Path to the Solomon instance file")
	iterations := flag.Int("iterations", 1000, "Number of LNS iterations")
	seed := flag.Int64("seed", 42, "Random seed")
	llmThreshold := flag.Int("llm-threshold", 20, "Iteration threshold for LLM intervention")
	useLLM := flag.Bool("use-llm", false, "Use Ollama LLM via /api/llm-destroy instead of the pure-Go heuristic for stagnation destroy selection")
	useLKH := flag.Bool("use-lkh", false, "Use the native LKH3 binary instead of the pure-Go K-means+LNS sub-solver for stagnation sub-solving")
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

		// 1. Destroy
		var removed []int
		var partialSol Solution
		destroyType := "Worst Destroy"
		if rand.Float64() < 0.5 {
			partialSol, removed = destroyWorst(currentSol, k, customerMap, depot)
		} else {
			destroyType = "Random Destroy"
			partialSol, removed = destroyRandom(currentSol, k, customerMap, depot)
		}
		sendProgressLog(iter, bestSol, startTime, "LNS:CHOOSE", "Neighborhood '%s' selected to remove %d customers: %v", destroyType, k, removed)

		// 2. Repair
		candidateSol := repairGreedy(partialSol, removed, customerMap, depot, capacity)

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
				if *useLLM {
					triggerCategory = "LLM:TRIGGER"
				}
				if attempt > 1 {
					sendProgressLog(iter, bestSol, startTime, triggerCategory, "Stagnation solver Attempt %d: Retrying with alternate seed routes. Increasing destroy size limit to %d-%d routes.", attempt, minDestroyRoutes, maxDestroyRoutes)
				} else if *useLLM {
					sendProgressLog(iter, bestSol, startTime, triggerCategory, "Stagnation detected (stagnated for %d iters). Querying Ollama LLM (suggesting %d-%d routes to destroy).", stagnationCounter, minDestroyRoutes, maxDestroyRoutes)
				} else {
					sendProgressLog(iter, bestSol, startTime, triggerCategory, "Stagnation detected (stagnated for %d iters). Invoking Smart Heuristic routing analyzer (suggesting %d-%d routes to destroy).", stagnationCounter, minDestroyRoutes, maxDestroyRoutes)
				}

				// Select vehicles to destroy: Ollama LLM if enabled, else the pure Go heuristic
				var finalDestroyIDs []int
				decisionCategory := "HEURISTIC:DECISION"
				if *useLLM {
					finalDestroyIDs = invokeLLMToSelectTrucks(bestSol, name, history, minDestroyRoutes, maxDestroyRoutes)

					// Validate returned IDs against the actual current route set before using them
					validVehicleIDs := make(map[int]bool)
					for _, r := range bestSol.Routes {
						validVehicleIDs[r.VehicleID] = true
					}
					filtered := make([]int, 0, len(finalDestroyIDs))
					for _, id := range finalDestroyIDs {
						if validVehicleIDs[id] {
							filtered = append(filtered, id)
						}
					}
					finalDestroyIDs = filtered

					if len(finalDestroyIDs) == 0 {
						sendProgressLog(iter, bestSol, startTime, "LLM:FALLBACK", "Ollama unavailable or returned no valid vehicles; falling back to heuristic destroy.")
						finalDestroyIDs = selectStagnationRoutesHeuristically(bestSol, history, minDestroyRoutes, maxDestroyRoutes, customerMap, attempt)
					} else {
						decisionCategory = "LLM:DECISION"
					}
				} else {
					finalDestroyIDs = selectStagnationRoutesHeuristically(bestSol, history, minDestroyRoutes, maxDestroyRoutes, customerMap, attempt)
				}
				
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
							sendProgressLog(iter, bestSol, startTime, "LKH:TRIGGER", "Attempt %d: Invoking LKH3 on %d removed customers (vehicles cap = %d, no timeout)...", attempt, len(destroyedCustomers), len(finalDestroyIDs))
							lkhStart := time.Now()
							lkhSol := invokeLKHSubSolver(destroyedCustomers, depot, capacity, customerMap, len(finalDestroyIDs))
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
							sendProgressLog(iter, bestSol, startTime, "HEURISTIC:SUB-SOLVER", "Attempt %d: Re-routing %d removed customers. Phase 1: K-Means Clustering -> Initial Sequence Insertion...", attempt, len(destroyedCustomers))

							// Re-solve with our approach: Clustering -> Initial solution -> LNS (run on the subset)
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
								VehicleIDs:  finalDestroyIDs,
								CustomerIDs: destroyedCustIDs,
							})
						}
					}
				} else {
					sendProgressLog(iter, bestSol, startTime, "HEURISTIC:FAILURE", "[FAILED] Attempt %d generated no valid vehicles. Retrying...", attempt)
					history = append(history, DestructionAttempt{
						VehicleIDs:  finalDestroyIDs,
						CustomerIDs: []int{},
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
	sendResult(bestSol, startTime)
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

type Point struct {
	X float64
	Y float64
}

// kMeansCluster partitions the customers into k clusters using the K-Means algorithm
func kMeansCluster(customers []Customer, k int) [][]Customer {
	if k <= 1 || len(customers) <= k {
		// If k is 1 or fewer, or there are fewer customers than clusters, just return them
		if k <= 1 || len(customers) == 0 {
			return [][]Customer{customers}
		}
		clusters := make([][]Customer, len(customers))
		for i, c := range customers {
			clusters[i] = []Customer{c}
		}
		return clusters
	}

	// Initialize centroids by random selection
	centroids := make([]Point, k)
	usedIdx := make(map[int]bool)
	for i := 0; i < k; i++ {
		idx := rand.Intn(len(customers))
		for usedIdx[idx] {
			idx = rand.Intn(len(customers))
		}
		usedIdx[idx] = true
		centroids[i] = Point{X: customers[idx].X, Y: customers[idx].Y}
	}

	maxIters := 50
	clusters := make([][]Customer, k)

	for iter := 0; iter < maxIters; iter++ {
		// Reset clusters
		clusters = make([][]Customer, k)

		// Assign each customer to the nearest centroid
		for _, cust := range customers {
			minDist := math.MaxFloat64
			bestCluster := 0
			for i, cent := range centroids {
				dist := math.Sqrt(math.Pow(cust.X-cent.X, 2) + math.Pow(cust.Y-cent.Y, 2))
				if dist < minDist {
					minDist = dist
					bestCluster = i
				}
			}
			clusters[bestCluster] = append(clusters[bestCluster], cust)
		}

		// Update centroids
		changed := false
		for i := 0; i < k; i++ {
			if len(clusters[i]) == 0 {
				// Empty cluster gets assigned to a random customer's coordinates
				idx := rand.Intn(len(customers))
				newCent := Point{X: customers[idx].X, Y: customers[idx].Y}
				if centroids[i] != newCent {
					centroids[i] = newCent
					changed = true
				}
				continue
			}

			var sumX, sumY float64
			for _, cust := range clusters[i] {
				sumX += cust.X
				sumY += cust.Y
			}
			newCent := Point{
				X: sumX / float64(len(clusters[i])),
				Y: sumY / float64(len(clusters[i])),
			}

			dist := math.Sqrt(math.Pow(centroids[i].X-newCent.X, 2) + math.Pow(centroids[i].Y-newCent.Y, 2))
			if dist > 1e-4 {
				centroids[i] = newCent
				changed = true
			}
		}

		if !changed {
			break
		}
	}

	// Return non-empty clusters
	var result [][]Customer
	for _, c := range clusters {
		if len(c) > 0 {
			result = append(result, c)
		}
	}
	return result
}

// Solomon I1 Insertion Heuristic with initial Clustering step
func buildInitialSolution(customers []Customer, depot Customer, capacity float64, customerMap map[int]Customer) Solution {
	// 1. Calculate total demand and estimate number of clusters (K)
	totalDemand := 0.0
	for _, c := range customers {
		totalDemand += c.Demand
	}

	// Est k with some capacity buffer (90%) so clusters map nicely to vehicles
	k := int(math.Ceil(totalDemand / (capacity * 0.90)))
	if k < 1 {
		k = 1
	}
	if k > len(customers) {
		k = len(customers)
	}

	// 2. Perform clustering
	clusters := kMeansCluster(customers, k)

	var routes []Route
	vehicleID := 1

	// 3. Route each cluster independently using I1 sequential insertion
	for _, cluster := range clusters {
		unrouted := make(map[int]Customer)
		for _, c := range cluster {
			unrouted[c.ID] = c
		}

		for len(unrouted) > 0 {
			// Initialize a new route with the most urgent seed customer of this cluster
			var seedID int
			bestScore := -1e9

			for id, cust := range unrouted {
				dist := distance(depot, cust)
				score := dist*0.5 - cust.DueDate*0.5
				if score > bestScore {
					bestScore = score
					seedID = id
				}
			}

			delete(unrouted, seedID)
			routeCusts := []int{seedID}

			// Try to insert remaining customers of this cluster as long as feasible
			for {
				bestCustID := -1
				bestPos := -1
				bestInsertCost := 1e9

				for id := range unrouted {
					for pos := 0; pos <= len(routeCusts); pos++ {
						// Test insertion
						testRoute := make([]int, len(routeCusts)+1)
						copy(testRoute[:pos], routeCusts[:pos])
						testRoute[pos] = id
						copy(testRoute[pos+1:], routeCusts[pos:])

						rDetails, feasible := calculateRouteDetails(testRoute, customerMap, depot, capacity)
						if feasible {
							origDetails, _ := calculateRouteDetails(routeCusts, customerMap, depot, capacity)
							cost := rDetails.Distance - origDetails.Distance
							if cost < bestInsertCost {
								bestInsertCost = cost
								bestCustID = id
								bestPos = pos
							}
						}
					}
				}

				if bestCustID != -1 {
					routeCusts = append(routeCusts, 0)
					copy(routeCusts[bestPos+1:], routeCusts[bestPos:])
					routeCusts[bestPos] = bestCustID
					delete(unrouted, bestCustID)
				} else {
					break
				}
			}

			// Complete and store the route
			rDetails, _ := calculateRouteDetails(routeCusts, customerMap, depot, capacity)
			rDetails.VehicleID = vehicleID
			routes = append(routes, rDetails)
			vehicleID++
		}
	}

	sol := Solution{Routes: routes}
	recalculateSolutionMetrics(&sol)
	return sol
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
	sol.IsFeasible = true

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
		working.Routes[bestRouteIdx] = rDetails

		delete(remaining, bestCustID)
	}

	for idx := range working.Routes {
		working.Routes[idx].VehicleID = idx + 1
	}
	recalculateSolutionMetrics(&working)
	return working, true
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
		partialSol, removed := destroyRouteElimination(sol, ranked[i])
		repaired, ok := repairGreedyNoNewRoute(partialSol, removed, customers, depot, capacity)
		if ok {
			return repaired, true
		}
	}

	return sol, false
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
		IsFeasible:    sol.IsFeasible,
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

func sendResult(sol Solution, startTime time.Time) {
	msg := ProgressMessage{
		Type:              "result",
		BestDistance:      sol.TotalDistance,
		BestVehicles:      sol.TotalVehicles,
		Routes:            sol.Routes,
		ComputationTimeMs: time.Since(startTime).Milliseconds(),
		Message:           "Optimization completed successfully.",
	}
	bytes, _ := json.Marshal(msg)
	fmt.Println(string(bytes))
}

func sendResultWithMessage(sol Solution, startTime time.Time, message string) {
	msg := ProgressMessage{
		Type:              "result",
		BestDistance:      sol.TotalDistance,
		BestVehicles:      sol.TotalVehicles,
		Routes:            sol.Routes,
		ComputationTimeMs: time.Since(startTime).Milliseconds(),
		Message:           message,
	}
	bytes, _ := json.Marshal(msg)
	fmt.Println(string(bytes))
}

func sendProgressMessage(iter int, sol Solution, startTime time.Time, message string) {
	msg := ProgressMessage{
		Type:              "progress",
		Iteration:         iter,
		BestDistance:      sol.TotalDistance,
		BestVehicles:      sol.TotalVehicles,
		Routes:            sol.Routes,
		ComputationTimeMs: time.Since(startTime).Milliseconds(),
		Message:           message,
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
	VehicleIDs  []int `json:"vehicleIds"`
	CustomerIDs []int `json:"customerIds"`
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

// invokeLKHSubSolver re-solves a set of destroyed customers using the native LKH3
// binary (lkh_bin) instead of the pure-Go K-means+LNS sub-solver. LKH3 handles
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

func invokeLLMToSelectTrucks(bestSol Solution, instanceName string, history []DestructionAttempt, minDestroy, maxDestroy int) []int {
	type LLMRequest struct {
		Routes       []Route              `json:"routes"`
		InstanceName string               `json:"instanceName"`
		History      []DestructionAttempt `json:"history"`
		MinDestroy   int                  `json:"minDestroy"`
		MaxDestroy   int                  `json:"maxDestroy"`
	}

	reqBody, err := json.Marshal(LLMRequest{
		Routes:       bestSol.Routes,
		InstanceName: instanceName,
		History:      history,
		MinDestroy:   minDestroy,
		MaxDestroy:   maxDestroy,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal LLM request: %v\n", err)
		return nil
	}

	url := "http://localhost:3000/api/llm-destroy"
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		fmt.Fprintf(os.Stderr, "LLM HTTP request failed: %v\n", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "LLM request returned status: %s\n", resp.Status)
		return nil
	}

	var result struct {
		VehicleIDs []int `json:"vehicleIds"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to decode LLM response: %v\n", err)
		return nil
	}

	return result.VehicleIDs
}

func sendError(err string) {
	msg := ProgressMessage{
		Type:    "error",
		Message: err,
	}
	bytes, _ := json.Marshal(msg)
	fmt.Println(string(bytes))
}
