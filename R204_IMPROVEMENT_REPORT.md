# R204 result-improvement session report

**Goal:** get the Go/WASM LNS solver within 3% of R204's best-known solution (2 vehicles,
825.52 distance) in a 10-minute run, editing solver logic freely, LKH-3 usage capped at ≤50% of
nodes per invocation, up to 2 hours of work.

**Result:** not met. Best achieved in a clean 10-minute run: **2 vehicles, 862.86 distance
(4.52% gap)**. This is a large improvement over the starting point, which never got a
qualifying answer at all (see below) — but it falls short of the 3% target.

All work happened on the `result-improvement` branch. LKH-3 was never invoked in the final
configuration (see "LKH-3" section for why).

## 1. Establishing the target

R204's `.txt` has no distance total, only the two best-known routes. Parsed
`public/data/r204.txt` and computed the exact Euclidean length of the given routes to confirm
the target: **825.5193** total distance, 2 vehicles, all 100 customers covered exactly once —
matching the value already hardcoded in `src/App.tsx`'s `BEST_KNOWN_SOLUTIONS.r204`. 3% of that
is a ceiling of ~850.3 distance, at 2 vehicles (vehicle count is the primary, hierarchical
objective — a 3-vehicle answer isn't "close" to a 2-vehicle best-known regardless of its raw
distance).

Also computed R204's structural numbers up front, since they shaped everything downstream:
total demand 1458, capacity 1000/vehicle → **capacity-only lower bound is 2 vehicles** (matches
best-known exactly), and average time-window width is 751 out of a 1000 horizon — very loose.
Capacity was never going to be the blocker; a 2-vehicle answer should be very reachable on time
windows alone.

## 2. Baseline: what the solver did before any changes

Ran the unmodified solver natively (`solver_bin`) with a hard 10-minute wall-clock cutoff
(`-iterations` set effectively unbounded, process killed at 600s — a native-binary proxy for a
"10-minute run," see caveat at the end).

**Result: stuck at 3 vehicles, ~779.6 distance at 10 minutes**, barely moving to 776.8 even
after 38 minutes left running for reference. A 3-vehicle answer fails the goal outright,
regardless of distance, since vehicle count is the primary objective.

Reading `solver/main.go` explained why immediately:

