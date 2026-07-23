import { GoogleGenAI, Type } from '@google/genai';

export interface Customer {
  id: number;
  x: number;
  y: number;
  demand: number;
  readyTime: number;
  dueDate: number;
  serviceTime: number;
}

export interface VRPTWInstanceData {
  name: string;
  vehicleNumber: number;
  capacity: number;
  depot: Customer;
  customers: Customer[];
}

export interface RouteData {
  vehicleId: number;
  customerIds: number[];
  distance: number;
  load: number;
  arrivalTimes: Record<number, number>;
  waitingTimes: Record<number, number>;
  departureTimes: Record<number, number>;
}

export interface SolutionData {
  routes: RouteData[];
  totalDistance: number;
  totalVehicles: number;
  isFeasible: boolean;
}

export interface SolverParamsOptions {
  iterations: number;
  algorithm: 'lns' | 'sa' | 'ortools' | 'lns-ortools' | string;
  seed?: number;
  optimal?: number;
  llmThreshold?: number;
}

export interface ProgressMessage {
  type: 'start' | 'progress' | 'result' | 'error';
  iteration?: number;
  bestDistance?: number;
  bestVehicles?: number;
  routes?: RouteData[];
  computationTimeMs?: number;
  message?: string;
}

export function evaluateRoute(customerIds: number[], depot: Customer, customerMap: Map<number, Customer>): RouteData {
  let dist = 0;
  let load = 0;
  let currentTime = 0;

  const arrivalTimes: Record<number, number> = {};
  const waitingTimes: Record<number, number> = {};
  const departureTimes: Record<number, number> = {};

  let prev = depot;

  for (const id of customerIds) {
    const cust = customerMap.get(id);
    if (!cust) continue;

    const d = Math.hypot(cust.x - prev.x, cust.y - prev.y);
    dist += d;
    load += cust.demand;

    const arrTime = currentTime + d;
    arrivalTimes[id] = Math.round(arrTime * 100) / 100;

    const waitTime = Math.max(0, cust.readyTime - arrTime);
    waitingTimes[id] = Math.round(waitTime * 100) / 100;

    currentTime = arrTime + waitTime + cust.serviceTime;
    departureTimes[id] = Math.round(currentTime * 100) / 100;

    prev = cust;
  }

  const dToDepot = Math.hypot(depot.x - prev.x, depot.y - prev.y);
  dist += dToDepot;

  return {
    vehicleId: 0,
    customerIds,
    distance: Math.round(dist * 100) / 100,
    load,
    arrivalTimes,
    waitingTimes,
    departureTimes
  };
}

export function isRouteFeasible(customerIds: number[], depot: Customer, capacity: number, customerMap: Map<number, Customer>): boolean {
  let load = 0;
  let currentTime = 0;
  let prev = depot;

  for (const id of customerIds) {
    const cust = customerMap.get(id);
    if (!cust) return false;

    load += cust.demand;
    if (load > capacity) return false;

    const d = Math.hypot(cust.x - prev.x, cust.y - prev.y);
    const arrTime = currentTime + d;
    if (arrTime > cust.dueDate) return false;

    const waitTime = Math.max(0, cust.readyTime - arrTime);
    currentTime = arrTime + waitTime + cust.serviceTime;

    prev = cust;
  }

  const dToDepot = Math.hypot(depot.x - prev.x, depot.y - prev.y);
  if (currentTime + dToDepot > depot.dueDate) return false;

  return true;
}

