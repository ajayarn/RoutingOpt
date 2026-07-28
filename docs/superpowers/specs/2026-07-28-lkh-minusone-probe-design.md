# LKH3 Minus-One Vehicle Probe — Design Spec

## Problem

`solver/main.go`'s stagnation intervention, when `-use-lkh` is enabled, hands
the destroyed customer set to `invokeLKHSubSolver` with a fixed
`maxVehicles = len(finalDestroyIDs)` — exactly the number of routes that were
just destroyed. LKH3 is never asked whether the same customers could fit into
*one fewer* vehicle, so the stagnation solver can only ever hold the vehicle
count steady or, indirectly, let the outer `tryRouteElimination` machinery
reduce it separately. Observed symptom (user's R204 run, LKH3 toggle on): the
solver reached 4 vehicles, the stagnation solver reduced that to 3, and never
reached the best-known 2 — and nothing in the solver log explained why the
attempt to go lower didn't happen, because no such attempt was ever made.

## Goal

On the first stagnation attempt of each trigger, before falling back to
today's `maxVehicles` call, ask LKH3 whether `maxVehicles - 1` is feasible for
the same destroyed customer set. If it is, use that result directly (skip the
`maxVehicles` call entirely — it would only be a worse candidate). If it
isn't, fall through to exactly today's behavior. Make both the probe attempt
and its outcome visible in the solver log, directly addressing "I cannot see
this in the logs."

## Design

### Where it fires

Inside the existing `if *useLKH` block in the stagnation-intervention section
of `main()`'s loop, at the call site currently reading:

```go
lkhSol := invokeLKHSubSolver(destroyedCustomers, depot, capacity, customerMap, len(finalDestroyIDs))
```

This sits inside the stagnation solver's `attempt` loop (up to 3 escalating
attempts per trigger). The probe fires **only when `attempt == 1`** — per
your decision, to bound the added LKH3 call cost to at most one extra solve
per stagnation trigger, not per attempt.

### Call sequence

1. If `attempt == 1` and `len(finalDestroyIDs) > 1` (a probe asking for 0
   vehicles is meaningless — skip it and fall straight to the existing call):
   - Log `LKH:PROBE`: attempt number, probe vehicle count
     (`len(finalDestroyIDs) - 1`), customer count, and the count being probed
     down from.
   - Call `invokeLKHSubSolver(destroyedCustomers, depot, capacity, customerMap, len(finalDestroyIDs)-1)`.
   - If it returns non-nil: log `LKH:PROBE-SUCCESS` (probe vehicle count,
     original count) and use this result as `lkhSol` directly — the
     `maxVehicles` call is skipped for this attempt.
   - If it returns nil: log `LKH:PROBE-FAILED` (probe vehicle count) and
     continue to step 2.
2. If `lkhSol` is still unset (either `attempt != 1`, or the probe wasn't
   attempted, or it failed): run exactly today's call and logging
   (`LKH:TRIGGER` → `invokeLKHSubSolver(..., len(finalDestroyIDs))`).
3. Existing logic downstream (`LKH:SUCCESS`/`LKH:FALLBACK`, feasibility
   re-validation via `calculateRouteDetails` inside `invokeLKHSubSolver`,
   acceptance) is completely unchanged — it only ever sees a single `lkhSol`
   value, however it was obtained.

### No changes to `invokeLKHSubSolver`, `buildLKHInstanceText`, or either
`runLKHSolver` implementation

`maxVehicles` is already a plain parameter threaded through as the LKH3
`VEHICLES` cap; passing `len(finalDestroyIDs)-1` needs no signature or
plumbing change. Feasibility re-validation is identical regardless of which
`maxVehicles` value produced the candidate — LKH3's soft-penalty output is
never trusted either way.

### Cost bound

At most one extra LKH3 sub-solve call per stagnation trigger (not per
attempt, not per iteration) — matches the existing `maxAttempts = 3` cost
philosophy already used elsewhere in the stagnation solver.

### Logging (the other half of the original complaint)

Four new log categories via the existing `sendProgressLog` mechanism:
`LKH:PROBE`, `LKH:PROBE-SUCCESS`, `LKH:PROBE-FAILED` — plus reuse of the
existing `LKH:TRIGGER`/`LKH:SUCCESS`/`LKH:FALLBACK` categories for the
fallback path. This makes the full reasoning chain ("tried 1, tried 2,
2 worked, skipped the 3-vehicle fallback" or "tried 1, failed, fell back to
2") visible in the same solver log the user already watches.

## Out of scope (YAGNI)

- No probing more than one vehicle lower (e.g. `maxVehicles - 2`) — if the
  first probe fails, the search space only gets harder, not easier, per the
  existing route-elimination design's timing note.
- No equivalent probe for the pure-Go stagnation path (when `-use-lkh` is
  off) — `buildInitialSolution`/the pure-Go sub-solver heuristic don't take a
  vehicle cap today; a separate follow-up if wanted.
- No CLI flag to toggle the probe independently of `-use-lkh` — it's a strict
  improvement on the existing LKH3 path with a small, fixed cost, so it's
  bundled with the existing toggle.
- No probing on attempts 2 or 3 — per your explicit choice, to keep the added
  cost to at most one extra call per trigger.

## Validation plan

1. Implement in `solver/main.go`, rebuild `solver_bin` via `./build_solver.sh`.
2. Add a unit test exercising the call-sequence logic in isolation (the
   probe/fallback branching), since the actual LKH3 subprocess call can't be
   unit-tested directly — follow the existing pattern in
   `solver/route_elimination_test.go` for testing pure decision logic without
   invoking the real binary.
3. Re-run R204 with `-use-lkh` on and the same seed the user used; confirm
   the log now shows `LKH:PROBE*` entries and check whether the probe reaches
   2 vehicles where the unprobed run stalled at 3.
4. Rebuild browser WASM artifacts (`./build_lkh_wasm.sh && ./build_solver_wasm.sh`)
   once validated, since the deployed app is what the user actually tests
   against.
