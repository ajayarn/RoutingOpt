#!/bin/bash
echo "==> Building LKH3..."
if command -v make &> /dev/null && command -v gcc &> /dev/null; then
  rm -f lkh3src/OBJ/*.o ./LKH
  (cd lkh3src && make)
  if [ $? -eq 0 ] && [ -f ./LKH ]; then
    mv ./LKH ./lkh_bin
    chmod +x lkh_bin
    echo "==> Compilation successful. Generated lkh_bin."
    exit 0
  else
    echo "==> Error: LKH3 build failed."
    exit 1
  fi
else
  echo "==> Error: make/gcc toolchain not found."
  exit 1
fi
