# CLAUDE.md

Guidance for Claude Code when working in this repository.

## What this is

RoutingOpt is an interactive Vehicle Routing Problem with Time Windows (VRPTW) solver: a React
dashboard that runs a Large Neighborhood Search (LNS) metaheuristic (written in Go, compiled to
WebAssembly) **entirely client-side**, in a Web Worker, streaming live
optimization progress (routes, distance, vehicle count, solver log) to the UI via `postMessage`.
LKH3 (a C TSP/VRP solver) is available as an optional stagnation sub-solver, also compiled to
WASM.

**The app is fully static now — no backend required at all, not even for instance data.** It's
deployed to GitHub Pages at https://ajayarn.github.io/RoutingOpt/ (see "Deployment: GitHub Pages"
below). `server.ts`/Express still exists and still runs for local dev (`npm run dev`), but purely
as a dev-convenience wrapper around Vite - the frontend no longer calls any of its API routes for
anything on the critical path (see "API surface").

It was originally scaffolded by Google AI Studio (`metadata.json`, `majorCapabilities:
MAJOR_CAPABILITY_SERVER_SIDE_GEMINI_API`) and used Gemini, then local Ollama, as an LLM-guided
destroy-operator selector. **That entire LLM-destroy feature has been fully deleted** (not just
removed from the running app) — see "History: the removed Ollama LLM step" below if you're
wondering where it went.

The Go solver (`solver/main.go`) is the only solver implementation in this repo. It's compiled to
WASM (`public/wasm/solver.wasm`) and run in a browser Web Worker (`public/solverWorker.js`), and is
also still buildable as a native CLI binary (`solver_bin`, via `./build_solver.sh`) for standalone
testing/benchmarking outside the browser — nothing in the running app spawns this binary anymore.
It has a Solomon I1 sequential insertion heuristic for the initial solution, a pure-Go stagnation
heuristic (`selectStagnationRoutesHeuristically`) as its default destroy-selection mechanism when
stagnated, and an optional LKH3 sub-solver (see below).

An earlier, since-deleted `solver_engine.ts` held a second, TypeScript reimplementation of the
same problem (angular-sweep construction, 2-opt/relocate local search, Shaw/random/whole-route
destroy, regret-2 repair) — it was the original (accidental) default before the app was rewired to
run the Go solver, but nothing ever called it after that rewiring, so it was removed rather than
kept around as reference.

### LKH3 stagnation sub-solver (`-use-lkh`)

`solver/main.go` takes a `-use-lkh` flag (default `false`), exposed in the UI as the "Use LKH3 for
stagnation sub-solving" toggle. When a stagnation intervention fires, instead of (or in addition
to falling back on) the pure-Go I1+LNS sub-solver, it can hand the destroyed customers to
LKH3 — a C VRP/TSP solver — as `invokeLKHSubSolver` in `solver/main.go`. LKH3's output is never
trusted directly (it uses a soft violation-penalty model, not hard constraints); every returned
route is re-validated through `calculateRouteDetails`, the same feasibility check every other
insertion/repair decision in the file uses, and the whole result is discarded if anything fails
that check.

The one platform-specific piece of this — actually *running* LKH3 — is split behind a
`runLKHSolver(instanceText, seed) (string, bool)` function with two build-tagged implementations:
- **`solver/lkh_native.go`** (`!(js && wasm)`): writes an LKH `.par`/instance file pair to a real
  temp dir and spawns `./lkh_bin` as a subprocess, exactly like a traditional CLI tool would.
- **`solver/lkh_wasm.go`** (`js && wasm`): `GOOS=js` has no subprocess support and no real
  filesystem, so this instead calls a JS-global bridge the host page is expected to register —
  `globalThis.__lkhWasmSolve(instanceText, parText) -> string|null` — which
  `public/solverWorker.js` implements using a small pool of pre-instantiated LKH WASM module
  instances (see "Client-side execution" below for why a pool, not one shared instance).

`build_solver.sh` explicitly lists `solver/lkh_native.go` alongside `main.go` — `go build` with
explicit files (not a package/directory) doesn't auto-include sibling files, so this needs to stay
in sync if more platform-specific files are added. `build_solver_wasm.sh` does the same with
`solver/lkh_wasm.go` instead.

### LKH3's C source and the wasm-ld tentative-definition problem

