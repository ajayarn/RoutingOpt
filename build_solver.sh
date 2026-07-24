#!/bin/bash
echo "==> Building Go VRPTW Solver..."
if command -v go &> /dev/null; then
  go build -ldflags="-s -w" -o solver_bin solver/main.go
  if [ $? -eq 0 ]; then
    echo "==> Compilation successful. Generated solver_bin."
    chmod +x solver_bin
    exit 0
  else
    echo "==> Error: Go build failed."
    exit 1
  fi
else
  echo "==> Error: Go (golang) is not installed."
  exit 1
fi