export function findBestInsertion(
  cust: Customer,
  route: number[],
  depot: Customer,
  capacity: number,
  customerMap: Map<number, Customer>
): { index: number; extraCost: number } | null {
  let bestPos = -1;
  let minExtraCost = Infinity;

  for (let i = 0; i <= route.length; i++) {
    const candidateRoute = [...route.slice(0, i), cust.id, ...route.slice(i)];
    if (isRouteFeasible(candidateRoute, depot, capacity, customerMap)) {
      const prevCust = i === 0 ? depot : customerMap.get(route[i - 1])!;
      const nextCust = i === route.length ? depot : customerMap.get(route[i])!;

      const oldDist = Math.hypot(nextCust.x - prevCust.x, nextCust.y - prevCust.y);
      const newDist = Math.hypot(cust.x - prevCust.x, cust.y - prevCust.y) + Math.hypot(nextCust.x - cust.x, nextCust.y - cust.y);
      const extraCost = newDist - oldDist;

      if (extraCost < minExtraCost) {
        minExtraCost = extraCost;
        bestPos = i;
      }
    }
  }

  if (bestPos === -1) return null;
  return { index: bestPos, extraCost: minExtraCost };
}

export function createInitialSolution(instance: VRPTWInstanceData): SolutionData {
  const { depot, customers, capacity } = instance;
  const customerMap = new Map<number, Customer>();
  customers.forEach(c => customerMap.set(c.id, c));
  customerMap.set(depot.id, depot);

  const unassigned = [...customers].sort((a, b) => {
    const angleA = Math.atan2(a.y - depot.y, a.x - depot.x);
    const angleB = Math.atan2(b.y - depot.y, b.x - depot.x);
    if (Math.abs(angleA - angleB) > 0.1) return angleA - angleB;
    return a.readyTime - b.readyTime;
  });

  const routesCustomerIds: number[][] = [];

  while (unassigned.length > 0) {
    let currentRoute: number[] = [];
    let progress = true;

    while (progress && unassigned.length > 0) {
      let bestCustIndex = -1;
      let bestPos = -1;
      let minExtraCost = Infinity;

      for (let i = 0; i < unassigned.length; i++) {
        const cust = unassigned[i];
        const res = findBestInsertion(cust, currentRoute, depot, capacity, customerMap);
        if (res && res.extraCost < minExtraCost) {
          minExtraCost = res.extraCost;
          bestPos = res.index;
          bestCustIndex = i;
        }
      }

      if (bestCustIndex !== -1) {
        const [cust] = unassigned.splice(bestCustIndex, 1);
        currentRoute.splice(bestPos, 0, cust.id);
      } else {
        progress = false;
      }
    }

    if (currentRoute.length > 0) {
      routesCustomerIds.push(currentRoute);
    } else {
      const cust = unassigned.shift()!;
      routesCustomerIds.push([cust.id]);
    }
  }

  const routes: RouteData[] = routesCustomerIds.map((cIds, idx) => {
    const r = evaluateRoute(cIds, depot, customerMap);
    r.vehicleId = idx + 1;
    return r;
  });

  const totalDistance = Math.round(routes.reduce((acc, r) => acc + r.distance, 0) * 100) / 100;

  return {
    routes,
    totalDistance,
    totalVehicles: routes.length,
    isFeasible: true
  };
}

export function localSearch2Opt(
  routesCustomerIds: number[][],
  depot: Customer,
  capacity: number,
  customerMap: Map<number, Customer>
): number[][] {
  const newRoutes = routesCustomerIds.map(r => [...r]);

  for (let rIdx = 0; rIdx < newRoutes.length; rIdx++) {
    const route = newRoutes[rIdx];
    if (route.length < 3) continue;

    let improved = true;
    while (improved) {
      improved = false;
      for (let i = 0; i < route.length - 1; i++) {
        for (let j = i + 1; j < route.length; j++) {
          const candidate = [
            ...route.slice(0, i),
            ...route.slice(i, j + 1).reverse(),
            ...route.slice(j + 1)
          ];
          if (isRouteFeasible(candidate, depot, capacity, customerMap)) {
            const oldEval = evaluateRoute(route, depot, customerMap);
            const newEval = evaluateRoute(candidate, depot, customerMap);
            if (newEval.distance < oldEval.distance - 0.01) {
              newRoutes[rIdx] = candidate;
              route.splice(0, route.length, ...candidate);
              improved = true;
              break;
            }
          }
        }
        if (improved) break;
      }
    }
  }

  return newRoutes;
}

