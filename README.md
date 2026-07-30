# RoutingOpt

A Large Neighborhood Search (LNS) solver for the Vehicle Routing Problem with Time Windows
(VRPTW), written in Go, compiled to WebAssembly, and run entirely client-side in a browser Web
Worker. No backend, no cloud inference — the whole optimization runs on whatever machine has the
tab open.

This document is for anyone with an operations-research background who wants to understand the
algorithm, poke holes in it, or fork the repo and improve it. It describes the *solver design*,
not the web app plumbing — see [CLAUDE.md](CLAUDE.md) for the engineering/architecture side (WASM
build pipeline, Web Worker internals, LKH3 vendoring, deployment).

**Live demo:** https://ajayarn.github.io/RoutingOpt/ (deploys from `main`; this document describes
the `result-improvement` branch, which only removes dead code and fixes non-algorithmic bugs on
top of `main` — the LNS/construction/LKH3 logic itself is identical between the two, so the demo
reflects everything described below regardless of merge status)
**Solver source:** [`solver/main.go`](solver/main.go) (~2000 lines, single file, no external Go
dependencies)

## Problem

Standard Solomon-format VRPTW: a depot, a set of customers each with `(x, y)`, `demand`,
`[readyTime, dueDate]`, and `serviceTime`, and a fleet of identical vehicles with a fixed
`capacity`. A route is feasible iff:

- **Capacity**: total demand on the route ≤ vehicle capacity
- **Time windows**: at every customer, `arrival + waiting ≤ dueDate` (arriving early just means
  waiting until `readyTime`; arriving after `dueDate` is infeasible, no soft violations)

The objective is **hierarchical / lexicographic**:

1. Minimize the number of vehicles (routes) used
2. Subject to (1), minimize total distance (Euclidean, double precision)

This is the standard formulation for the Solomon/Homberger 100-customer benchmark set, and it's
what every acceptance/comparison decision in this solver is keyed on — a solution with fewer
vehicles is *always* preferred over one with more, regardless of distance.

