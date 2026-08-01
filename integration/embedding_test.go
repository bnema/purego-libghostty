//go:build integration

package integration

import (
	"runtime"
	"testing"

	"github.com/bnema/purego-libghostty/ghostty"
)

func TestEmbeddingRawLifecycle(t *testing.T) {
	if err := ghostty.Load(); err != nil {
		t.Fatal(err)
	}
	argv0 := append([]byte("purego-libghostty-test"), 0)
	argv := []*byte{&argv0[0]}
	if code := ghostty.Init(uintptr(len(argv)), &argv[0]); code != 0 {
		t.Fatalf("ghostty_init returned %d", code)
	}
	runtime.KeepAlive(argv0)
	runtime.KeepAlive(argv)
	info := ghostty.Info()
	if info.Version == nil || info.VersionLen == 0 {
		t.Fatal("empty version")
	}
	cfg := ghostty.ConfigNew()
	if cfg == 0 {
		t.Fatal("nil config")
	}
	ghostty.ConfigFinalize(cfg)
	ghostty.ConfigFree(cfg)
}
