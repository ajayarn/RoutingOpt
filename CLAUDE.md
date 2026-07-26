# CLAUDE.md

Guidance for Claude Code when working in this repository.

## What this is

RoutingOpt is an interactive Vehicle Routing Problem with Time Windows (VRPTW) solver: a React
dashboard that runs a Large Neighborhood Search (LNS) / Simulated Annealing (SA) metaheuristic
(written in Go, compiled to WebAssembly) **entirely client-side**, in a Web Worker, streaming live
optimization progress (routes, distance, vehicle count, solver log) to the UI via `postMessage`.
LKH3 (a C TSP/VRP solver) is available as an optional stagnation sub-solver, also compiled to
WASM. The Express backend that remains only serves instance data (parsed JSON + raw Solomon text)
and static assets — it does not run or proxy any part of the solve itself.

It was originally scaffolded by Google AI Studio (`metadata.json`, `majorCapabilities:
MAJOR_CAPABILITY_SERVER_SIDE_GEMINI_API`) and used Gemini, then local Ollama, as an LLM-guided
destroy-operator selector. **That entire LLM-destroy feature has been removed from the running
app** (no more `/api/llm-destroy`, no more "Use Ollama LLM" toggle) — see "History: the removed
Ollama LLM step" below if you're wondering where it went.

There are **two independent solver implementations** in this repo — know which one you're
touching:

1. **Go solver (`solver/main.go`)** — the one actually wired into the running app, compiled to
   WASM (`public/wasm/solver.wasm`) and run in a browser Web Worker (`public/solverWorker.js`).
   It's also still buildable as a native CLI binary (`solver_bin`, via `./build_solver.sh`) for
   standalone testing/benchmarking outside the browser — nothing in the running app spawns this
   binary anymore. It has K-means clustering for the initial solution, a pure-Go stagnation
   heuristic (`selectStagnationRoutesHeuristically`) as its default destroy-selection mechanism
   when stagnated, and an optional LKH3 sub-solver (see below).
2. **TypeScript solver (`solver_engine.ts`)** — kept in the repo but **not used** anywhere. It was
   the original (accidental) default before the app was rewired to run the Go solver; it still
   works standalone if imported directly, but nothing currently calls it.

Both implementations parse the same Solomon-format files and target the same problem, but their
algorithms (destroy/repair operators, acceptance criteria) have diverged — don't assume a fix in
one applies to the other.

### LKH3 stagnation sub-solver (`-use-lkh`)

`solver/main.go` takes a `-use-lkh` flag (default `false`), exposed in the UI as the "Use LKH3 for
stagnation sub-solving" toggle. When a stagnation intervention fires, instead of (or in addition
to falling back on) the pure-Go K-means+LNS sub-solver, it can hand the destroyed customers to
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
  with `esbuild` to `dist/server.cjs` for prod. Serves parsed/raw instance data and static assets
  only — no solver logic, no child processes.
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
    async refill tops the pool back up in the background.
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
flag was set, exposed as a "Use Ollama LLM for destroy selection" UI toggle. This has been fully
removed from the running app (endpoint deleted, toggle deleted, worker never passes `-use-llm`).
`ollama_client.ts` and the `-use-llm` flag/`invokeLLMToSelectTrucks` function still exist in
`solver/main.go` for standalone native-CLI use, but nothing in the browser path reaches them.

## Commands

```bash
npm install
npm run dev      # tsx server.ts — Express + Vite middleware, http://localhost:3000
npm run build     # vite build (frontend, copies public/ verbatim into dist/) + esbuild bundle of server.ts -> dist/server.cjs
npm start         # node dist/server.cjs (prod)
npm run lint      # tsc --noEmit
npm run clean     # rm -rf dist server.js solver_bin

./build_solver.sh          # compiles solver/main.go + solver/lkh_native.go -> solver_bin (native CLI, standalone use only)
./solver_bin -file data/c101.txt -iterations 1000 -algorithm lns   # standalone Go solver

./build_lkh.sh              # compiles lkh3src/ -> lkh_bin (native, used by solver_bin's LKH path)
./build_lkh_wasm.sh         # compiles lkh3src/ -> lkh_wasm.cjs/lkh_wasm.wasm via Emscripten (needs tools/emsdk/, gitignored - see script for setup)
./build_solver_wasm.sh      # compiles solver/main.go + solver/lkh_wasm.go -> public/wasm/solver.wasm, copies wasm_exec.js + lkh_wasm.{js,wasm} into public/wasm/ (requires build_lkh_wasm.sh to have run first)
```

