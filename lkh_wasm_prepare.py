#!/usr/bin/env python3
"""
Prepares a staging copy of lkh3src for WASM compilation.

LKH3 declares much of its global state as plain tentative definitions (e.g.
`int TraceLevel;` with no `extern`) in headers included by many .c files, and
a handful more directly as non-static file-scope variables. Classic linkers
merge same-named tentative definitions across translation units into one
"common" symbol; wasm-ld does not support common symbols at all ("duplicate
symbol" / "common symbols are not yet implemented for Wasm").

This script does NOT touch lkh3src/ in place. It copies lkh3src/INCLUDE into
a staging directory, rewrites each affected header so its global-variable
block is prefixed `extern`, and emits a single Globals.c with the original
(non-extern) statements as the one true definition of each - the standard
fix for porting old tentative-definition C to a -fno-common-only target
like Wasm.

A unity build (concatenating all .c files into one translation unit) was
tried first since it sidesteps this whole class of bug for free, but LKH3
uses very short macro names (e.g. `#define f (1 - 1/298.257)` in one file)
that collide with unrelated local variables of the same name in other files
once everything shares one preprocessing pass - a worse, less bounded
problem than enumerating the ~130 actual global declarations by hand here.
"""
import sys
import shutil
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent
SRC_INCLUDE = REPO_ROOT / "lkh3src" / "INCLUDE"

# Each block is (header filename, exact text immediately BEFORE the block,
# exact text immediately AFTER the block). The block itself is never
# reproduced here - it's sliced out of the live file content - so this
# stays correct even if comments/wording inside the block change.
BLOCKS = [
    ("LKH.h", "for a node */\n};\n\n", "\n\n/* Function prototypes: */"),
    ("LKH.h", "typedef GainType (*MergeTourFunction) (void);\n", "\n\n/* The Node structure"),
    ("Sequence.h", '#include "LKH.h"\n\n', "\n\nint FeasibleKOptMove"),
    ("Genetic.h", "typedef void (*CrossoverFunction) ();\n\n", "\n\nvoid AddToPopulation"),
    ("gpx.h", "int *label_list);\n\n", "\n\nint *alloc_vectori"),
]


def split_statements(block: str):
    """Split a global-declarations block into one raw text chunk per
    semicolon-terminated statement. None of these blocks' comments contain a
    literal ';', so a plain split is safe (verified when each block was
    identified)."""
    parts = block.split(";")
    assert parts[-1].strip() == "", f"unexpected trailing content: {parts[-1]!r}"
    return [p for p in parts[:-1] if p.strip()]


def main():
    if not SRC_INCLUDE.exists():
        print(f"error: {SRC_INCLUDE} not found", file=sys.stderr)
        return 1

    out_dir = Path(sys.argv[1]) if len(sys.argv) > 1 else REPO_ROOT / "lkh_wasm_build"
    out_include = out_dir / "INCLUDE"

    if out_dir.exists():
        shutil.rmtree(out_dir)
    shutil.copytree(SRC_INCLUDE, out_include)

    seen_normalized = set()
    define_stmts = []
    total_globals = 0

    # Group blocks by file so multiple blocks in the same header (LKH.h has
    # two, non-adjacent) are applied without stale offsets from earlier edits.
    by_file = {}
    for fname, before, after in BLOCKS:
        by_file.setdefault(fname, []).append((before, after))

    for fname, specs in by_file.items():
        path = out_include / fname
        # lkh3src has a mix of LF and CRLF files (artifact of the original
        # zip); normalize so the LF-based markers above match either way.
        text = path.read_text().replace("\r\n", "\n")
        for before, after in specs:
            si = text.index(before) + len(before)
            ei = text.index(after, si)
            block = text[si:ei]

            # The last declaration in a block is sometimes followed by a
            # trailing comment with no semicolon of its own (the comment
            # describing it, before the next code the `after` marker cuts
            # into) - keep that verbatim rather than feeding it through
            # split_statements as a bogus final "statement".
            last_semi = block.rfind(";")
            real_block, trailing = block[: last_semi + 1], block[last_semi + 1 :]

            statements = split_statements(real_block)
            extern_stmts = ["extern " + s + ";" for s in statements]
            patched_block = "\n".join(extern_stmts) + trailing
            text = text[:si] + patched_block + text[ei:]

            for stmt in statements:
                normalized = " ".join(stmt.split())
                if normalized in seen_normalized:
                    # LKH.h declares `double DistanceLimit;` twice (once each
                    # at line 241 and 283) - skip the repeat.
                    continue
                seen_normalized.add(normalized)
                define_stmts.append(stmt + ";")
                total_globals += 1
        path.write_text(text)

    globals_c = (
        '#include "LKH.h"\n'
        '#include "Genetic.h"\n'
        '#include "Sequence.h"\n'
        '#include "gpx.h"\n\n'
        "/* Single true definition of every global declared `extern` in the\n"
        " * wasm-patched headers. Generated by lkh_wasm_prepare.py - do not\n"
        " * hand-edit. */\n\n"
        + "\n".join(define_stmts)
        + "\n"
    )
    (out_dir / "Globals.c").write_text(globals_c)

    print(f"Wrote patched headers ({', '.join(by_file)}) + Globals.c ({total_globals} globals) to {out_dir}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