Every route the solver ever produces is validated against both constraints before it's accepted
anywhere — see [Feasibility](#feasibility) below. There is no soft-penalty relaxation anywhere in
the Go code; the one place a soft-penalty model *is* in play (LKH3) is explicitly walled off and
re-validated (see [LKH3 sub-solver](#lkh3-sub-solver-optional)).

## Pipeline overview

```mermaid
flowchart TD
    A[Parse Solomon instance] --> B["Construction:<br/>Solomon I1 sequential insertion"]
    B --> C["Local search polish<br/>(2-opt + Or-opt to convergence)"]
    C --> D["Vehicle-minimization pre-phase<br/>(route elimination, budget = 10% of iterations)"]
    D --> E["Local search polish"]
    E --> F[Main LNS loop]
    F --> G{Stagnated for<br/>N iterations?}
    G -- no --> F
    G -- yes --> H[Stagnation intervention]
    H --> F
    F --> I{Iteration budget<br/>exhausted?}
    I -- no --> F
    I -- yes --> J[Return best solution found]

    style B fill:#dbeafe
    style C fill:#dbeafe
    style D fill:#fef3c7
    style E fill:#dbeafe
    style F fill:#dcfce7
    style H fill:#fce7f3
```

Every stage after construction only ever *keeps* a candidate solution if it is feasible and at
least as good under the hierarchical objective — nothing downstream can make the solution worse
in a way that survives.

## Construction: Solomon I1 sequential insertion

`buildInitialSolution` (Solomon, 1987) — no clustering pre-step. Routes are built one at a time
from the full unrouted pool:

1. **Seed** the route with the unrouted customer farthest from the depot (`selectSeedCustomer`).
2. **Fill** the route by repeatedly picking, among all unrouted customers, the one that maximizes
   a two-part insertion criterion:
   - **c1** (`i1c1`): for a given customer and candidate route, the cheapest feasible insertion
     position, scored by an incremental-distance + time-window-shift measure, minimized over
     positions.
   - **c2**: `λ · distance(depot, customer) − c1`, maximized over customers — this is a *regret*
     term that prioritizes customers far from the depot when there's a choice, since those are
     the ones most likely to become infeasible-to-insert if left for a later, farther-away route.
     `λ = 2.0` (`i1Lambda` in the code), a fixed constant rather than tuned per instance.
3. Repeat until no unrouted customer fits feasibly anywhere in the route (checked via
   `calculateRouteDetails`, the same hard feasibility function used everywhere else), then close
   the route and start a new one with a fresh seed.

No customer is ever pre-assigned to a cluster/route by geography before insertion — a customer
that doesn't fit the current route remains eligible for *any* later route. On the Solomon C101
benchmark this produces a feasible initial solution well under the naive-nearest-neighbor baseline
of ~28 routes / ~2806 distance; see [Results](#results) for what the full pipeline achieves.

## Local search: 2-opt + Or-opt to convergence

`localSearchImprove` alternates two intensification operators until a full round of both produces
no further distance improvement (or a 5-round cap is hit):

- **2-opt** (`twoOptRoute`, intra-route): standard edge-pair reversal to remove crossing edges
  within a single route.
- **Or-opt** (`orOptImproveSolution`, cross-route): relocates short customer segments between
  routes.

These are run alternately, not just once each, because a move from one can re-open an opportunity
for the other — an Or-opt relocation can leave a route in a shape 2-opt can now untangle further,
and vice versa. This runs after construction, after the vehicle-minimization pre-phase, and after
every accepted destroy/repair candidate in the main loop.

## Vehicle-minimization pre-phase

Because the objective is hierarchical (vehicles first), and the main LNS loop below spends most of
its effort on distance, there's a dedicated pre-phase (`runVehicleMinimizationPrePhase`) that tries
to shed routes *before* the main loop starts, while the solution is still "loose" from construction
and hasn't been locked into a tight, hard-to-restructure distance-optimized shape yet. Budgeted at
10% of the total `-iterations`.

The core primitive is **route elimination** (`tryRouteElimination` → `eliminateOneRoute`):

1. Rank routes by "easiest to eliminate" (fewest customers, then lowest total demand —
   `selectWeakestRoutes`).
2. For each candidate route (up to a small `maxAttempts`), evict all its customers
   (`destroyRouteElimination`) and try to reinsert them into the *other* routes only —
   `repairGreedyNoNewRoute` never opens a new route, so success here is a genuine vehicle-count
   reduction.
3. **If the direct reinsertion fails** (measured on the R204 instance: this happens 200/200 times
   once routes are near the capacity-lower-bound fleet size — "no slack anywhere in either fixed
   survivor order" is a real structural dead end, not bad luck), fall back to also freeing a random
   slice of the *survivors'* own customers back into the same repair pass, giving the search room
   to reshuffle both sides at once instead of inserting into an immovable skeleton
   (`routeEliminationShuffleBudget = 5` retries).
4. Stop when vehicle count hits the capacity lower bound (`minVehiclesLowerBound` — a closed-form
   bin-packing bound: `⌈total demand / capacity⌉`, which time windows can only push up from, never
   below), the budget is exhausted, or `vehicleMinMaxConsecutiveFailures = 10` attempts in a row
   fail at the same vehicle count (a stuck partition should defer to the main loop's broader
   destroy/repair to reach a *different* customer-to-route split, rather than burn the whole budget
   retrying a proven dead end).

Route elimination is *also* available as a destroy operator inside the main loop (below) — the
pre-phase just runs it up front, unconditionally, before distance optimization narrows the search.

## Main LNS loop

```mermaid
flowchart TD
    Start([Current solution]) --> Choose{Pick destroy<br/>operator}
    Choose -- 20% --> RE[Route Elimination]
    Choose -- 40% --> WD["Worst Destroy<br/>(remove k customers,<br/>noised removal-cost ranking)"]
    Choose -- 40% --> RD["Random Destroy<br/>(remove k customers<br/>uniformly)"]

    RE --> ReRepair["repairGreedyNoNewRoute<br/>(reinsert, no new route)"]
    WD --> Repair["repairGreedy<br/>(reinsert, new route allowed)"]
    RD --> Repair

    ReRepair --> Polish2["localSearchImprove<br/>(2-opt + Or-opt)"]
    Repair --> Polish2

    Polish2 --> Accept{Accept?}
    Accept -- "fewer vehicles,<br/>OR same vehicles + shorter" --> Keep[Candidate becomes current]
    Accept -- "worse, but destroy<br/>type = Random" --> Keep
    Accept -- otherwise --> Reject[Discard candidate]

    Keep --> Best{New global best?}
    Best -- yes --> UpdateBest[Update best solution<br/>reset stagnation counter]
    Best -- no --> Continue[Continue]
    Reject --> IncStag[Increment stagnation counter]

    UpdateBest --> Next([Next iteration])
    Continue --> Next
    IncStag --> Next
```

Each iteration: destroy → repair → local-search polish → accept/reject → check stagnation.
`k` (customers removed per iteration, for Worst/Random) is drawn uniformly from
`[max(2, 5% of customers), max(5, 30% of customers)]` each iteration.

### Destroy operators (`chooseDestroyOperator`)

| Operator | Probability | Mechanism |
|---|---|---|
| **Route Elimination** | 20% | Same primitive as the pre-phase — try to empty one whole weak route into the others. |
| **Worst Destroy** | 40% | Remove the `k` customers whose removal saves the most route distance (`destroyWorst`), with random noise added to the ranking so it isn't perfectly greedy every time. |
| **Random Destroy** | 40% | Remove `k` uniformly random customers (`destroyRandom`). |

### Repair

**Greedy insertion** (`repairGreedy`): each removed customer is reinserted at its single cheapest
feasible `(route, position)` across all existing routes, or into a brand-new route if nothing
fits. Route Elimination candidates instead go through `repairGreedyNoNewRoute` (same greedy
insertion, but a new route is never opened — that would defeat the point of trying to eliminate
one).

### Acceptance criterion

This is a **best-improvement-with-forced-diversification** rule, not a simulated-annealing /
Metropolis criterion:

- Always accept if the candidate uses **fewer vehicles**.
- Otherwise accept if vehicle count is unchanged and **distance improved**.
- Otherwise, **always accept anyway if the destroy operator was Random Destroy** — this is the
  solver's sole mechanism for escaping local optima. (Worst Destroy and Route Elimination
  candidates that don't improve are simply discarded.)

There's no cooling schedule, no acceptance probability, no tabu list. See
[Limitations](#known-limitations--places-to-improve) for what this trades away.

### Stagnation intervention

If `stagnationCounter` (consecutive non-improving iterations) reaches `-llm-threshold` (default
20; the flag name is a legacy misnomer, see below), a heavier intervention fires:

1. **Select routes to destroy** (`selectStagnationRoutesHeuristically`): rather than a random or
   worst-distance pick, this computes each route's geographic centroid and preferentially selects
   *spatially overlapping* routes — the intuition being that overlapping routes are the most
   likely to have an inefficient customer-to-vehicle assignment that a purely-local destroy/repair
   pass would never untangle, because doing so requires moving many customers across two routes at
   once.
2. **Re-solve the freed customers as a subproblem**, via either:
   - the **pure-Go sub-solver** (default): I1 construction on just the destroyed customers, then
     50 sub-iterations of a small destroy/repair LNS on that subset, then a local-search polish; or
   - **LKH3** (`-use-lkh`), see below.
3. **Merge** the resolved subproblem's routes back with the untouched routes, and accept the merge
   only if it improves on the pre-intervention best.
4. If it doesn't improve, retry up to `maxAttempts = 3` times with different candidate routes
   (destroy-route selection excludes previously-tried combinations via a `history` list) before
   giving up and reverting to the pre-intervention solution.

### LKH3 sub-solver (optional)

`-use-lkh` swaps step 2 above for a call into [LKH3](http://webhotel4.ruc.dk/~keld/research/LKH-3/)
— a specialized, decades-refined TSP/VRP local-search solver — compiled either as a native binary
(CLI use) or to WebAssembly (browser use, via a small pool of pre-instantiated module instances;
see CLAUDE.md's "Client-side execution" section for why a pool).

Two things make this safe to bolt onto a hard-constraint solver even though **LKH3 internally uses
a soft violation-penalty model, not hard constraints**:

- **A hard vehicle cap.** Unlike the pure-Go sub-solver (which can always open a new route),
  LKH3's `VEHICLES` parameter is a hard partition cap. Before falling back to using exactly as
  many vehicles as were destroyed, the solver **probes `vehicles − 1`** on the first attempt of
  each stagnation trigger (`shouldProbeLKHMinusOne`) — if LKH3 can partition the destroyed
  customers into one fewer vehicle, that's a genuine, cheap vehicle-count reduction discovered
  essentially for free.
- **Zero trust in the output.** Every route LKH3 returns is re-run through
  `calculateRouteDetails` — the identical hard feasibility check every other part of the file
  uses — before it's allowed anywhere near the accepted solution. If validation fails for even one
  route, the entire LKH3 result for that call is discarded and the pure-Go sub-solver runs
  instead. LKH3 can never be the reason an infeasible solution reaches the output.

## Feasibility

`calculateRouteDetails(customerIDs, customers, depot, capacity) (Route, bool)` is the single
feasibility oracle every operator in the file goes through — construction, every destroy/repair,
every local-search move, and every LKH3-sourced route. It computes arrival/wait/departure times
and load in one pass and returns `ok=false` on the first capacity or time-window violation. There
is no other path by which a route enters a `Solution`. This is the property that makes it safe to
bolt a soft-penalty external solver (LKH3) onto an otherwise hard-constraint system.

## Results

| Instance | Best known | This solver | Gap | Run condition |
|---|---|---|---|---|
| C101 (clustered, loose time windows) | 10 vehicles / 828.94 | 10 vehicles / 828.94 | 0.00% | `-iterations 50`, `-seed 1` — solves in ~1s, well under a minute |
| R204 (random, wide time windows, 2-vehicle capacity-bound) | 2 vehicles / 825.52 | 2 vehicles / 862.86 | 4.52% | 10-minute wall-clock budget, `-use-lkh` on |

These two runs used very different budgets (a few dozen iterations vs. ten minutes), so don't read
the gap column as an apples-to-apples comparison across rows — each instance's own row is real, the
two rows aren't directly comparable to each other.

The C101 number is not a hardcoded target: best-known values live only in the frontend's display
table (`BEST_KNOWN_SOLUTIONS` in `src/App.tsx`) for showing the gap in the UI, and are never passed
into the solver — there is no early-stop-at-optimal feature anywhere in `solver/main.go` (an
earlier version of this codebase had one; it was removed and is not coming back via this repo's
`-optimal` flag, which doesn't exist). C101's loose time windows and clustered layout make it a
genuinely easy instance for I1 + LNS to reach optimality on — R204 (2-vehicle capacity bound, wide
time windows) is a much harder search space, hence the visible gap. See
[`R204_IMPROVEMENT_REPORT.md`](R204_IMPROVEMENT_REPORT.md) for a full session log of what was tried
against it (this is exactly the kind of instance where a smarter destroy operator or a proper
ALNS weight-learning scheme would likely help most — see below).

Results were not run in this pass across the full 56-instance Solomon/Homberger set bundled in
`public/data/` — that would be a natural next step for anyone forking this to benchmark
systematically.

## Known limitations / places to improve

This is deliberately not a from-the-literature textbook ALNS implementation, and there are several
places where a more principled approach would likely do better. Listed roughly in the order an OR
practitioner would probably want to attack them:

- **No adaptive operator weighting.** The 20/40/40 destroy-operator split is a fixed constant, not
  learned. A standard ALNS roulette-wheel weight update (reward operators that recently produced
  improvements, decay weights over time) is the most obvious structural upgrade — the codebase
  already logs enough per-iteration outcome data (`LNS:ACCEPT`/`LNS:REJECT`/`LNS:DECISION` in the
  solver console) to bootstrap this.
- **No principled acceptance criterion.** "Always accept Random Destroy, otherwise only accept
  strict improvements" is a crude diversification mechanism. A simulated-annealing-style
  Metropolis criterion (accept worse solutions with probability `exp(-Δ/T)`, cooling `T` over the
  run) is standard LNS/ILS practice and isn't implemented here.
- **No Shaw / relatedness-based removal.** Worst-distance and pure-random are the two removal
  operators; there's no removal operator that targets *related* customers (by distance + time
  window overlap + demand similarity — classic "Shaw removal"), which tends to create more
  promising repair opportunities than either extreme.
- **Stagnation route selection is a hand-tuned heuristic**, not derived from an established
  removal criterion — it clusters by geographic centroid overlap. This works well enough to be
  useful (see Results) but a Shaw-style relatedness measure incorporating time windows and demand,
  not just geography, would likely generalize better across instance types (R1/RC1 series vs. C1).
- **LKH3 WASM pool has a known intermittent bug** (documented in CLAUDE.md): an occasional
  Emscripten runtime error from one sub-solve call can leave the module pool unable to refill for
  the rest of a run, silently downgrading later stagnation interventions to the pure-Go sub-solver
  for the remainder of that run. Feasibility is unaffected (nothing infeasible can leak through
  regardless), but solve *quality* quietly degrades with no visible error. Root cause not yet
  isolated — see CLAUDE.md's "Client-side execution" section.
- **No parallelism or multi-start.** Single-threaded, single-trajectory LNS. Running several
  independent trajectories (different seeds) and keeping the best, or parallelizing the
  destroy/repair evaluation itself, isn't implemented.
- **Only Euclidean, static-instance VRPTW.** No support for asymmetric distances, multiple depots,
  heterogeneous fleets, or dynamic/online arrivals — matches the Solomon benchmark's scope, but is
  worth knowing if you're evaluating this against a different problem class.
- **`-llm-threshold`** (stagnation iteration count) and the frontend's `logFilter: 'llm'` key are
  both legacy misnomers from an earlier LLM-guided-destroy-operator design that was fully removed
  from this codebase; kept as-is rather than renamed to avoid touching the CLI-argv/UI wiring for
  a cosmetic change. `-use-lkh` is unrelated and still fully live.

## Running it locally

```bash
./build_solver.sh
./solver_bin -file public/data/c101.txt -iterations 1000
```

Flags: `-file` (Solomon instance path, required), `-iterations` (LNS iteration budget, default
1000), `-seed` (RNG seed, default 42), `-llm-threshold` (stagnation trigger threshold, default 20,
`0` disables it), `-use-lkh` (enable the LKH3 sub-solver, default `false`, requires `lkh_bin` —
see `./build_lkh.sh`). Output is one JSON object per line on stdout: `start`/`progress`/`result`/
`error` messages, the same protocol the browser Web Worker consumes.

Go unit tests (`solver/*_test.go`) cover construction correctness (every customer routed exactly
once, feasibility, determinism under a fixed seed), local search (never worsens, never drops
customers), route elimination (vehicle-count reduction, no customer-ID aliasing bugs), and the
stagnation/LKH decision helpers:

```bash
cd solver && go test ./...
```

`public/data/` bundles all 56 Solomon/Homberger 100-customer benchmark instances (c1/c2/r1/r2/
rc1/rc2 series) if you want to benchmark against something other than C101/R204.

## Contributing

Forks and PRs welcome, especially ones that:

- Replace the fixed destroy-operator weights with adaptive (ALNS-style) weighting
- Add a Shaw/relatedness removal operator
- Add a proper acceptance criterion (simulated annealing or similar) instead of the current
  always-accept-on-random-destroy rule
- Root-cause the LKH3 WASM pool exhaustion bug
- Run and publish a full 56-instance benchmark comparison

See [CLAUDE.md](CLAUDE.md) for the build/deploy pipeline (Go → WASM, LKH3 vendoring and its
Emscripten linking workaround, GitHub Pages deploy) if your change touches anything beyond
`solver/main.go` itself.
