package ghostty

import (
	"fmt"
	"os"
	"sync"

	"github.com/bnema/purego"
	"github.com/bnema/purego-libghostty/internal/loader"
)

var (
	loadOnce sync.Once
	loadErr  error

	openLibrary     = loader.OpenDefault
	registerLibrary = register
	closeLibrary    = purego.Dlclose
)

func Load() error {
	loadOnce.Do(func() {
		handle, err := openLibrary(os.Getenv("LIBGHOSTTY_PATH"), "ghostty-internal.so")
		if err != nil {
			loadErr = fmt.Errorf("ghostty: load: %w", err)
			return
		}
		if err := registerLibrary(handle); err != nil {
			_ = closeLibrary(handle)
			loadErr = fmt.Errorf("ghostty: register: %w", err)
		}
	})
	return loadErr
}

func resetLoadForTest() {
	loadOnce = sync.Once{}
	loadErr = nil
	openLibrary = loader.OpenDefault
	registerLibrary = register
	closeLibrary = purego.Dlclose
}
