package ghostty_test

import (
	"log"
	"runtime"

	"github.com/bnema/purego-libghostty/ghostty"
)

func Example_rawConfig() {
	if err := ghostty.Load(); err != nil {
		log.Fatal(err)
	}
	argv0 := append([]byte("purego-libghostty"), 0)
	argv := []*byte{&argv0[0]}
	if code := ghostty.Init(uintptr(len(argv)), &argv[0]); code != 0 {
		log.Fatalf("ghostty_init returned %d", code)
	}
	runtime.KeepAlive(argv0)
	runtime.KeepAlive(argv)
	cfg := ghostty.ConfigNew()
	if cfg == 0 {
		log.Fatal("ghostty_config_new returned nil")
	}
	defer ghostty.ConfigFree(cfg)
	ghostty.ConfigFinalize(cfg)
}
