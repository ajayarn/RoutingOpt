# CLAUDE.md

Guidance for Claude Code when working in this repository.

## What this is

RoutingOpt is an interactive full-stack Vehicle Routing Problem with Time Windows (VRPTW)
solver: a React dashboard that streams live optimization progress (routes, distance, vehicle
count, solver log) from a Node/Express backend as it runs a Large Neighborhood Search (LNS) /
Simulated Annealing (SA) metaheuristic, with a local Ollama LLM occasionally chosen to pick which
routes to destroy. It was originally scaffolded by Google AI Studio (`metadata.json`,
`majorCapabilities: MAJOR_CAPABILITY_SERVER_SIDE_GEMINI_API`) and used Gemini for the LLM step;
that's since been replaced with local Ollama inference (no API key, no cloud dependency).

There are **two independent solver implementations** in this repo — know which one you're
touching:

1. **Go solver (`solver/main.go`, compiled to `solver_bin`)** — the one actually wired into the
   running app. `server.ts`'s `/api/solve-stream` SSE endpoint spawns `solver_bin` as a child
   process and relays its stdout (one JSON `ProgressMessage` per line) to the browser verbatim
   over SSE. This is what the React frontend drives. It has K-means clustering for the initial
   solution and a pure-Go stagnation heuristic (`selectStagnationRoutesHeuristically`) as its
   default destroy-selection mechanism when stagnated.
2. **TypeScript solver (`solver_engine.ts`)** — kept in the repo but **not used** by `server.ts`.
   It was the original (accidental) default before the app was rewired to spawn the Go binary;
   it still works standalone if imported directly, but nothing currently calls it.

Both implementations parse the same Solomon-format files and target the same problem, but their
algorithms (destroy/repair operators, acceptance criteria, LLM integration point) have diverged —
don't assume a fix in one applies to the other.

### The Ollama LLM destroy switch (`-use-llm`)

`solver_bin` takes a `-use-llm` flag (default `false`), exposed in the UI as the "Use Ollama LLM
for destroy selection" toggle in Solver Controls. `server.ts` passes it through as a single
`-use-llm=true`/`-use-llm=false` token — **not** as two separate args (`-use-llm`, `"false"`); Go's
`flag.Bool` treats the flag's mere presence as `true` and would silently ignore a following
`"false"` token as a stray positional argument. This bit us once already; if you add more boolean
flags to `solver/main.go`, pass them the same `-flag=value` way from `server.ts`.

- Off (default): the stagnation-trigger loop calls `selectStagnationRoutesHeuristically` (pure
  Go, no LLM).
- On: it calls `invokeLLMToSelectTrucks` instead, which POSTs to this same server's
  `/api/llm-destroy` endpoint (Ollama-backed) — falling back to the heuristic for that attempt if
  Ollama is unreachable or returns no valid vehicle IDs.

## Stack

- **Frontend**: React 19 + Vite 6 + Tailwind 4 (`src/App.tsx`, single ~1400-line component;
  `src/types.ts` for shared types; `src/index.css`). Uses `lucide-react` for icons, `motion` for
  animation.
- **Backend**: Express 4 (`server.ts`), run via `tsx` in dev (Vite in middleware mode) or bundled
  with `esbuild` to `dist/server.cjs` for prod. Spawns `solver_bin` (Go) as a child process per
  solve request — no in-process solver logic of its own beyond parsing/routing.
- **Solver runtime**: Go (`solver/main.go`, compiled to `solver_bin`) — see the top of this
  document. Requires the Go toolchain only to rebuild (`./build_solver.sh`); the compiled binary
  itself has no runtime dependencies.
- **LLM**: local Ollama (`ollama_client.ts`), `http://localhost:11434` with model `gemma4:12b` by
  default — override via `OLLAMA_BASE_URL`/`OLLAMA_MODEL` env vars (see `.env.example`). No API
  key required; every LLM call path degrades gracefully to a heuristic/random destroy operator if
  Ollama is unreachable or returns bad JSON, so the app still runs without it.

## Commands

```bash
npm install
npm run dev      # tsx server.ts — Express + Vite middleware, http://localhost:3000
npm run build     # vite build (frontend) + esbuild bundle of server.ts -> dist/server.cjs
npm start         # node dist/server.cjs (prod)
npm run lint      # tsc --noEmit
npm run clean     # rm -rf dist server.js solver_bin

./build_solver.sh          # compiles solver/main.go -> solver_bin (requires Go toolchain)
./solver_bin -file data/c101.txt -iterations 1000 -algorithm lns   # standalone Go solver, same binary the web app spawns
```

