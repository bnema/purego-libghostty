package loader

import (
	"errors"
	"fmt"

	"github.com/bnema/purego"
)

type Ops struct {
	Open  func(string, int) (uintptr, error)
	Close func(uintptr) error
}

// Open tries the override, when nonempty, or each candidate in order.
// A nonempty override replaces the candidate list.
func Open(ops Ops, override string, candidates ...string) (uintptr, error) {
	if ops.Open == nil {
		return 0, errors.New("ghostty loader: open operation is nil")
	}
	if override != "" {
		candidates = []string{override}
	}
	if len(candidates) == 0 {
		return 0, errors.New("ghostty loader: no library candidates")
	}

	flags := purego.RTLD_NOW | purego.RTLD_GLOBAL
	attempts := make([]error, 0, len(candidates))
	for _, candidate := range candidates {
		handle, err := ops.Open(candidate, flags)
		if err == nil && handle != 0 {
			return handle, nil
		}
		if err == nil {
			err = errors.New("returned zero handle")
		}
		attempt := fmt.Errorf("open %s: %w", candidate, err)
		if handle != 0 && ops.Close != nil {
			if closeErr := ops.Close(handle); closeErr != nil {
				attempt = errors.Join(attempt, fmt.Errorf("close %s: %w", candidate, closeErr))
			}
		}
		attempts = append(attempts, attempt)
	}
	return 0, errors.Join(attempts...)
}

func OpenDefault(override string, candidates ...string) (uintptr, error) {
	return Open(Ops{Open: purego.Dlopen, Close: purego.Dlclose}, override, candidates...)
}
