#!/bin/bash
# Compiles the Go VRPTW solver (solver/main.go + solver/lkh_wasm.go, the
# js/wasm-tagged LKH bridge - NOT solver/lkh_native.go, which os/exec-spawns
# ./lkh_bin and doesn't even compile usefully for this target) to
# GOOS=js/wasm, and stages it plus Go's wasm_exec.js glue and the
# already-built LKH wasm module (build_lkh_wasm.sh) into public/wasm/, where
# Vite serves them as static assets. Purely additive - does not touch
# build_solver.sh/solver_bin (the native build the app currently runs).
set -e
cd "$(dirname "${BASH_SOURCE[0]}")"

echo "==> Building Go VRPTW Solver (WASM)..."

if ! command -v go &> /dev/null; then
  echo "==> Error: Go (golang) is not installed."
  exit 1
fi

if [ ! -f lkh_wasm.cjs ] || [ ! -f lkh_wasm.wasm ]; then
  echo "==> Error: lkh_wasm.cjs/lkh_wasm.wasm not found - run ./build_lkh_wasm.sh first."
  exit 1
fi

mkdir -p public/wasm

GOOS=js GOARCH=wasm go build -ldflags="-s -w" -o public/wasm/solver.wasm solver/main.go solver/lkh_wasm.go
if [ ! -f public/wasm/solver.wasm ]; then
  echo "==> Error: Go wasm build failed."
  exit 1
fi

GOROOT="$(go env GOROOT)"
cp "$GOROOT/lib/wasm/wasm_exec.js" public/wasm/wasm_exec.js
# .js, not .cjs: this is loaded via importScripts() in a classic Worker
# (public/solverWorker.js), not required as a Node module - the extension
# only mattered for lkh_wasm.cjs's Node correctness-harness because this
# repo's package.json has "type": "module" (see build_lkh_wasm.sh's comment
# on the same issue). Browsers/importScripts don't care about the extension.
cp lkh_wasm.cjs public/wasm/lkh_wasm.js
cp lkh_wasm.wasm public/wasm/lkh_wasm.wasm

echo "==> Compilation successful. Generated public/wasm/{solver.wasm, wasm_exec.js, lkh_wasm.js, lkh_wasm.wasm}."
