//go:build js && wasm

package main

import (
	"fmt"
	"syscall/js"
)

// runLKHSolver is the wasm implementation: GOOS=js has no subprocess support
// (os/exec always fails at runtime with "not implemented on js") and no real
// filesystem, so instead of writing files and spawning ./lkh_bin like the
// native build (lkh_native.go), this calls out to a JS-global bridge
// function the host page/harness is expected to register:
//
//	globalThis.__lkhWasmSolve = function(instanceText, parText) -> string | null
//
// The bridge is expected to have already loaded the LKH wasm module
// (lkh_wasm.cjs / lkh_wasm.wasm) and to, per call: write instanceText/parText
// into that module's MEMFS, invoke its callMain, read the resulting solution
// file back out of MEMFS, and return it as a string (or null/undefined on
// any failure, which this function treats as "fall back to the pure-Go
// sub-solver" exactly like a native lkh_bin failure would).
//
// PROBLEM_FILE/MTSP_SOLUTION_FILE below are fixed in-MEMFS paths rather than
// real temp paths (there's no OS temp dir to allocate) - the bridge owns
// mapping those to actual MEMFS writes/reads.
func runLKHSolver(instanceText string, seed int64) (string, bool) {
	bridge := js.Global().Get("__lkhWasmSolve")
	if bridge.IsUndefined() || bridge.IsNull() {
		fmt.Println("LKH: globalThis.__lkhWasmSolve is not registered; falling back")
		return "", false
	}

	parText := fmt.Sprintf(
		"PROBLEM_FILE = /lkh_sub.vrptw\nMTSP_SOLUTION_FILE = /lkh_sub.sol\nMAX_TRIALS = 200\nRUNS = 1\nTRACE_LEVEL = 0\nSEED = %d\n",
		seed,
	)

	result := bridge.Invoke(instanceText, parText)
	if result.IsUndefined() || result.IsNull() {
		return "", false
	}
	return result.String(), true
}
