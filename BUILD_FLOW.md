# How RoutingOpt Was Built — High-Level Flow

## 1. Scaffold
Started from a Google AI Studio–generated skeleton (React frontend + Express backend), wired to the Gemini API as an LLM-guided destroy-operator selector for a prototype VRPTW solver built on OR-Tools.

## 2. Replace the prototype solver with a real one
Swapped the OR-Tools/Gemini prototype for a hand-written Go solver: K-means clustering for the initial solution, destroy/repair operators (worst/random destroy, greedy repair), and a stagnation-recovery mechanism. The LLM step moved from Gemini (cloud) to a locally-running Ollama model, keeping the "LLM picks the destroy operator" idea but removing the cloud dependency.

## 3. Add a stronger sub-solver for stagnation
When the LNS loop stagnates, destroying and greedily re-repairing a handful of routes isn't always enough. Integrated LKH3 (a specialized C TSP/VRP solver) as an optional, higher-quality sub-solver for exactly that moment — re-optimizing the freed customers before handing the result back to the main loop, with every LKH-produced route re-validated against the same hard feasibility checks as everything else (LKH uses soft penalties internally, so its output is never trusted blindly).

## 4. Get everything running in the browser
This was the big architectural shift: move the solver out of a server process and into the browser entirely.
- Compiled LKH3 to WebAssembly via Emscripten, working around its old-C89 tentative-definition globals (which wasm-ld can't link) by patching headers and generating a single real definition per global.
- Compiled the Go solver to WASM and ran it inside a Web Worker, building the small platform shims Go's WASM runtime doesn't provide for free (a virtual filesystem for `os.ReadFile`, a JS-side bridge for invoking the LKH WASM module, stdout forwarding via `postMessage` instead of the console).
- Dropped Ollama entirely — an LLM step made no sense once inference had to happen synchronously inside a browser tab — and bundled the full 56-instance Solomon benchmark library alongside the app.

## 5. Go fully static
With the solver, LKH3, and now the instance data all living client-side, the Express backend had nothing load-bearing left to do. Removed the last server dependency and deployed the app as a static site on GitHub Pages, fixing the handful of hardcoded absolute paths that only worked when served from a domain root.

## 6. Simplify and correct the optimization loop
With the architecture settled, follow-up work focused on the algorithm itself: removed a Simulated-Annealing+LNS variant that wasn't earning its complexity, dropped an "early-stop once you hit the optimal" shortcut that was short-circuiting real optimization, fixed how solutions get compared against the published best-known values, and added the ability to export results.

## Net trajectory
Cloud LLM prototype → local LLM + hand-rolled solver → add a specialist C solver for hard cases → move the whole stack into the browser (WASM + Web Worker) → delete the server → clean up and correct the optimization logic now that the platform is stable.
