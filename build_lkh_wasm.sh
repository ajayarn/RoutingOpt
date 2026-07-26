#!/bin/bash
# Compiles the same LKH3 C source used by build_lkh.sh (-> lkh_bin) into a
# WASM module (lkh_wasm.js + lkh_wasm.wasm) via Emscripten, instead of a
# native binary. Does not touch build_lkh.sh, lkh_bin, or lkh3src/ in any
# way (lkh3src/Makefile's _OBJ list is read, never edited) - purely additive.
#
# LKH.h declares all of LKH3's global state as plain tentative definitions
# (no `extern`), which every .c file includes - classic linkers merge these
# into one "common" symbol per name, but wasm-ld doesn't support common
# symbols at all ("duplicate symbol" / "common symbols are not yet
# implemented for Wasm"). lkh_wasm_prepare.py works around this by staging a
# patched copy of lkh3src/INCLUDE (LKH.h globals prefixed `extern`) plus a
# generated Globals.c with the single true definition of each - the
# standard fix for porting old tentative-definition C to a -fno-common-only
# target. See lkh_wasm_prepare.py for details.
set -e
cd "$(dirname "${BASH_SOURCE[0]}")"

echo "==> Building LKH3 (WASM)..."

if [ ! -f tools/emsdk/emsdk_env.sh ]; then
  echo "==> Error: emsdk not found at tools/emsdk."
  echo "    Setup: git clone https://github.com/emscripten-core/emsdk.git tools/emsdk"
  echo "           (cd tools/emsdk && ./emsdk install latest && ./emsdk activate latest)"
  exit 1
fi
source tools/emsdk/emsdk_env.sh > /dev/null

if ! command -v emcc &> /dev/null; then
  echo "==> Error: emcc not found on PATH after sourcing emsdk_env.sh."
  exit 1
fi

if [ ! -d lkh3src ]; then
  echo "==> Error: lkh3src/ not found."
  exit 1
fi

echo "==> Staging extern-patched LKH.h + Globals.c..."
python3 lkh_wasm_prepare.py

STAGE_INCLUDE="$(pwd)/lkh_wasm_build/INCLUDE"
GLOBALS_C="$(pwd)/lkh_wasm_build/Globals.c"

rm -rf lkh3src/OBJ_WASM
mkdir -p lkh3src/OBJ_WASM
rm -f ./lkh_wasm.js ./lkh_wasm.cjs ./lkh_wasm.wasm

# Pull the exact object list lkh3src/Makefile itself uses for the native
# build (_OBJ), so the wasm build compiles precisely the same source set -
# lkh3src/ has a couple of stray unused *.copy.c files from the original zip
# extraction that are NOT in _OBJ and must NOT be swept in by a naive glob.
OBJ_LIST=$(printf 'include Makefile\nprint-obj:\n\t@echo $(OBJ)\n' | \
  make -C lkh3src -f - CC=emcc ODIR=OBJ_WASM print-obj)

echo "==> Compiling $(echo $OBJ_LIST | wc -w | tr -d ' ') LKH3 source files to WASM objects..."
printf 'include Makefile\nbuild-objs: $(OBJ)\n' | \
  make -C lkh3src -f - CC=emcc ODIR=OBJ_WASM \
  CFLAGS="-O2 -Wall -I$STAGE_INCLUDE -DTWO_LEVEL_TREE" \
  build-objs

WASM_SETTINGS="-sMODULARIZE=1 -sEXPORT_NAME=LKHModule -sEXPORTED_RUNTIME_METHODS=callMain,FS,ccall,cwrap -sFORCE_FILESYSTEM=1 -sALLOW_MEMORY_GROWTH=1 -sENVIRONMENT=web,node -sINVOKE_RUN=0 -sEXIT_RUNTIME=1"

echo "==> Linking lkh_wasm.cjs + lkh_wasm.wasm..."
OBJ_FILES=$(for o in $OBJ_LIST; do echo "lkh3src/$o"; done)
# .cjs (not .js): package.json here has "type": "module", so a plain .js
# would be loaded as an ES module by Node and silently ignore emcc's UMD-
# style `module.exports` line at the bottom (module/exports don't exist as
# bare identifiers in ESM scope). .cjs forces CommonJS regardless of that
# setting - matters for the Node correctness harness, irrelevant when this
# same file is loaded via <script>/import in a browser later.
emcc -O2 -o lkh_wasm.cjs $OBJ_FILES "$GLOBALS_C" -I"$STAGE_INCLUDE" $WASM_SETTINGS -lm

if [ ! -f lkh_wasm.cjs ] || [ ! -f lkh_wasm.wasm ]; then
  echo "==> Error: LKH3 WASM build failed (expected lkh_wasm.cjs + lkh_wasm.wasm)."
  exit 1
fi

echo "==> Compilation successful. Generated lkh_wasm.cjs + lkh_wasm.wasm."
