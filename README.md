# purego-libghostty

Plan 1 provides raw, generated `purego` bindings for Ghostty's embedding API in
`include/ghostty.h`. The bindings target Linux amd64 and arm64, work with
`CGO_ENABLED=0`, and are generated for Ghostty commit
`4d605bf0d819df901a0332bbb320dc849fdd82e4`.

This release does not include VT bindings or managed wrappers. The raw API
inherits ABI instability from the pinned upstream Ghostty release.

## Build Ghostty

Build the pinned upstream embedding library with Zig:

```sh
git clone https://github.com/ghostty-org/ghostty.git
git -C ghostty checkout 4d605bf0d819df901a0332bbb320dc849fdd82e4
cd ghostty
zig build --prefix /tmp/libghostty-prefix -Dapp-runtime=none -Demit-exe=false
```

The resulting library is `/tmp/libghostty-prefix/lib/ghostty-internal.so`.
Set `LIBGHOSTTY_PATH` to its path before the first call to `ghostty.Load`.
Without an override, `ghostty.Load` looks up `ghostty-internal.so` through the
normal dynamic loader search path. Loading is sticky, so set the override before
calling it.

## Raw API

Load the library, initialize it with raw argv storage, inspect build information,
and manage a raw config handle:

```go
if err := ghostty.Load(); err != nil {
	return err
}
argv0 := append([]byte("purego-libghostty"), 0)
argv := []*byte{&argv0[0]}
if code := ghostty.Init(uintptr(len(argv)), &argv[0]); code != 0 {
	return fmt.Errorf("ghostty_init returned %d", code)
}
runtime.KeepAlive(argv0)
runtime.KeepAlive(argv)
info := ghostty.Info()
_ = info.Version
cfg := ghostty.ConfigNew()
if cfg != 0 {
	ghostty.ConfigFinalize(cfg)
	ghostty.ConfigFree(cfg)
}
```

## Generation and checks

Generation requires Clang 22; the `clang` executable used by `go generate`
must report major version 22. Generation resolves the exact pin by default. A
matching local checkout can be used for development:

```sh
GHOSTTY_SOURCE_DIR=/path/to/ghostty make generate
CGO_ENABLED=0 go test ./...
make test
GHOSTTY_SOURCE_DIR=/path/to/ghostty make check
```

`make check` regenerates the bindings, runs ordinary and race tests plus vet,
and fails if generated files are stale. Native ABI and raw lifecycle integration
requires a pinned native build:

```sh
rm -rf /tmp/libghostty-prefix
cd /path/to/ghostty
test "$(git rev-parse HEAD)" = 4d605bf0d819df901a0332bbb320dc849fdd82e4
zig build --prefix /tmp/libghostty-prefix -Dapp-runtime=none -Demit-exe=false
cd /path/to/purego-libghostty
GHOSTTY_SOURCE_DIR=/path/to/ghostty \
LIBGHOSTTY_PATH=/tmp/libghostty-prefix/lib/ghostty-internal.so \
make integration
```

The native integration checks compile a no-cgo C ABI probe from the pinned
header and exercise complete symbol registration, `ghostty_init`, build info,
and raw config create/finalize/free.