`solver_bin` is a compiled binary and is currently committed to the repo (not gitignored) —
rebuild it with `./build_solver.sh` after editing `solver/main.go`, don't hand-edit the binary.
**The running web app depends on this binary existing and being executable** (`server.ts` spawns
`path.join(process.cwd(), 'solver_bin')` directly) — after any `solver/main.go` change, rebuild
before testing through the UI or you'll be running against stale behavior.

## Problem data

Solomon benchmark instances live in `data/*.txt` (c101, c201, r101, r201, rc101, rc201), in the
standard Solomon text format (name / VEHICLE NUMBER+CAPACITY / CUSTOMER table with
`readyTime`/`dueDate`/`serviceTime` columns). There are **two separate parsers** for this same
format — `parseSolomonText` in `server.ts` and `parseSolomonFile` in `solver/main.go` — kept in
sync by hand. If you change the parsing logic or the data format, update both.

Users can also upload custom instances via `POST /api/upload` (written to `data/<name>.txt`).

Best-known solution distances are hardcoded in `server.ts` (`BACKEND_BEST_KNOWN_SOLUTIONS`):
c101: 828.94, c201: 591.56, r101: 1645.79, r201: 1252.37, rc101: 1696.94, rc201: 1261.67.

## Solver algorithm (Go path, `solver/main.go` — the one the web app runs)

- **Initial solution**: K-means clustering + insertion (`buildInitialSolution`).
- **Destroy operators**: worst-distance and random removal (`destroyWorst`/`destroyRandom`).
- **Repair**: greedy insertion (`repairGreedy`).
- **Acceptance**: always accept on reduced fleet size or distance; SA-style probabilistic
  acceptance in `-algorithm sa` mode; random destroys always accepted to keep exploring.
- **Stagnation intervention**: after `-llm-threshold` iterations with no improvement, destroys
  2–5 routes and re-solves the freed customers via a K-means + LNS sub-solver, escalating destroy
  size across up to 3 attempts if it doesn't improve. Route selection for this step is either the
  pure-Go heuristic (`selectStagnationRoutesHeuristically`, default) or Ollama
  (`invokeLLMToSelectTrucks`) depending on `-use-llm` — see above.
- **Early stop**: `-optimal <distance>` ends the run once within 0.1% of that distance; the
  frontend already sends this for instances with a known best solution.

## Solver algorithm (TS path, `solver_engine.ts` — unused by the web app, kept in repo)

- **Initial solution**: angular sweep + cheapest insertion (`createInitialSolution`).
- **Local search**: 2-opt (`localSearch2Opt`) and cross-route relocate (`localSearchRelocate`).
- **Destroy operators**: random, Shaw (distance + time-window similarity), and whole-route
  removal, chosen randomly each iteration.
- **Repair**: regret-2 insertion (`repairRegret2`), falls back to a new route if nothing fits.
- **Acceptance**: always accept if vehicle count drops or distance improves; otherwise SA-style
  probabilistic acceptance, with random/whole-route destroys always accepted to keep exploring.
- **LLM destroy**: every `llmThreshold` iterations, calls `ollamaGenerateJSON` inline in
  `runSolverStream` to pick 2 sub-optimal vehicle routes to destroy instead of the heuristic
  operators. Nothing currently invokes this file — see the top of this document.

## API surface (`server.ts`)

- `GET /api/instances` — list available Solomon instances from `data/`
- `GET /api/instances/:id` — parsed instance detail
- `POST /api/upload` — save + parse a custom Solomon-format instance
- `GET /api/solve-stream?instance=&iterations=&algorithm=&llmThreshold=&optimal=&useLlm=` —
  spawns `solver_bin` with matching flags and relays its stdout JSON lines to the browser as SSE
  (`type: start|progress|result|error`)
- `POST /api/llm-destroy` — given current routes (+ history of failed destroy attempts), ask
  Ollama which vehicle IDs to destroy; returns `{ vehicleIds: [] }` on any Ollama failure so the
  caller can fall back to a heuristic. Called by `solver/main.go`'s `invokeLLMToSelectTrucks` when
  `-use-llm=true`.

## Feasibility

Time windows and capacity are checked before any candidate route/solution is accepted
(`isRouteFeasible` in TS, `calculateRouteDetails` in Go) — this must hold in both solver
implementations.
