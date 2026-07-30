// Runs the Go VRPTW solver (compiled to wasm/solver.wasm, GOOS=js GOARCH=wasm)
// entirely in-browser, off the main thread. Classic (non-module) worker, so
// wasm_exec.js's/lkh_wasm.js's importScripts()-based loading and top-level
// `Go`/`LKHModule` globals work without any bundler involvement.
//
// GOOS=js has no subprocess and no real filesystem - two things
// solver/main.go relies on:
//   1. os.ReadFile(*filePath) to load the Solomon instance text. Fixed below
//      with a small in-memory `fs` shim (virtual files keyed by path),
//      installed as `self.fs` *before* importScripts('wasm_exec.js') runs -
//      wasm_exec.js only installs its own (ENOSYS-stubbed) default fs if
//      `globalThis.fs` isn't already set.
//   2. The LKH sub-solver (solver/lkh_wasm.go, active when -use-lkh=true)
//      calling out to a subprocess. Fixed by registering
//      `self.__lkhWasmSolve(instanceText, parText) -> string|null`, which
//      solver/lkh_wasm.go expects, backed by a small pool of pre-instantiated
//      LKH wasm module instances (lkh_wasm.js/.wasm, built by
//      build_lkh_wasm.sh) - see the pool comment below for why pooled
//      instances instead of one shared instance.

const virtualFiles = new Map(); // path -> Uint8Array
const openFiles = new Map(); // fd -> { data: Uint8Array, pos: number }
let nextFd = 3; // 0/1/2 are reserved for stdin/stdout/stderr

const encoder = new TextEncoder();
const decoder = new TextDecoder("utf-8");

function vfsWriteFile(path, text) {
  virtualFiles.set(path, encoder.encode(text));
}

function enoent() {
  const err = new Error("ENOENT: no such file");
  err.code = "ENOENT";
  return err;
}

function enosys() {
  const err = new Error("not implemented");
  err.code = "ENOSYS";
  return err;
}

function makeStat(size) {
  return {
    size,
    mode: 0o644,
    mtimeMs: 0,
    isDirectory: () => false,
    isFile: () => true,
  };
}

// stdout/stderr are line-buffered here and forwarded as postMessage events
// rather than console.log'd, so the main thread can parse each JSON line the
// same way server.ts used to relay solver_bin's stdout over SSE.
let stdoutBuf = "";
function handleStdoutLine(line) {
  if (!line) return;
  let msg;
  try {
    msg = JSON.parse(line);
  } catch (e) {
    console.error("Non-JSON line from solver.wasm, dropping:", line);
    return;
  }
  self.postMessage(msg);
}

self.fs = {
  constants: { O_WRONLY: -1, O_RDWR: -1, O_CREAT: -1, O_TRUNC: -1, O_APPEND: -1, O_EXCL: -1, O_DIRECTORY: -1 },
  writeSync(fd, buf) {
    const text = decoder.decode(buf);
    if (fd === 2) {
      console.error(text);
      return buf.length;
    }
    stdoutBuf += text;
    let nl;
    while ((nl = stdoutBuf.indexOf("\n")) !== -1) {
      handleStdoutLine(stdoutBuf.slice(0, nl).trim());
      stdoutBuf = stdoutBuf.slice(nl + 1);
    }
    return buf.length;
  },
  write(fd, buf, offset, length, position, callback) {
    if (offset !== 0 || length !== buf.length || position !== null) {
      callback(enosys());
      return;
    }
    const n = this.writeSync(fd, buf);
    callback(null, n);
  },
  open(path, flags, mode, callback) {
    const data = virtualFiles.get(path);
    if (!data) {
      callback(enoent());
      return;
    }
    const fd = nextFd++;
    openFiles.set(fd, { data, pos: 0 });
    callback(null, fd);
  },
  close(fd, callback) {
    openFiles.delete(fd);
    callback(null);
  },
  read(fd, buffer, offset, length, position, callback) {
    const f = openFiles.get(fd);
    if (!f) {
      callback(enoent());
      return;
    }
    const readPos = position === null || position === undefined ? f.pos : position;
    const chunk = f.data.subarray(readPos, readPos + length);
    buffer.set(chunk, offset);
    if (position === null || position === undefined) f.pos += chunk.length;
    callback(null, chunk.length);
  },
  fstat(fd, callback) {
    const f = openFiles.get(fd);
    if (!f) {
      callback(enoent());
      return;
    }
    callback(null, makeStat(f.data.length));
  },
  stat(path, callback) {
    const data = virtualFiles.get(path);
    if (!data) {
      callback(enoent());
      return;
    }
    callback(null, makeStat(data.length));
  },
  lstat(path, callback) {
    this.stat(path, callback);
  },
  fsync(fd, callback) {
    callback(null);
  },
  chmod(path, mode, callback) { callback(null); },
  chown(path, uid, gid, callback) { callback(null); },
  fchmod(fd, mode, callback) { callback(null); },
  fchown(fd, uid, gid, callback) { callback(null); },
  ftruncate(fd, length, callback) { callback(enosys()); },
  lchown(path, uid, gid, callback) { callback(enosys()); },
  link(path, link, callback) { callback(enosys()); },
  mkdir(path, perm, callback) { callback(enosys()); },
  readdir(path, callback) { callback(enosys()); },
  readlink(path, callback) { callback(enosys()); },
  rename(from, to, callback) { callback(enosys()); },
  rmdir(path, callback) { callback(enosys()); },
  symlink(path, link, callback) { callback(enosys()); },
  truncate(path, length, callback) { callback(enosys()); },
  unlink(path, callback) { callback(enosys()); },
  utimes(path, atime, mtime, callback) { callback(null); },
};

