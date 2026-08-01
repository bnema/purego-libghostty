package loader

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/bnema/purego"
)

func TestOpenUsesOverrideOnlyWhenSet(t *testing.T) {
	var paths []string
	ops := Ops{
		Open: func(path string, flags int) (uintptr, error) {
			paths = append(paths, path)
			if flags != purego.RTLD_NOW|purego.RTLD_GLOBAL {
				t.Fatalf("flags = %d, want %d", flags, purego.RTLD_NOW|purego.RTLD_GLOBAL)
			}
			return 11, nil
		},
		Close: func(uintptr) error { return nil },
	}
	handle, err := Open(ops, "/tmp/override.so", "candidate-a.so", "candidate-b.so")
	if err != nil {
		t.Fatal(err)
	}
	if handle != 11 {
		t.Fatalf("handle = %d, want 11", handle)
	}
	if !reflect.DeepEqual(paths, []string{"/tmp/override.so"}) {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestOpenFallsBackToCandidate(t *testing.T) {
	firstErr := errors.New("first missing")
	var paths []string
	ops := Ops{
		Open: func(path string, flags int) (uintptr, error) {
			paths = append(paths, path)
			if path == "first.so" {
				return 0, firstErr
			}
			return 22, nil
		},
		Close: func(uintptr) error { return nil },
	}
	handle, err := Open(ops, "", "first.so", "second.so")
	if err != nil {
		t.Fatal(err)
	}
	if handle != 22 {
		t.Fatalf("handle = %d, want 22", handle)
	}
	if !reflect.DeepEqual(paths, []string{"first.so", "second.so"}) {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestOpenClosesFailedHandles(t *testing.T) {
	firstErr := errors.New("first missing")
	secondErr := errors.New("second missing")
	var closed []uintptr
	ops := Ops{
		Open: func(path string, _ int) (uintptr, error) {
			if path == "first.so" {
				return 31, firstErr
			}
			return 32, secondErr
		},
		Close: func(handle uintptr) error {
			closed = append(closed, handle)
			return nil
		},
	}
	_, err := Open(ops, "", "first.so", "second.so")
	if err == nil {
		t.Fatal("Open unexpectedly succeeded")
	}
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("error = %v, missing attempt error", err)
	}
	if !reflect.DeepEqual(closed, []uintptr{31, 32}) {
		t.Fatalf("closed = %#v", closed)
	}
}

func TestOpenReportsAllAttempts(t *testing.T) {
	firstErr := errors.New("first missing")
	secondErr := errors.New("second missing")
	ops := Ops{
		Open: func(path string, _ int) (uintptr, error) {
			if path == "first.so" {
				return 0, firstErr
			}
			return 0, secondErr
		},
		Close: func(uintptr) error { return nil },
	}
	_, err := Open(ops, "", "first.so", "second.so")
	if err == nil {
		t.Fatal("Open unexpectedly succeeded")
	}
	message := err.Error()
	for _, want := range []string{"first.so", "second.so", "first missing", "second missing"} {
		if !strings.Contains(message, want) {
			t.Errorf("error %q missing %q", message, want)
		}
	}
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("error = %v, missing joined errors", err)
	}
}