export function localSearchRelocate(
  routesCustomerIds: number[][],
  depot: Customer,
  capacity: number,
  customerMap: Map<number, Customer>
): number[][] {
  let routes = routesCustomerIds.map(r => [...r]);
  let improved = true;

  while (improved) {
    improved = false;

    for (let r1 = 0; r1 < routes.length; r1++) {
      for (let pos1 = 0; pos1 < routes[r1].length; pos1++) {
        const custId = routes[r1][pos1];
        const cust = customerMap.get(custId)!;

        for (let r2 = 0; r2 < routes.length; r2++) {
          const targetRoute = r1 === r2 ? routes[r1].filter((_, idx) => idx !== pos1) : routes[r2];

          const bestInsertion = findBestInsertion(cust, targetRoute, depot, capacity, customerMap);
          if (bestInsertion) {
            const r1Candidate = routes[r1].filter((_, idx) => idx !== pos1);
            const r2Candidate = r1 === r2
              ? [...targetRoute.slice(0, bestInsertion.index), custId, ...targetRoute.slice(bestInsertion.index)]
              : [...routes[r2].slice(0, bestInsertion.index), custId, ...routes[r2].slice(bestInsertion.index)];

            if (r1 !== r2 && r1Candidate.length > 0 && !isRouteFeasible(r1Candidate, depot, capacity, customerMap)) continue;

            const oldDist = (r1 === r2 ? 0 : evaluateRoute(routes[r1], depot, customerMap).distance) + evaluateRoute(routes[r2], depot, customerMap).distance;
            const newDist = (r1 === r2 ? 0 : (r1Candidate.length > 0 ? evaluateRoute(r1Candidate, depot, customerMap).distance : 0)) + evaluateRoute(r2Candidate, depot, customerMap).distance;

            if (newDist < oldDist - 0.01) {
              if (r1 === r2) {
                routes[r1] = r2Candidate;
              } else {
                routes[r1] = r1Candidate;
                routes[r2] = r2Candidate;
              }
              routes = routes.filter(r => r.length > 0);
              improved = true;
              break;
            }
          }
        }
        if (improved) break;
      }
      if (improved) break;
    }
  }

  return routes;
}

export function repairRegret2(
  removedCustomerIds: number[],
  routesCustomerIds: number[][],
  depot: Customer,
  capacity: number,
  customerMap: Map<number, Customer>
): number[][] {
  const unassigned = [...removedCustomerIds];
  let routes = routesCustomerIds.map(r => [...r]);

  while (unassigned.length > 0) {
    let bestCustIndex = -1;
    let maxRegret = -1;
    let selectedRouteIndex = -1;
    let selectedInsertPos = -1;

    for (let cIdx = 0; cIdx < unassigned.length; cIdx++) {
      const cust = customerMap.get(unassigned[cIdx])!;
      const insertions: Array<{ routeIdx: number; pos: number; extraCost: number }> = [];

      for (let rIdx = 0; rIdx < routes.length; rIdx++) {
        const res = findBestInsertion(cust, routes[rIdx], depot, capacity, customerMap);
        if (res) {
          insertions.push({ routeIdx: rIdx, pos: res.index, extraCost: res.extraCost });
        }
      }

      const newRouteRes = findBestInsertion(cust, [], depot, capacity, customerMap);
      if (newRouteRes) {
        insertions.push({ routeIdx: routes.length, pos: 0, extraCost: newRouteRes.extraCost + 50 });
      }

      insertions.sort((a, b) => a.extraCost - b.extraCost);

      if (insertions.length === 0) continue;

      const bestCost = insertions[0].extraCost;
      const secondBestCost = insertions.length > 1 ? insertions[1].extraCost : bestCost + 200;
      const regret = secondBestCost - bestCost;

      if (regret > maxRegret) {
        maxRegret = regret;
        bestCustIndex = cIdx;
        selectedRouteIndex = insertions[0].routeIdx;
        selectedInsertPos = insertions[0].pos;
      }
    }

    if (bestCustIndex !== -1) {
      const custId = unassigned.splice(bestCustIndex, 1)[0];
      if (selectedRouteIndex >= routes.length) {
        routes.push([custId]);
      } else {
        routes[selectedRouteIndex].splice(selectedInsertPos, 0, custId);
      }
    } else {
      const custId = unassigned.shift()!;
      routes.push([custId]);
    }
  }

  return routes.filter(r => r.length > 0);
}