- `runVehicleMinimizationPrePhase` (the up-front routine meant to shed vehicles while the
  solution is still loose) **gave up permanently after a single failed elimination attempt**,
  even though its own comment already flagged this as arguably wrong ("this is NOT proof no
  further reduction is possible... stopping here is a deliberate cost bound, not a correctness
  guarantee"). Whatever vehicle count construction landed on, minus at most one lucky
  elimination, was final — the prephase practically never used its real budget.
- **No 2-opt, no Or-opt, no local search of any kind existed anywhere in the Go solver.**
  Destroy/repair only ever appended customers at their cheapest immediate insertion point, with
  nothing to untangle a route afterward. For R204 specifically — 2-3 routes of 40-50 customers
  each, not 10+ routes of ~10 like C101 — that is a much bigger deal: a single big route left
  in whatever order greedy insertion built it in has a lot of easy, unclaimed distance sitting
  in it.

## 3. Fixes made, in the order I made them

### 3.1 Fixed the vehicle-minimization pre-phase giving up after one failure

Changed `runVehicleMinimizationPrePhase` to keep retrying (each retry gets a different
insertion order for free, since Go's map iteration is runtime-randomized even under a fixed
seed) instead of returning on the first failure. Added a `vehicleMinMaxConsecutiveFailures`
cap (see 3.5 for why this number moved around) so a huge `-iterations`-derived budget can't spin
forever on a genuinely-stuck case.

### 3.2 Added 2-opt and Or-opt local search (didn't exist at all before)

- `twoOptRoute` / `twoOptImproveSolution`: classic intra-route 2-opt (reverse a segment, keep it
  if the reversal is both feasible — re-checked via the existing `calculateRouteDetails`, since
  time windows can flip on a reorder even when raw distance improves — and strictly better).
- `orOptImproveSolution`: relocates a single customer to its best feasible slot anywhere,
  including a different route entirely (the "cross-route relocate" `DESIGN.md` already noted
  the unused TS engine has and the Go engine didn't).
- `localSearchImprove`: alternates the two to a local optimum.

Wired in at every point a solution gets constructed or repaired: right after initial
construction, after the vehicle-min prephase, after every accepted main-loop candidate, inside
`tryRouteElimination`'s successful result, and on the pure-Go stagnation sub-solver's output.

**Bug caught by my own test for this:** `orOptImproveSolution` could relocate every customer
out of a route, leaving a **phantom zero-customer route still counted toward `TotalVehicles`**
— silently inflating the primary objective. `TestOrOptImproveSolutionRelocatesAcrossRoutes`
caught this immediately (merging three customers into one route was the *correct* move in my
test fixture, but left an empty second route in the output). Fixed by pruning empty routes and
reindexing before returning. This means Or-opt can now also discover route elimination as a
free side effect, not just distance improvements.

Tests added: `solver/localsearch_test.go` (2-opt untangles a hand-built crossing route,
cross-route relocation, and a broader "never worsens distance / never drops or duplicates a
customer" smoke test).

### 3.3 Interleaved 2-opt into the insertion loop itself

Local search alone (applied once, after repair) didn't fix vehicle count — still stuck at 3.
Root cause: `repairGreedyNoNewRoute` inserts customers into a route one at a time in whatever
order the route currently has, and a route left in an untuned order can be so time-window-tight
that the *next* customer has nowhere feasible to go, even though a reordered version of the
same route would have room. Fixed by calling `twoOptRoute` on the target route immediately
after each single insertion, not once at the end. This measurably improved distance a lot in
isolated testing (e.g. one config went from 932 → 828 for a 3-vehicle answer) but, on its own,
still didn't unlock 2 vehicles.

### 3.4 Added a "shuffle" escalation to route elimination, then diagnosed why it still failed

Added `eliminateOneRoute`: if directly reinserting an evicted route's customers into the
survivors fails, free a random slice of the *survivors'* own customers too and retry — giving
the search room to reshuffle both sides, not just insert into an immovable skeleton.

To find out whether this needed a bigger budget or was fighting a lost cause, I wrote a
throwaway experiment harness (`solver/r204_experiment_test.go`, deleted before the final
commit) that loads the real R204 data directly and times `tryRouteElimination` / individual
`eliminateOneRoute` calls without going through the full CLI binary — seconds per iteration
cycle instead of minutes.

**Finding:** after construction + local search, R204 reliably lands on a 3-route split of
47/40/13 customers, loads 628/647/183 out of 1000 capacity — genuinely *not* capacity-bound
(628+183=811 and 647+183=830 both fit easily under 1000). Tried eliminating **all three routes
individually**, each with up to 40 shuffle retries churning up to 30% of the survivors: **every
single one failed.** This is a real structural dead end for that specific 47/40/13 partition —
more retries against the same partition were never going to fix it, since it's not bad luck,
it's the wrong partition. Best-known's 2-route split (48/52 customers) is a genuinely different
customer-to-route assignment that these local moves (insert, 2-opt, single-customer relocate)
can't reach from this particular starting point by construction.

### 3.5 Performance correction: the shuffle escalation was eating the whole time budget

Embedding a several-seconds-per-call operation (3.4) inside a 200-consecutive-failure retry
loop (3.1) meant the vehicle-min prephase alone could burn most or all of the 10-minute budget
retrying an elimination that step 3.4 had already shown can't succeed on this partition —
before the main LNS loop (which explores genuinely different partitions via unrestricted
destroy/repair) ever got to run. A real 10-minute run got through only ~1 progress line in the
first ~3 minutes. Fixed by dialing the retry/shuffle budgets back down
(`vehicleMinMaxConsecutiveFailures` 200→10, `routeEliminationShuffleBudget` 40→5) so a failing
case gives up quickly and hands the remaining budget to the main loop, which has the actual
diversity-generating power (it isn't restricted to insertion-only, whole-route moves).

## 4. Final result

With all of the above, a clean 10-minute native run (`-iterations` unbounded, killed at 600s):

| | Vehicles | Distance | Gap |
|---|---|---|---|
| Best known | 2 | 825.52 | — |
| Baseline (before this session) | 3 | ~779.6 | disqualified (wrong vehicle count) |
| **After fixes** | **2** | **862.86** | **4.52%** |

The main LNS loop (not the prephase) found the first 2-vehicle answer partway through the run
and kept tightening it via 2-opt/Or-opt for the remainder. Getting to 2 vehicles at all is the
headline change — the baseline never produced a qualifying answer in 10 minutes at any point in
this session (even 38 minutes wasn't enough for it). 4.52% is short of the 3% target but a
large step from "disqualified."

## 5. LKH-3

Not used in the final configuration. The constraint (≤50% of nodes per invocation) turned out
to be awkward for R204 specifically: with only 2-3 routes total and each already 13-47% of all
100 customers, using LKH-3 on even one route approaches the cap, and any pairwise route merge
(the natural elimination move) exceeds it outright. A workable use — LKH-3 on the single
smallest route plus a capped subset of survivors — is plausible but wasn't implemented in the
remaining time. This is the clearest concrete follow-up.

## 6. What I'd try next (out of time, not out of ideas)

- **Construction-time fix, not post-hoc elimination.** The real issue is that the I1 sequential
  insertion heuristic (which replaced K-means clustering in the previous session, correctly, for
  C101-style clustered/tight-capacity instances) has no notion of "keep routes big" for
  loose-time-window, high-capacity instances like R204. It closes a route once no unrouted
  customer fits *by its c1/c2 criteria*, not once capacity is actually exhausted. A
  capacity-utilization-aware bias (or re-trying construction with a couple of different seed
  strategies and keeping whichever needs fewer vehicles) would attack the root cause instead of
  patching around a bad partition after the fact.
- **Exchange/swap moves, not just insertion**, for route elimination — a plain insert can only
  ever add load/time to the target route; a swap (trade one customer from the evicted route for
  one from a survivor) can resolve time-window deadlocks insertion structurally cannot.
- Given more time budget, use LKH-3 as sketched in §5, sized to respect the node cap.

## Caveat on "10-minute run"

The app's `-iterations` UI knob (default 1000) doesn't map cleanly to wall-clock time, and the
WASM build (what actually ships to the browser) is slower than the native `solver_bin` used for
all testing here. All timings in this report are native-binary wall-clock, used as a proxy for
"10 minutes of solver compute" — real in-browser WASM runs will process fewer iterations in the
same 10 minutes, so the reported gap should be treated as an optimistic bound, not a guarantee
of what the deployed app will show.
