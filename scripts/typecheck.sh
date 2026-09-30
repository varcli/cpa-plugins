#!/usr/bin/env bash
# typecheck.sh — compile-check a c-shared plugin on a machine with no C toolchain.
#
# The plugins' cgo files are the C ABI shim (main.go; trae also has
# intl_main.go); every other file is pure Go. With CGO_ENABLED=0 and no C
# toolchain, `go build`/`go vet` give the top-level package zero coverage, in
# one of two shapes: a package made only of cgo files is silently skipped
# (exit 0 — a false green; cline and kiro are like this), while a package that
# also has non-cgo files fails with bogus `undefined: storedAuth` errors
# (a false red). Either way a port can be thousands of lines wrong and still
# look fine. This harness makes a shadow copy, strips the cgo preamble and C.
# qualifiers from EVERY cgo file, adds an equivalent pure-Go shim in the same
# package, and type-checks that — catching real errors in every other file.
#
# Usage: scripts/typecheck.sh [-t] plugins/<id>
#   -t  also run `go test ./...` on the shadow copy. Tests that stub the
#       hostAuth*/hostCall seams work; anything reaching the real C bridge
#       sees the shim's zero values, so a -t run is a build+unit check, not
#       an integration one.
set -euo pipefail

run_tests=0
if [ "${1:-}" = "-t" ]; then
	run_tests=1
	shift
fi

plugin_dir="${1:?usage: typecheck.sh [-t] plugins/<id>}"
plugin_dir="${plugin_dir%/}"
id="$(basename "$plugin_dir")"
src="$(cd "$plugin_dir" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

cp -r "$src/." "$work/"

# 1. Strip the cgo preamble and the `import "C"` line from every cgo file, and
#    drop every `C.` qualifier so the remaining code reads as plain Go. A
#    plugin may have more than one (trae: main.go + intl_main.go), and each
#    carries package-level declarations the rest of the package needs.
python - "$work" <<'PY'
import os, re, sys
root = sys.argv[1]
for name in sorted(os.listdir(root)):
    if not name.endswith('.go'):
        continue
    path = os.path.join(root, name)
    src = open(path, encoding='utf-8').read()
    if 'import "C"' not in src:
        continue
    # Remove the /* ... */ preamble that immediately precedes import "C".
    src = re.sub(r'/\*.*?\*/\s*import "C"\n', '', src, count=1, flags=re.S)
    src = src.replace('import "C"\n', '')
    src = re.sub(r'\bC\.', '', src)
    # //export directives are cgo-only; keep them as inert comments.
    open(path, 'w', encoding='utf-8', newline='\n').write(src)
PY

# 2. Emit the pure-Go stand-in for the C types/functions main.go used.
cat > "$work/zz_cgo_shim.go" <<'GO'
package main

// Pure-Go stand-ins for the cgo symbols, so the package type-checks without a
// C toolchain. Never shipped: this file exists only in the shadow copy.

import "unsafe"

type char = byte
type uint8_t = uint8
type uint32_t = uint32
type size_t = uintptr

type cliproxy_buffer struct {
	ptr unsafe.Pointer
	len size_t
}

type cliproxy_host_call_fn func(unsafe.Pointer, *char, *uint8_t, size_t, *cliproxy_buffer) int
type cliproxy_host_free_fn func(unsafe.Pointer, size_t)

type cliproxy_host_api struct {
	abi_version uint32_t
	host_ctx    unsafe.Pointer
	call        cliproxy_host_call_fn
	free_buffer cliproxy_host_free_fn
}

type cliproxy_plugin_call_fn func(*char, *uint8_t, size_t, *cliproxy_buffer) int
type cliproxy_plugin_free_fn func(unsafe.Pointer, size_t)
type cliproxy_plugin_shutdown_fn func()

type cliproxy_plugin_api struct {
	abi_version uint32_t
	call        cliproxy_plugin_call_fn
	free_buffer cliproxy_plugin_free_fn
	shutdown    cliproxy_plugin_shutdown_fn
}

func CString(s string) *char  { return nil }
func CBytes(b []byte) unsafe.Pointer { return nil }
func GoString(p *char) string { return "" }
func GoBytes(p unsafe.Pointer, n int) []byte { return nil }
func free(p unsafe.Pointer)   {}

func store_host_api(host *cliproxy_host_api) {}
func call_host_api(method *char, req *uint8_t, n size_t, resp *cliproxy_buffer) int { return 0 }
func free_host_buffer(ptr unsafe.Pointer, n size_t) {}
func wb_call_host(api *cliproxy_host_api, method *char, req *uint8_t, n size_t, resp *cliproxy_buffer) int {
	return 0
}
func wb_free_host_buffer(api *cliproxy_host_api, ptr unsafe.Pointer, n size_t) {}
GO

cd "$work"
echo "[typecheck] $id: building shadow copy without cgo"
GOFLAGS=-mod=mod CGO_ENABLED=0 go build ./... 2>&1
GOFLAGS=-mod=mod CGO_ENABLED=0 go vet ./... 2>&1
if [ "$run_tests" = "1" ]; then
	echo "[typecheck] $id: go test ./..."
	GOFLAGS=-mod=mod CGO_ENABLED=0 go test ./... 2>&1
fi
echo "[typecheck] $id: OK"