export async function runSolverStream(
  instance: VRPTWInstanceData,
  params: SolverParamsOptions,
  getGeminiClient: () => GoogleGenAI | null,
  onMessage: (msg: ProgressMessage) => void,
  isCancelled: () => boolean
) {
  const startTime = Date.now();
  const { depot, customers, capacity } = instance;
  const customerMap = new Map<number, Customer>();
  customers.forEach(c => customerMap.set(c.id, c));
  customerMap.set(depot.id, depot);

  onMessage({
    type: 'start',
    message: `Loaded instance ${instance.name}. Starting ${params.algorithm.toUpperCase()} solver for ${params.iterations} iterations.`
  });

  // Construct initial solution
  let currentSol = createInitialSolution(instance);
  let bestSol = currentSol;

  const totalCustomersCount = customers.length;
  let temp = 100.0;
  const coolingFactor = Math.pow(0.01 / 100.0, 1.0 / Math.max(10, params.iterations));

  onMessage({
    type: 'progress',
    iteration: 0,
    bestDistance: bestSol.totalDistance,
    bestVehicles: bestSol.totalVehicles,
    routes: bestSol.routes,
    computationTimeMs: Date.now() - startTime,
    message: `[LNS:INITIAL] Created initial solution: ${bestSol.totalDistance.toFixed(2)} (${bestSol.totalVehicles} vehicles)`
  });

  for (let iter = 1; iter <= params.iterations; iter++) {
    if (isCancelled()) break;

    let destroyType = 'Random Destroy';
    let removedCustomerIds: number[] = [];
    let currentRouteIds = currentSol.routes.map(r => [...r.customerIds]);

    const isLLMTurn = params.llmThreshold && params.llmThreshold > 0 && iter % params.llmThreshold === 0;

    if (isLLMTurn) {
      destroyType = 'LLM Destroy';
      onMessage({
        type: 'progress',
        iteration: iter,
        bestDistance: bestSol.totalDistance,
        bestVehicles: bestSol.totalVehicles,
        routes: bestSol.routes,
        computationTimeMs: Date.now() - startTime,
        message: `[LLM:TRIGGER] Querying Gemini LLM to analyze and destroy sub-optimal routes...`
      });

      try {
        const client = getGeminiClient();
        if (client) {
          const routesDesc = currentSol.routes.map(r => `Vehicle ${r.vehicleId}: Distance = ${r.distance}, Customers = [${r.customerIds.join(', ')}]`).join('\n');
          const prompt = `Select 2 vehicle routes (by vehicleId) from this VRPTW solution to destroy:\n${routesDesc}\nReturn JSON array of vehicle IDs to destroy.`;
          
          const response = await client.models.generateContent({
            model: 'gemini-3.6-flash',
            contents: prompt,
            config: {
              responseMimeType: 'application/json',
              responseSchema: {
                type: Type.ARRAY,
                items: { type: Type.INTEGER }
              }
            }
          });

          const vehicleIds: number[] = JSON.parse(response.text || '[]');
          if (vehicleIds.length > 0) {
            const destroySet = new Set(vehicleIds);
            const keptRoutes: number[][] = [];
            for (const r of currentSol.routes) {
              if (destroySet.has(r.vehicleId)) {
                removedCustomerIds.push(...r.customerIds);
              } else {
                keptRoutes.push([...r.customerIds]);
              }
            }
            if (removedCustomerIds.length > 0) {
              currentRouteIds = keptRoutes;
              onMessage({
                type: 'progress',
                iteration: iter,
                bestDistance: bestSol.totalDistance,
                bestVehicles: bestSol.totalVehicles,
                routes: bestSol.routes,
                computationTimeMs: Date.now() - startTime,
                message: `[LLM:DECISION] Gemini destroyed vehicles [${vehicleIds.join(', ')}] freeing ${removedCustomerIds.length} customers.`
              });
            }
          }
        }
      } catch (err: any) {
        onMessage({
          type: 'progress',
          iteration: iter,
          bestDistance: bestSol.totalDistance,
          bestVehicles: bestSol.totalVehicles,
          routes: bestSol.routes,
          computationTimeMs: Date.now() - startTime,
          message: `[LLM:FALLBACK] Gemini API unavailable (${err.message || err}). Falling back to heuristic destroy.`
        });
      }
    }

    if (removedCustomerIds.length === 0) {
      // Pick heuristic destroy operator
      const rVal = Math.random();
      if (rVal < 0.4) {
        destroyType = 'Random Destroy';
        const numRemove = Math.max(2, Math.min(Math.floor(totalCustomersCount * 0.2), 15));
        const allIds = customers.map(c => c.id);
        // Shuffle and pick
        for (let i = allIds.length - 1; i > 0; i--) {
          const j = Math.floor(Math.random() * (i + 1));
          [allIds[i], allIds[j]] = [allIds[j], allIds[i]];
        }
        removedCustomerIds = allIds.slice(0, numRemove);
        const removeSet = new Set(removedCustomerIds);
        currentRouteIds = currentRouteIds.map(r => r.filter(id => !removeSet.has(id))).filter(r => r.length > 0);
      } else if (rVal < 0.7) {
        destroyType = 'Shaw Destroy';
        const numRemove = Math.max(2, Math.min(Math.floor(totalCustomersCount * 0.2), 15));
        const seedCust = customers[Math.floor(Math.random() * customers.length)];
        const scored = customers.map(c => {
          const dist = Math.hypot(c.x - seedCust.x, c.y - seedCust.y);
          const timeDiff = Math.abs(c.readyTime - seedCust.readyTime);
          return { id: c.id, score: dist + timeDiff * 0.1 };
        });
        scored.sort((a, b) => a.score - b.score);
        removedCustomerIds = scored.slice(0, numRemove).map(s => s.id);
        const removeSet = new Set(removedCustomerIds);
        currentRouteIds = currentRouteIds.map(r => r.filter(id => !removeSet.has(id))).filter(r => r.length > 0);
      } else {
        destroyType = 'Route Destroy';
        if (currentRouteIds.length > 1) {
          const victimIdx = Math.floor(Math.random() * currentRouteIds.length);
          removedCustomerIds = [...currentRouteIds[victimIdx]];
          currentRouteIds.splice(victimIdx, 1);
        } else {
          destroyType = 'Random Destroy';
          removedCustomerIds = [customers[Math.floor(Math.random() * customers.length)].id];
          const removeSet = new Set(removedCustomerIds);
          currentRouteIds = currentRouteIds.map(r => r.filter(id => !removeSet.has(id))).filter(r => r.length > 0);
        }
      }
    }

    // Repair candidate solution
    let repairedRoutes = repairRegret2(removedCustomerIds, currentRouteIds, depot, capacity, customerMap);

    // Apply local search (2-opt & relocate)
    repairedRoutes = localSearch2Opt(repairedRoutes, depot, capacity, customerMap);
    repairedRoutes = localSearchRelocate(repairedRoutes, depot, capacity, customerMap);

    // Evaluate candidate solution
    const candidateRouteData: RouteData[] = repairedRoutes.map((cIds, idx) => {
      const r = evaluateRoute(cIds, depot, customerMap);
      r.vehicleId = idx + 1;
      return r;
    });

    const candidateDist = Math.round(candidateRouteData.reduce((sum, r) => sum + r.distance, 0) * 100) / 100;
    const candidateVehicles = candidateRouteData.length;

    const candidateSol: SolutionData = {
      routes: candidateRouteData,
      totalDistance: candidateDist,
      totalVehicles: candidateVehicles,
      isFeasible: true
    };

    const delta = candidateDist - currentSol.totalDistance;
    let accept = false;
    let acceptReason = '';
    let acceptCategory = 'LNS:REJECT';

    if (candidateVehicles < currentSol.totalVehicles) {
      accept = true;
      acceptReason = `Reduced vehicle count to ${candidateVehicles}`;
      acceptCategory = 'LNS:ACCEPT';
    } else if (delta < -0.01) {
      accept = true;
      acceptReason = `Improved distance by ${(-delta).toFixed(2)}`;
      acceptCategory = 'LNS:ACCEPT';
    } else if (params.algorithm === 'sa' || params.algorithm === 'lns') {
      const p = Math.exp(-delta / Math.max(0.001, temp));
      if (Math.random() < p) {
        accept = true;
        acceptReason = `Accepted candidate solution (delta: +${delta.toFixed(2)}, T: ${temp.toFixed(2)}, P: ${(p * 100).toFixed(1)}%)`;
        acceptCategory = 'SA:DECISION';
      } else if (destroyType === 'Random Destroy' || destroyType === 'Route Destroy') {
        accept = true;
        acceptReason = `Always accept ${destroyType} to explore new solution space (random walk)`;
        acceptCategory = 'LNS:ACCEPT';
      }
    }

    if (accept) {
      currentSol = candidateSol; // Random walk state evolution
    }

    let isNewBest = false;
    if (candidateVehicles < bestSol.totalVehicles || (candidateVehicles === bestSol.totalVehicles && candidateDist < bestSol.totalDistance - 0.01)) {
      bestSol = candidateSol;
      isNewBest = true;
    }

    temp *= coolingFactor;

    // Report progress
    if (isNewBest) {
      onMessage({
        type: 'progress',
        iteration: iter,
        bestDistance: bestSol.totalDistance,
        bestVehicles: bestSol.totalVehicles,
        routes: bestSol.routes,
        computationTimeMs: Date.now() - startTime,
        message: `[LNS:DECISION] [NEW BEST] Iteration ${iter}: Found better solution! Distance: ${bestSol.totalDistance.toFixed(2)} (${bestSol.totalVehicles} vehicles)`
      });
    } else if (iter % 10 === 0 || isLLMTurn) {
      onMessage({
        type: 'progress',
        iteration: iter,
        bestDistance: bestSol.totalDistance,
        bestVehicles: bestSol.totalVehicles,
        routes: bestSol.routes,
        computationTimeMs: Date.now() - startTime,
        message: `[${acceptCategory}] Iteration ${iter}/${params.iterations}: ${destroyType} -> ${acceptReason || 'Candidate rejected (kept current state)'}`
      });
    }

    // Yield control to event loop every few iterations to keep server responsive and support streaming
    if (iter % 5 === 0) {
      await new Promise(resolve => setTimeout(resolve, 5));
    }
  }

  onMessage({
    type: 'result',
    bestDistance: bestSol.totalDistance,
    bestVehicles: bestSol.totalVehicles,
    routes: bestSol.routes,
    computationTimeMs: Date.now() - startTime,
    message: `Optimization complete! Best Distance: ${bestSol.totalDistance.toFixed(2)} with ${bestSol.totalVehicles} vehicles.`
  });
}