`solver_bin`/`lkh_bin` (native binaries) are committed to the repo (not gitignored) — rebuild them
with `./build_solver.sh`/`./build_lkh.sh` after editing their sources, don't hand-edit the
binaries. `public/wasm/*` (the browser-facing artifacts) are **not** committed as of this writing;
rebuild with `./build_lkh_wasm.sh && ./build_solver_wasm.sh` after editing `solver/main.go`,
`solver/lkh_wasm.go`, or `lkh3src/` — **the running web app depends on these existing** (the
frontend fetches `/wasm/solver.wasm` directly), so stale or missing files there mean the UI's
"Start Solving" button will fail at fetch time.

## Problem data

`data/*.txt` holds all 56 Solomon 100-customer VRPTW benchmark instances (c1/c2/r1/r2/rc1/rc2
series - originally sourced from `problems_100_customers/`, untracked, kept as the raw reference
copy), in the standard Solomon text format (name / VEHICLE NUMBER+CAPACITY / CUSTOMER table with
`readyTime`/`dueDate`/`serviceTime` columns). There are **two separate parsers** for this same
format — `parseSolomonText` in `server.ts` and `parseSolomonFile` in `solver/main.go` — kept in
sync by hand. If you change the parsing logic or the data format, update both.

Users can also upload custom instances via `POST /api/upload` (written to `data/<name>.txt`).
`data/` is also served statically at `/data/*` (`express.static`) so the browser can fetch the raw
Solomon text directly — the wasm solver has no filesystem of its own; the worker fetches this raw
text and hands it to Go's `os.ReadFile` via the virtual fs shim described above.

Best-known solution distances used for early-stop are in `src/App.tsx`'s `BEST_KNOWN_SOLUTIONS`
(client-side only — `server.ts` has no copy), scraped from
https://www.sintef.no/projectweb/top/vrptw/100-customers/ for all 56 instances. Trust that table
over any other hardcoded value you find - the original hand-entered values for r101 and rc201 in
this file's history were wrong (rc201's was actually rc103's value).

## Solver algorithm (Go path, `solver/main.go` — the one the web app runs, via WASM)

- **Initial solution**: K-means clustering + insertion (`buildInitialSolution`).
- **Destroy operators**: worst-distance and random removal (`destroyWorst`/`destroyRandom`).
- **Repair**: greedy insertion (`repairGreedy`).
- **Acceptance**: always accept on reduced fleet size or distance; SA-style probabilistic
  acceptance in `-algorithm sa` mode; random destroys always accepted to keep exploring.
- **Stagnation intervention**: after `-llm-threshold` iterations with no improvement, destroys
  2–5 routes and re-solves the freed customers, escalating destroy size across up to 3 attempts if
  it doesn't improve. Route re-solving is either the pure-Go heuristic
  (`selectStagnationRoutesHeuristically`, default) or LKH3 (`invokeLKHSubSolver`) depending on
  `-use-lkh` — see above. (Despite the flag's name, `-llm-threshold` is just the stagnation
  iteration count; it predates the LKH3 work and hasn't been renamed.)
- **Early stop**: `-optimal <distance>` ends the run once within 0.1% of that distance; the
  frontend already sends this for instances with a known best solution (see "Problem data").

## Solver algorithm (TS path, `solver_engine.ts` — unused, kept in repo)

- **Initial solution**: angular sweep + cheapest insertion (`createInitialSolution`).
- **Local search**: 2-opt (`localSearch2Opt`) and cross-route relocate (`localSearchRelocate`).
- **Destroy operators**: random, Shaw (distance + time-window similarity), and whole-route
  removal, chosen randomly each iteration.
- **Repair**: regret-2 insertion (`repairRegret2`), falls back to a new route if nothing fits.
- **Acceptance**: always accept if vehicle count drops or distance improves; otherwise SA-style
  probabilistic acceptance, with random/whole-route destroys always accepted to keep exploring.
- Also contains a since-removed-from-the-app LLM destroy call (`ollamaGenerateJSON` inline in
  `runSolverStream`) — vestigial, since nothing invokes this file at all.

## API surface (`server.ts`)

- `GET /api/instances` — list available Solomon instances from `data/`
- `GET /api/instances/:id` — parsed instance detail (JSON)
- `GET /data/<id>.txt` — raw Solomon-format text (`express.static`), for the wasm solver's virtual
  filesystem to fetch directly
- `POST /api/upload` — save + parse a custom Solomon-format instance

That's it — no solve or LLM endpoints anymore. The actual solve runs entirely in the browser (see
"Client-side execution").

## Feasibility

Time windows and capacity are checked before any candidate route/solution is accepted
(`isRouteFeasible` in TS, `calculateRouteDetails` in Go) — this must hold in both solver
implementations, and LKH3's output specifically is never trusted without this check (see above).
