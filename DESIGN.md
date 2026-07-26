# RoutingOpt — Design Document

## What this is

RoutingOpt is an interactive VRPTW (Vehicle Routing Problem with Time Windows)
solver: a React dashboard that streams live optimization progress — routes,
distance, vehicle count, a decision log — from a metaheuristic solver as it
runs, with an LLM optionally chosen to pick which routes to destroy and
re-solve. It targets the Solomon benchmark instances (`c101`, `c201`, `r101`,
`r201`, `rc101`, `rc201`), each with 100 customers, demand/capacity
constraints, and per-customer time windows.

The project was originally scaffolded by Google AI Studio
(`metadata.json`'s `MAJOR_CAPABILITY_SERVER_SIDE_GEMINI_API`), which explains
why its LLM integration point was originally Gemini. LLM calls now go to a
local Ollama instance instead.

## Architecture at a glance

```
┌─────────────────────┐   SSE (/api/solve-stream)   ┌─────────────────────┐   spawn()   ┌─────────────────────┐
│  React frontend      │────────────────────────────▶│  Express server      │────────────▶│  solver_bin           │
│  src/App.tsx          │◀──────────────────────────── │  server.ts            │◀ ─ ─ ─ ─ ─ ─│  (compiled from        │
│  (routes, log, chart) │  stdout lines relayed as    │                       │  stdout     │   solver/main.go)      │
└─────────────────────┘  SSE data: frames, verbatim  └─────────────────────┘  JSON lines └─────────────────────┘
                                                              │  ▲                                  │
                                                              │  │ POST /api/llm-destroy             │ POST (if -use-llm=true)
                                                              ▼  └──────────────────────────────────┘
                                                        ollamaGenerateJSON()
                                                          → localhost:11434
```

`solver_engine.ts` (TypeScript) also still exists in the repo, implementing
the same problem independently — but nothing calls it. It was the original,
accidental default before `server.ts` was rewired to spawn `solver_bin`
directly; see "History" below.

## Request lifecycle for a live solve

1. Frontend calls `GET /api/solve-stream?instance=&iterations=&llmThreshold=&optimal=&useLlm=`.
2. `server.ts` validates the instance file exists, then `spawn()`s
   `solver_bin` with matching CLI flags (`-file`, `-iterations`,
   `-llm-threshold`, `-optimal`, `-use-llm=<bool>`, `-seed <Date.now()>`).
3. `solver_bin` prints one JSON `ProgressMessage` object per line to stdout
   as it solves. `server.ts` reads these via `readline` and relays each line
   **verbatim** as an SSE `data:` frame — no reparsing or reshaping, since
   the JSON field names already match what the frontend expects exactly.
4. If `-use-llm=true` and the solver hits its stagnation threshold, `solver_bin`
   itself makes an HTTP POST back to this same server's `/api/llm-destroy`
   endpoint (Ollama-backed) to decide which routes to destroy, falling back
   to its own pure-Go heuristic if that call fails or returns nothing usable.
5. On client disconnect (`req.on('close')`), `server.ts` kills the child
   process. On solver exit, the SSE response ends.
6. The frontend appends each message to `solverLogs`/`progressHistory` and
   re-renders the map, route list, and convergence chart live.

## The Go solver (`solver/main.go` → `solver_bin`)

- **Initial solution**: K-means clustering + insertion (`buildInitialSolution`).
- **Destroy operators**: worst-distance and random removal.
- **Repair**: greedy insertion.
- **Acceptance**: always-accept on improvement; random destroys always accepted.
- **Stagnation intervention**: after `-llm-threshold` iterations with no
  improvement, destroys 2–5 routes and re-solves the freed customers via a
  K-means + LNS sub-solver, escalating destroy size across up to 3 attempts.
  Route selection for this step is the pure-Go heuristic
  (`selectStagnationRoutesHeuristically`) by default, or Ollama
  (`invokeLLMToSelectTrucks`, via `/api/llm-destroy`) if `-use-llm=true` —
  falling back to the heuristic on any Ollama failure.
- **Early stop**: `-optimal <distance>` ends the run once within 0.1% of
  that distance. The frontend sends this automatically for instances with a
  known best solution (`BEST_KNOWN_SOLUTIONS` in `src/App.tsx`).

An earlier version of this solver also shelled out to a Python/OR-Tools
subprocess for full solves and per-route TSPTW re-sequencing, exposed in the
UI as extra algorithm choices. That capability has been removed entirely
(rather than left as a disconnected feature) — the OR-Tools UI options never
actually invoked it anyway, back when `solver_engine.ts` was still the live
path.

## The `-use-llm` flag: a boolean-flag gotcha worth knowing

`server.ts` passes the switch as a single `-use-llm=true`/`-use-llm=false`
token, not two separate args. Go's `flag.Bool` treats a flag's mere
*presence* as `true` — passing `-use-llm` and `"false"` as two separate
array elements to `spawn()` sets it to `true` regardless, silently ignoring
the `"false"` string as a stray positional argument. This was an actual bug
hit and fixed during development; any future boolean flag added to
`solver/main.go` needs to be passed the same `-flag=value` way.

## History: `solver_engine.ts` and the unused Gemini/Go plumbing

Before this design settled, `server.ts` called `solver_engine.ts`
in-process, and `solver_bin` was a separate, unwired standalone CLI tool.
Several pieces of now-load-bearing code were dormant leftovers at that
point that have since been connected:

- `server.ts`'s `spawn`/`readline` imports, previously unused, now drive the
  actual solve.
- The frontend's `&optimal=` query param, previously sent but never read
  server-side, now reaches `solver_bin`'s early-stop flag.
- `solver/main.go`'s `invokeLLMToSelectTrucks`, previously dead code, is now
  reachable via the `-use-llm` switch.
- The status message `"Initializing Go VRPTW Solver..."` in `src/App.tsx`,
  previously inaccurate (the app ran TS), is now accurate.

`solver_engine.ts` and `ollama_client.ts`'s use inside it are kept in the
repo but are **not called by anything** — if you're asked to fix "the LLM
destroy logic" or any other solver behavior, you want `solver/main.go`, not
`solver_engine.ts`.

## Data parsing (two copies, kept in sync by hand)

Solomon-format instance files (`data/*.txt`) are parsed independently by
`parseSolomonText` (`server.ts`, used for `/api/instances`, `/api/upload`,
etc. — not for `/api/solve-stream`, which just checks the file exists and
lets `solver_bin` do its own parsing) and `parseSolomonFile`
(`solver/main.go`). Same format, same field order, two hand-written
parsers — a change to the file format needs both updated together. Users
can also upload custom instances via `POST /api/upload`, written to
`data/<name>.txt`.

## Feasibility

Time windows and vehicle capacity are checked before any candidate
route/solution is accepted in both implementations independently
(`isRouteFeasible` in TS, `calculateRouteDetails` in Go). This invariant
holds regardless of which solver produced the candidate.

## LLM integration details

`ollama_client.ts` exposes `getOllamaConfigFromEnv()` (reads
`OLLAMA_BASE_URL` / `OLLAMA_MODEL`, defaulting to `http://localhost:11434`
and `gemma4:12b`) and `ollamaGenerateJSON()`, which POSTs to Ollama's
`/api/generate` with a JSON-schema `format` and parses the response. It's
used by `POST /api/llm-destroy` (`server.ts`) — given a list of routes,
asks the model to pick 2–3 to destroy, validates the returned IDs against
the actual route set, and returns `{ vehicleIds: [] }` on any failure so the
caller (Go's `invokeLLMToSelectTrucks`) falls back to its own heuristic.

## Frontend layout

`src/App.tsx` renders a 12-column grid: a fixed-width left column (Solver
Controls — including the stagnation-threshold/Ollama-switch
controls — and Instance Specification) and a wide right-hand stack (Routing
Visualization → Route Explorer, with an inline per-vehicle timeline when a
route is selected → Status & History: live console, best-solution metrics,
convergence chart).
