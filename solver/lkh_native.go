//go:build !(js && wasm)

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// runLKHSolver is the native implementation: write instance+par files to a
// real temp dir, spawn ./lkh_bin as a subprocess (no timeout - see
// invokeLKHSubSolver's doc comment), and read the solution file back. The
// wasm build (lkh_wasm.go) implements the same signature via a JS-bridged
// call into an in-browser/in-process LKH wasm module instead, since
// GOOS=js has no subprocess or real filesystem support.
func runLKHSolver(instanceText string, seed int64) (string, bool) {
	tmpDir, err := os.MkdirTemp("", "lkh_sub_*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "LKH: failed to create temp dir: %v\n", err)
		return "", false
	}
	defer os.RemoveAll(tmpDir)

	instancePath := filepath.Join(tmpDir, "sub.vrptw")
	parPath := filepath.Join(tmpDir, "sub.par")
	solPath := filepath.Join(tmpDir, "sub.sol")

	if err := os.WriteFile(instancePath, []byte(instanceText), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "LKH: failed to write instance file: %v\n", err)
		return "", false
	}

	par := fmt.Sprintf(
		"PROBLEM_FILE = %s\nMTSP_SOLUTION_FILE = %s\nMAX_TRIALS = 200\nRUNS = 1\nTRACE_LEVEL = 0\nSEED = %d\n",
		instancePath, solPath, seed,
	)
	if err := os.WriteFile(parPath, []byte(par), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "LKH: failed to write parameter file: %v\n", err)
		return "", false
	}

	// No timeout: LKH's value is expected to matter most on the larger
	// subproblems, which need more search time - let it run to completion
	// (MAX_TRIALS/RUNS above still bound the search itself).
	cmd := exec.Command("./lkh_bin", parPath)
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "LKH: invocation failed: %v\n", err)
		return "", false
	}

	solData, err := os.ReadFile(solPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "LKH: failed to read solution file: %v\n", err)
		return "", false
	}
	return string(solData), true
}
