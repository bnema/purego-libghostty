package generate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCrossTargetHeaderInspectionWithoutHostSysroot(t *testing.T) {
	realClang, err := exec.LookPath("clang")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	header := filepath.Join(root, "probe.h")
	if err := os.WriteFile(header, []byte(`#include <sys/types.h>
#define GHOSTTY_API __attribute__((visibility("default")))
typedef struct {
  ssize_t value;
} ghostty_probe_s;
GHOSTTY_API ghostty_probe_s ghostty_probe(void);
`), 0o600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "targets.log")
	clang := filepath.Join(root, "clang-no-sysroot")
	script := fmt.Sprintf(`#!/bin/sh
set -eu
previous=
found=false
for arg in "$@"; do
  case "$arg" in
    --target=*) printf '%%s\n' "$arg" >> %s ;;
  esac
  if [ "$previous" = "-isystem" ]; then
    test -f "$arg/sys/types.h"
    found=true
  fi
  previous="$arg"
done
test "$found" = true
exec %s -nostdinc "$@"
`, shellQuote(log), shellQuote(realClang))
	if err := os.WriteFile(clang, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	model, err := InspectHeader(context.Background(), clang, header, root, LinuxTargets, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasType(model, "ghostty_probe_s") || !hasFunction(model, "ghostty_probe") {
		t.Fatalf("model = %#v", model)
	}
	for _, target := range LinuxTargets {
		layouts, err := InspectRecordLayouts(context.Background(), clang, header, root, target, model)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := layouts["ghostty_probe_s"]; !ok {
			t.Fatalf("missing %s layout: %#v", target.GOARCH, layouts)
		}
	}

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, target := range LinuxTargets {
		if !strings.Contains(got, "--target="+target.Triple) {
			t.Fatalf("target %s was not inspected: %q", target.Triple, got)
		}
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