// Relative (not "/wasm/...") - resolves against this worker script's own
// URL regardless of whether the site is served from the domain root or a
// GitHub Pages project subpath.
importScripts("wasm/wasm_exec.js");

// lkh_wasm.js/.wasm are only needed when a solve actually passes
// -use-lkh=true (see onmessage below) - loaded separately from wasm_exec.js
// and wrapped in try/catch so a missing/stale LKH build artifact only
// disables the optional LKH sub-solver for this session instead of breaking
// every solve, including ones with the LKH toggle off.
let lkhModuleAvailable = false;
try {
  importScripts("wasm/lkh_wasm.js");
  lkhModuleAvailable = true;
} catch (e) {
  console.error("[lkh bridge] wasm/lkh_wasm.js unavailable - LKH sub-solver disabled for this session:", e);
}

// js.Value.Invoke() on the Go side (solver/lkh_wasm.go) blocks synchronously
// until this JS function returns, so it must be synchronous - but
// instantiating an Emscripten module is only exposed as an async factory.
// Pre-instantiate a small pool of ready LKH module instances and hand them
// out synchronously, refilling in the background; each sub-solve call gets
// its own instance because LKH's C globals are never reset between
// `callMain` invocations within one instance, so reusing one instance
// across calls risks stale-state corruption (matches the Node correctness
// harness used to verify this pipeline - see run_go_wasm_with_lkh.js).
const LKH_POOL_SIZE = 3;
let lkhPool = [];
let lkhPoolPrimed = false;
let lkhPoolRefilling = false;

async function refillLkhPool() {
  // __lkhWasmSolve fires refillLkhPool() after every pop without awaiting
  // it; guard against overlapping refills racing each other and pushing the
  // pool past LKH_POOL_SIZE.
  if (lkhPoolRefilling) return;
  lkhPoolRefilling = true;
  try {
    while (lkhPool.length < LKH_POOL_SIZE) {
      lkhPool.push(await LKHModule({
        print: () => {},
        printErr: (t) => console.error("[lkh-wasm]", t),
        // Emscripten's own scriptDirectory detection resolves against this
        // worker's own URL (self.location - importScripts doesn't change it),
        // not wasm/lkh_wasm.js's URL, so left to its own devices it looks for
        // lkh_wasm.wasm next to solverWorker.js instead of under wasm/ -
        // works by coincidence when everything's at the domain root, breaks
        // under any subpath (e.g. GitHub Pages). locateFile overrides that.
        locateFile: (path) => "wasm/" + path,
      }));
    }
  } finally {
    lkhPoolRefilling = false;
  }
}

self.__lkhWasmSolve = function (instanceText, parText) {
  if (lkhPool.length === 0) {
    console.error("[lkh bridge] pool exhausted, falling back to pure-Go sub-solver");
    return null;
  }
  const Module = lkhPool.pop();
  refillLkhPool(); // async top-up, don't block returning this result
  try {
    Module.FS.writeFile("/lkh_sub.vrptw", instanceText);
    Module.FS.writeFile("/lkh_sub.par", parText);
    Module.callMain(["/lkh_sub.par"]);
    return Module.FS.readFile("/lkh_sub.sol", { encoding: "utf8" });
  } catch (e) {
    console.error("[lkh bridge] call failed:", e);
    return null;
  }
};

self.onmessage = async (event) => {
  const { type, instanceText, args } = event.data || {};
  if (type !== "solve") return;

  try {
    vfsWriteFile("/instance.txt", instanceText);

    if (args.useLkh && !lkhPoolPrimed) {
      lkhPoolPrimed = true;
      if (lkhModuleAvailable) {
        await refillLkhPool();
      } else {
        // Leave lkhPool empty: __lkhWasmSolve sees pool.length === 0 and
        // returns null, which solver/lkh_wasm.go already treats as "LKH
        // unavailable, fall back to the pure-Go sub-solver" - so a missing
        // LKH build artifact degrades this one solve instead of failing it.
        console.error("[lkh bridge] -use-lkh requested but LKH module never loaded; sub-solver will fall back to pure-Go heuristic.");
      }
    }

    const go = new Go();
    const argv = [
      "js",
      "-file", "/instance.txt",
      "-iterations", String(args.iterations),
      "-llm-threshold", String(args.llmThreshold),
      "-seed", String(args.seed),
      `-use-lkh=${!!args.useLkh}`,
    ];
    go.argv = argv;

    const resp = await fetch("wasm/solver.wasm");
    if (!resp.ok) {
      throw new Error(`Failed to fetch solver.wasm: HTTP ${resp.status}`);
    }
    const bytes = await resp.arrayBuffer();
    const { instance } = await WebAssembly.instantiate(bytes, go.importObject);
    await go.run(instance);
  } catch (e) {
    self.postMessage({ type: "error", message: `Worker failure: ${e && e.message ? e.message : e}` });
  }
};