`lkh3src/` (vendored LKH3 C source, **untracked** — not committed, likely due to LKH3's
license) is compiled two ways:
- `./build_lkh.sh` → native `lkh_bin`, via `lkh3src/Makefile` unmodified.
- `./build_lkh_wasm.sh` → `lkh_wasm.cjs`/`lkh_wasm.wasm` via Emscripten.

LKH3's headers (`LKH.h`, `Sequence.h`, `Genetic.h`, `gpx.h`) declare ~130 global variables as
plain **tentative definitions** with no `extern` (old C89 style), relying on every `.c` file that
includes them being merged into one "common" symbol per name at link time. wasm-ld doesn't support
common symbols at all ("duplicate symbol" errors). `lkh_wasm_prepare.py` (invoked by
`build_lkh_wasm.sh`) works around this without touching `lkh3src/` itself: it stages a patched
copy of those four headers with `extern` prefixed, plus a generated `Globals.c` with the one true
definition of each. A unity build (concatenating every `.c` file into one translation unit) was
tried first since it sidesteps this class of bug for free, but LKH3 has macro names short enough
(`#define f (1 - 1/298.257)`) to collide with unrelated variables once everything shares one
preprocessing pass — abandoned in favor of the header-patching approach above.

## Stack

- **Frontend**: React 19 + Vite 6 + Tailwind 4 (`src/App.tsx`, single ~1450-line component;
  `src/types.ts` for shared types; `src/index.css`). Uses `lucide-react` for icons, `motion` for
  animation.
- **Backend**: Express 4 (`server.ts`), run via `tsx` in dev (Vite in middleware mode) or bundled
  with `esbuild` to `dist/server.cjs` for prod. Purely a Vite dev-server/static-file wrapper now —
  no API routes, no solver logic, no child processes (see "API surface").
