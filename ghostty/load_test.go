package ghostty

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bnema/purego"
	"github.com/bnema/purego-libghostty/internal/loader"
)

func TestLoadConcurrentSuccess(t *testing.T) {
	reset := beginLoadTest(t)
	defer reset()

	var failures loadTestFailures
	var opens, registrations, closes atomic.Int32
	openLibrary = func(override string, candidates ...string) (uintptr, error) {
		opens.Add(1)
		if override != "" || len(candidates) != 1 || candidates[0] != "ghostty-internal.so" {
			failures.add("open args = %q, %#v", override, candidates)
		}
		return 101, nil
	}
	registerLibrary = func(uintptr) error {
		registrations.Add(1)
		return nil
	}
	closeLibrary = func(uintptr) error {
		closes.Add(1)
		return nil
	}

	const callers = 32
	results := make(chan error, callers)
	var group sync.WaitGroup
	group.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer group.Done()
			results <- Load()
		}()
	}
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("opens = %d, want 1", got)
	}
	if got := registrations.Load(); got != 1 {
		t.Fatalf("registrations = %d, want 1", got)
	}
	if got := closes.Load(); got != 0 {
		t.Fatalf("closes = %d, want 0", got)
	}
	failures.check(t)
}

func TestLoadConcurrentFailureIsSticky(t *testing.T) {
	reset := beginLoadTest(t)
	defer reset()

	sentinel := errors.New("library unavailable")
	var failures loadTestFailures
	var opens atomic.Int32
	openLibrary = func(string, ...string) (uintptr, error) {
		opens.Add(1)
		return 0, sentinel
	}
	registerLibrary = func(uintptr) error {
		failures.add("register called after open failure")
		return nil
	}

	const callers = 24
	results := make(chan error, callers)
	var group sync.WaitGroup
	group.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer group.Done()
			results <- Load()
		}()
	}
	group.Wait()
	close(results)
	var first error
	for err := range results {
		if first == nil {
			first = err
		}
		if !errors.Is(err, sentinel) || err.Error() != first.Error() {
			t.Fatalf("Load error = %v, want sticky wrapped failure", err)
		}
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("opens = %d, want 1", got)
	}
	if err := Load(); !errors.Is(err, sentinel) || err.Error() != first.Error() {
		t.Fatalf("sticky error = %v, want %v", err, first)
	}
	failures.check(t)
}

func TestLoadUsesOverrideBeforeFirstCall(t *testing.T) {
	reset := beginLoadTest(t)
	defer reset()
	t.Setenv("LIBGHOSTTY_PATH", "/tmp/libghostty-test.so")

	var gotOverride string
	var gotCandidates []string
	openLibrary = func(override string, candidates ...string) (uintptr, error) {
		gotOverride = override
		gotCandidates = append([]string(nil), candidates...)
		return 202, nil
	}
	registerLibrary = func(uintptr) error { return nil }
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	if gotOverride != "/tmp/libghostty-test.so" {
		t.Fatalf("override = %q", gotOverride)
	}
	if len(gotCandidates) != 1 || gotCandidates[0] != "ghostty-internal.so" {
		t.Fatalf("candidates = %#v", gotCandidates)
	}
}

func TestLoadMissingSymbolPropagationAndCleanup(t *testing.T) {
	reset := beginLoadTest(t)
	defer reset()

	sentinel := errors.New("symbol missing")
	var closed uintptr
	openLibrary = func(string, ...string) (uintptr, error) { return 303, nil }
	registerLibrary = func(uintptr) error {
		return fmt.Errorf("resolve ghostty_config_new: %w", sentinel)
	}
	closeLibrary = func(handle uintptr) error {
		closed = handle
		return nil
	}
	ConfigNew = nil
	if err := Load(); !errors.Is(err, sentinel) {
		t.Fatalf("Load error = %v, want missing symbol", err)
	} else if want := "ghostty: register: resolve ghostty_config_new: symbol missing"; err.Error() != want {
		t.Fatalf("Load error = %q, want %q", err, want)
	}
	if ConfigNew != nil {
		t.Fatal("ConfigNew is non-nil after failed Load")
	}
	if closed != 303 {
		t.Fatalf("closed handle = %d, want 303", closed)
	}
}

func TestLoadFailureRemainsStickyAfterInjectionChanges(t *testing.T) {
	reset := beginLoadTest(t)
	defer reset()

	first := errors.New("first failure")
	var opens atomic.Int32
	openLibrary = func(string, ...string) (uintptr, error) {
		opens.Add(1)
		return 0, first
	}
	if err := Load(); !errors.Is(err, first) {
		t.Fatal(err)
	}
	openLibrary = func(string, ...string) (uintptr, error) {
		t.Fatal("injected replacement was used after sticky failure")
		return 0, nil
	}
	if err := Load(); !errors.Is(err, first) {
		t.Fatalf("second Load error = %v", err)
	}
	if opens.Load() != 1 {
		t.Fatalf("opens = %d, want 1", opens.Load())
	}
}

type loadTestFailures struct {
	mu       sync.Mutex
	messages []string
}

func (f *loadTestFailures) add(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, fmt.Sprintf(format, args...))
}

func (f *loadTestFailures) check(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	messages := append([]string(nil), f.messages...)
	f.mu.Unlock()
	if len(messages) != 0 {
		t.Fatalf("concurrent test failures: %s", strings.Join(messages, "; "))
	}
}

func resetLoadForTest() {
	loadOnce = sync.Once{}
	loadErr = nil
	openLibrary = loader.OpenDefault
	registerLibrary = register
	closeLibrary = purego.Dlclose
}

func beginLoadTest(t *testing.T) func() {
	t.Helper()
	t.Setenv("LIBGHOSTTY_PATH", "")
	resetLoadForTest()
	return func() {
		resetLoadForTest()
	}
}
