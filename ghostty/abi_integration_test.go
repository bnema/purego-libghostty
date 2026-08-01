//go:build integration

package ghostty

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type abiProbeField struct {
	Name   string  `json:"name"`
	Offset uintptr `json:"offset"`
}

type abiProbeRecord struct {
	Name   string          `json:"name"`
	Size   uintptr         `json:"size"`
	Align  uintptr         `json:"align"`
	Fields []abiProbeField `json:"fields"`
}

func TestEmbeddingABI(t *testing.T) {
	source := os.Getenv("GHOSTTY_SOURCE_DIR")
	if source == "" {
		t.Fatal("GHOSTTY_SOURCE_DIR is required")
	}
	records := abiRecords()
	if len(records) == 0 {
		t.Fatal("generated ABI metadata is empty")
	}
	dir := t.TempDir()
	probe := filepath.Join(dir, "abi-probe.c")
	if err := os.WriteFile(probe, []byte(abiProbeSource(records)), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "abi-probe")
	clang := os.Getenv("CLANG")
	if clang == "" {
		clang = "clang"
	}
	compileContext, cancelCompile := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelCompile()
	compile := exec.CommandContext(compileContext, clang, "-std=c11", "-I", filepath.Join(source, "include"), probe, "-o", binary)
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("compile ABI probe: %v\n%s", err, output)
	}
	runContext, cancelRun := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelRun()
	command := exec.CommandContext(runContext, binary)
	output, err := command.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("run ABI probe: %v\n%s", err, exitErr.Stderr)
		}
		t.Fatal(err)
	}
	var actual []abiProbeRecord
	if err := json.Unmarshal(output, &actual); err != nil {
		t.Fatalf("decode ABI probe output %q: %v", output, err)
	}
	if len(actual) != len(records) {
		t.Fatalf("ABI record count for %s = %d, want %d", runtime.GOARCH, len(actual), len(records))
	}
	for i, expected := range records {
		got := actual[i]
		if got.Name != expected.name {
			t.Fatalf("ABI record %d name = %q, want %q", i, got.Name, expected.name)
		}
		if got.Size != expected.size {
			t.Errorf("%s size on %s = %d, want %d", expected.name, runtime.GOARCH, got.Size, expected.size)
		}
		if got.Align != expected.align {
			t.Errorf("%s alignment on %s = %d, want %d", expected.name, runtime.GOARCH, got.Align, expected.align)
		}
		if len(got.Fields) != len(expected.fields) {
			t.Fatalf("%s field count on %s = %d, want %d", expected.name, runtime.GOARCH, len(got.Fields), len(expected.fields))
		}
		for j, expectedField := range expected.fields {
			gotField := got.Fields[j]
			if gotField.Name != expectedField.name {
				t.Fatalf("%s field %d name = %q, want %q", expected.name, j, gotField.Name, expectedField.name)
			}
			if gotField.Offset != expectedField.offset {
				t.Errorf("%s.%s offset on %s = %d, want %d", expected.name, expectedField.name, runtime.GOARCH, gotField.Offset, expectedField.offset)
			}
		}
	}
}

func abiProbeSource(records []abiRecord) string {
	var source strings.Builder
	source.WriteString("#include <stddef.h>\n#include <stdio.h>\n#include <ghostty.h>\n\n")
	for i, record := range records {
		if strings.Contains(record.name, ".") {
			source.WriteString("typedef __typeof__(" + abiProbeLValue(record.name) + ") ghostty_probe_record_" + fmt.Sprint(i) + "_t;\n")
		}
	}
	source.WriteString("\nint main(void) {\n  fputs(\"[\", stdout);\n")
	for i, record := range records {
		if i != 0 {
			source.WriteString("  fputs(\",\", stdout);\n")
		}
		typeName := record.name
		if strings.Contains(record.name, ".") {
			typeName = "ghostty_probe_record_" + fmt.Sprint(i) + "_t"
		}
		source.WriteString("  printf(\"{\\\"name\\\":\\\"" + record.name + "\\\",\\\"size\\\":%zu,\\\"align\\\":%zu,\\\"fields\\\":[\", sizeof(" + typeName + "), _Alignof(" + typeName + "));\n")
		for j, field := range record.fields {
			if j != 0 {
				source.WriteString("  fputs(\",\", stdout);\n")
			}
			source.WriteString("  printf(\"{\\\"name\\\":\\\"" + field.name + "\\\",\\\"offset\\\":%zu}\", offsetof(" + typeName + ", " + field.name + "));\n")
		}
		source.WriteString("  fputs(\"]}\", stdout);\n")
	}
	source.WriteString("  fputs(\"]\\n\", stdout);\n  return 0;\n}\n")
	return source.String()
}

func abiProbeLValue(name string) string {
	parts := strings.Split(name, ".")
	expression := "((" + parts[0] + " *)0)"
	for _, part := range parts[1:] {
		expression = "(" + expression + ")->" + part
	}
	return expression
}