- **Client-side execution**: `public/solverWorker.js` is a classic (non-ESM, so `importScripts`
  works) Web Worker that loads `wasm_exec.js` (Go's wasm runtime glue) and `lkh_wasm.js`
  (Emscripten's glue for the LKH wasm module), then runs `solver.wasm`. Two things it has to
  provide that a browser doesn't have out of the box:
  - **A virtual filesystem**: `os.ReadFile` (used to load the instance text) needs a `fs`-shaped
    global object with `open`/`read`/`fstat`/`close`/etc — Go's wasm runtime only ships a default
    fs shim that implements `writeSync` (stdout) and stubs everything else to `ENOSYS`. The
    worker's `self.fs` is a small in-memory shim (`Map<path, Uint8Array>`) that must be installed
    *before* `importScripts("wasm_exec.js")` runs, since that file only installs its own default
    `if (!globalThis.fs)`.
  - **The LKH bridge**: `self.__lkhWasmSolve` (see above), backed by a small **pool** of
    pre-instantiated LKH module instances rather than one shared instance — LKH's C globals are
    never reset between `callMain` calls within a single module instance, so reusing one instance
    across sub-solve calls risks stale-state corruption. Each call pops a fresh instance and an
    async refill (guarded against overlapping itself - see `lkhPoolRefilling`) tops the pool back
    up in the background.
    - **Known issue**: an occasional Emscripten `ErrnoError` from inside one `__lkhWasmSolve` call
      (root cause not yet isolated - reproduced under an aggressive stagnation-threshold stress
      test, pre-dates this cleanup pass) can leave the pool permanently unable to refill for the
      rest of that solve, after which every stagnation intervention silently falls back to the
      pure-Go sub-solver instead of LKH3 for the remainder of the run. Results stay correct
      (feasibility is still enforced, see "Feasibility") - this only degrades solve *quality* by
      losing the LKH boost, silently. Worth root-causing if `-use-lkh` runs are ending up
      LKH-less more often than expected.
  - stdout (`fmt.Println(json)` in `main.go`) is line-buffered in the shim and forwarded to the
    main thread via `postMessage` instead of `console.log`, preserving the same
    one-JSON-message-per-line protocol the old SSE relay used.
  - Interrupting a solve mid-flight is done via `worker.terminate()` from the main thread
    (`src/App.tsx`'s `stopSolver`), not a message the worker polls for — once `go.run()` starts,
    the wasm program occupies the worker's single JS thread until it returns.
- **Solver runtime**: Go (`solver/main.go` + `solver/lkh_{native,wasm}.go`) — see above for both
  build targets and the LKH3 sub-solver split.

### History: the removed Ollama LLM step

The app used to have an LLM-guided destroy-operator selector: `server.ts`'s `/api/llm-destroy`
endpoint called local Ollama (`ollama_client.ts`, `http://localhost:11434`, model `gemma4:12b`),
invoked from `solver/main.go`'s `invokeLLMToSelectTrucks` (native build only) when a `-use-llm`
flag was set, exposed as a "Use Ollama LLM for destroy selection" UI toggle. The `/api/llm-destroy`
endpoint was deleted first, which silently broke `-use-llm` (it POSTed to a route that no longer
existed and always fell through to the heuristic) — the flag, `invokeLLMToSelectTrucks`, and
`ollama_client.ts` sat around broken and unused until this cleanup pass deleted all of them
outright. There is no LLM-guided destroy path left anywhere in this repo, native or browser.

## Commands

```bash
npm install
npm run dev      # tsx server.ts — Express + Vite middleware, http://localhost:3000
npm run build     # vite build (frontend, copies public/ verbatim into dist/) + esbuild bundle of server.ts -> dist/server.cjs
npm start         # node dist/server.cjs (prod)
npm run lint      # tsc --noEmit
npm run clean     # rm -rf dist server.js solver_bin

./build_solver.sh          # compiles solver/main.go + solver/lkh_native.go -> solver_bin (native CLI, standalone use only)
./solver_bin -file public/data/c101.txt -iterations 1000   # standalone Go solver

./build_lkh.sh              # compiles lkh3src/ -> lkh_bin (native, used by solver_bin's LKH path)
./build_lkh_wasm.sh         # compiles lkh3src/ -> lkh_wasm.cjs/lkh_wasm.wasm via Emscripten (needs tools/emsdk/, gitignored - see script for setup)
./build_solver_wasm.sh      # compiles solver/main.go + solver/lkh_wasm.go -> public/wasm/solver.wasm, copies wasm_exec.js + lkh_wasm.{js,wasm} into public/wasm/ (requires build_lkh_wasm.sh to have run first)
```

`solver_bin`/`lkh_bin` (native binaries) and `public/wasm/*` (the browser-facing artifacts,
including `lkh_wasm.js`/`lkh_wasm.wasm`) are all committed to the repo (not gitignored) — rebuild
them with `./build_solver.sh`/`./build_lkh.sh`/`./build_solver_wasm.sh` after editing their
sources and commit the results, don't hand-edit the binaries. `./build_lkh_wasm.sh` only needs to
run again if `lkh3src/` itself changes (it also needs `tools/emsdk/`, gitignored - see the script
for setup); `build_solver_wasm.sh` reuses its output (`lkh_wasm.cjs`/`lkh_wasm.wasm` at repo root)
otherwise. **The running web app depends on `public/wasm/*` existing** (the frontend fetches
`wasm/solver.wasm` directly), so stale or missing files there mean the UI's "Start Solving" button
will fail at fetch time.

## Problem data

`public/data/*.txt` holds all 56 Solomon 100-customer VRPTW benchmark instances (c1/c2/r1/r2/rc1/rc2
series - originally sourced from `problems_100_customers/`, untracked, kept as the raw reference
copy), in the standard Solomon text format (name / VEHICLE NUMBER+CAPACITY / CUSTOMER table with
`readyTime`/`dueDate`/`serviceTime` columns). Living under `public/` means a plain `vite build`
copies it into `dist/data/*.txt` automatically - required for the static GitHub Pages deploy,
where there's no server to ask for a file listing or a parsed instance.

There are **two parsers** for this format - `src/parseSolomon.ts` (used by the frontend: instance
listing, instance detail, upload) and `parseSolomonFile` in `solver/main.go` (Go, used by the
actual solve). `server.ts` used to have its own near-identical copy backing now-deleted
`/api/instances*` routes (see "API surface") - that copy is gone along with the routes it served,
so these two are the only parsers left; if you change the parsing logic or the Solomon format
assumptions, update both.

Users can upload custom instances via the UI's upload modal - handled entirely client-side now
(`handleUpload` in `src/App.tsx` parses the pasted text with `parseSolomon.ts` and holds it in an
in-memory `uploadedInstanceText` map), since a static deploy has nowhere to persist an uploaded
file to. Uploads are session-only - gone on refresh - and behave identically whether running via
`npm run dev` or the deployed static site (no dev/prod divergence).

Best-known solution distances, used only for the UI's gap-to-optimal display and "Load Optimal
Sequence" reference view (never sent to the solver - there is no solver-side early-stop feature),
are in `src/App.tsx`'s `BEST_KNOWN_SOLUTIONS` (client-side only — `server.ts` has no copy), scraped
from https://www.sintef.no/projectweb/top/vrptw/100-customers/ for all 56 instances. Trust that
table over any other hardcoded value you find - the original hand-entered values for r101 and
rc201 in this file's history were wrong (rc201's was actually rc103's value).

## Solver algorithm (Go path, `solver/main.go` — the one the web app runs, via WASM)

**[README.md](README.md) is the full algorithmic writeup** (aimed at an OR audience, with
diagrams) - this section is just an engineering-oriented index into it, plus the flag/build
details README.md doesn't cover.

- **Initial solution**: Solomon I1 sequential insertion (`buildInitialSolution`) — one route at a
  time from the full unrouted pool, seeded by farthest-from-depot, filled by c1 (distance +
  time-window shift) / c2 (depot-distance regret) selection. No clustering pre-step.
- **Vehicle-minimization pre-phase**: immediately after construction, before the main loop,
  repeatedly calls `tryRouteElimination` to shed routes while the solution is still loose.
  Budgeted at 10% of `-iterations`, logged under the `VEHICLE-MIN` category. See
  `docs/superpowers/specs/2026-07-27-route-elimination-operator-design.md` for why this runs
  up front rather than only reactively.
- **Local search**: `localSearchImprove` alternates four operators to convergence (or a 5-round
  cap) after construction, after every accepted destroy/repair candidate, and after a stagnation
  merge (see below) - intra-route 2-opt (`twoOptRoute`), inter-route tail-swap 2-opt*
  (`twoOptStarImproveSolution`), single-customer cross-route Or-opt (`orOptImproveSolution`), and
  2/3-customer segment cross-route Or-opt (`orOptSegmentImproveSolution`). The last three are
  cross-route moves 2-opt alone can't make; 2-opt* and the segment variant both use an O(1)
  boundary-delta pre-filter before the expensive feasibility check, since only the edges at the
  cut/insertion point change. See README.md's "Local search" section for the full mechanics.
- **Destroy operators**: four ALNS-roulette-weighted operators - Route Elimination, Worst Destroy,
  Random Destroy, and Shaw (relatedness-based) Destroy - chosen each iteration by adaptive
  roulette-wheel weighting (`alnsWeights`), not a fixed split. Weights start equal and are
  reweighted every 50 iterations from each operator's average reward that segment (new-best >
  tied-vehicle improvement > accepted-but-worse > nothing for a reject). A fifth mechanism, **Long-
  Edge Destroy**, sits outside this roulette entirely: a forced intervention (checked every
  iteration, same pattern as the stagnation intervention below) that fires when the current
  solution's worst customer-to-customer edge is a statistical outlier (`detectLongEdgeOutlier`,
  `mean + 2.5·stddev` of that solution's own customer-to-customer edges, depot legs excluded), and
  anchors a Shaw-style removal directly at that edge's two endpoints (`destroyShawSeeded`) instead
  of Shaw Destroy's random seed. Capped at one forced attempt per specific flagged edge (detection
  re-runs every iteration, so a persisting problem gets re-evaluated later rather than starving the
  roulette or being retried needlessly on an edge that isn't resolving) and never credited/
  penalized via `alnsWeights`. See README.md's "Destroy operators" section for the full mechanics
  and Shaw removal's relatedness formula.
- **Repair**: greedy insertion (`repairGreedy`); Route Elimination uses
  `repairGreedyNoNewRoute` instead (reinsertion into existing routes only, never opens a new
  one).
- **Acceptance**: always accept on reduced fleet size or distance; within a tied vehicle count, a
  worse-distance candidate is accepted via a simulated-annealing Metropolis criterion
  (`simulatedAnnealingAccept`) with a geometrically-cooling temperature schedule - never applies
  across a worse vehicle count, so the hierarchical objective can't be relaxed by temperature.
- **Stagnation intervention**: after `-stagnation-threshold` iterations with no improvement,
  destroys 2–5 routes (selected by route-level relatedness - `selectStagnationRoutesHeuristically`
  → `mostRelatedRoutes`, reusing the same `customerRelatedness` scoring Shaw removal uses) and
  re-solves the freed customers, escalating destroy size across up to 3 attempts if it doesn't
  improve. Route re-solving is either the pure-Go heuristic (default) or LKH3
  (`invokeLKHSubSolver`) depending on `-use-lkh` — see above. The re-solved routes are merged back
  with the untouched ones via `mergeStagnationSubSolution`, which runs a full `localSearchImprove`
  pass over the *merged* solution (not just the sub-solve in isolation) before accepting it - this
  is what cleans up a bad connector edge at the seam between untouched and re-solved routes.
- **Multi-start**: `-restarts N` (native CLI only, default 1, browser worker never passes anything
  else) runs N full independent solves sequentially with seed, seed+1, ..., keeping the best
  (`isBetterSolution`) as the final result.
- There is no solver-side early-stop feature - the loop always runs the full `-iterations` count
  (× `-restarts`, if set above 1). The UI's best-known-solution comparison (see "Problem data") is
  purely a post-hoc display, not a stopping condition sent to the solver.

## API surface (`server.ts`) — there is none anymore

`server.ts` used to expose `GET /api/instances`, `GET /api/instances/:id`, and `POST /api/upload`,
all backing frontend features that were rewritten to run entirely client-side (instance
listing/detail parse `public/data/*.txt` directly via `src/parseSolomon.ts`; upload holds pasted
text in an in-memory map - see "Problem data"). Once nothing called them, they were deleted along
with their backing `parseSolomonText`/`Customer` duplicate-parser code, not just left unused.

`server.ts` today is nothing but a Vite dev-server/static-file wrapper: Express app setup, Vite
middleware in dev, `express.static(distPath)` + SPA fallback in prod. No routes, no request
handlers, no child processes. There's no `/data/*` static route either - redundant once `data/`
moved under `public/`, since Vite's dev middleware and the prod `express.static(distPath)` branch
both already serve `public/`'s contents automatically.

The actual solve runs entirely in the browser (see "Client-side execution") and instance
data/listing/upload are all client-side too (see "Problem data") - `server.ts` has nothing on the
critical path at all; it only exists so `npm run dev`/`npm start` have something to run.

## Deployment: GitHub Pages

The app is deployed as a fully static site at **https://ajayarn.github.io/RoutingOpt/** via
`.github/workflows/deploy-pages.yml`, which on every push to `main` (or manual
`workflow_dispatch`) runs `npm run build:pages` (`vite build` only - skips the `esbuild
server.ts ...` step in the regular `build` script, which isn't needed for a static deploy) and
publishes `dist/` via `actions/upload-pages-artifact` + `actions/deploy-pages`. The repo's Pages
source is set to `build_type: workflow` (done once via `gh api --method POST
repos/ajayarn/RoutingOpt/pages -f build_type=workflow`) so Actions is what serves it, not a
`gh-pages` branch.

Two things make a plain `vite build` output actually work when served from a subpath
(`/RoutingOpt/`, not the domain root) instead of needing a hardcoded path:
- `vite.config.ts` sets `base: './'` (relative, not absolute) so every Vite-bundled asset
  reference resolves correctly regardless of subpath depth.
- Every hand-written root-absolute path elsewhere (`new Worker('/solverWorker.js')`,
  `fetch('/data/...')`, and inside `public/solverWorker.js` its `importScripts(...)` and the
  `solver.wasm` fetch) was changed to a **relative** path - Vite doesn't rewrite plain runtime
  string literals like these, only its own bundled `import`/asset references, so these needed
  fixing by hand. If you add a new absolute `/xxx` path reference anywhere in the frontend or
  worker, it will 404 on Pages even though it works fine locally at the domain root - use a
  relative path instead.

## Feasibility

Time windows and capacity are checked before any candidate route/solution is accepted
(`isRouteFeasible` in TS, `calculateRouteDetails` in Go) — this must hold in both solver
implementations, and LKH3's output specifically is never trusted without this check (see above).
