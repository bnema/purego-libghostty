package generate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Target struct {
	GOARCH string
	Triple string
}

var LinuxTargets = []Target{
	{GOARCH: "amd64", Triple: "x86_64-linux-gnu"},
	{GOARCH: "arm64", Triple: "aarch64-linux-gnu"},
}

type PlatformVariant struct {
	Reason  string
	Defines []string
}

var EmbeddingVariants = []PlatformVariant{{Reason: "__APPLE__", Defines: []string{"__APPLE__=1"}}}

func inspectAST(ctx context.Context, clang, header, includeDir string, target Target, defines ...string) ([]byte, error) {
	prelude, systemIncludeDir, cleanup, err := targetPrelude(target)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	args := []string{"--target=" + target.Triple, "-include", prelude, "-ffreestanding", "-std=c11", "-isystem", systemIncludeDir, "-I", includeDir, "-fsyntax-only", "-Xclang", "-ast-dump=json", "-x", "c"}
	for _, define := range defines {
		args = append(args, "-D"+define)
	}
	args = append(args, header)
	return clangOutput(ctx, clang, args...)
}

func inspectMacros(ctx context.Context, clang, header, includeDir string, target Target) ([]byte, error) {
	prelude, systemIncludeDir, cleanup, err := targetPrelude(target)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return clangOutput(ctx, clang, "--target="+target.Triple, "-include", prelude, "-ffreestanding", "-std=c11", "-isystem", systemIncludeDir, "-I", includeDir, "-dM", "-E", "-x", "c", header)
}

func targetPrelude(target Target) (string, string, func(), error) {
	root, err := os.MkdirTemp("", "ghosttygen-"+target.GOARCH+"-*")
	if err != nil {
		return "", "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	prelude := filepath.Join(root, "prelude.h")
	if err := os.WriteFile(prelude, []byte("#ifndef _SYS_TYPES_H\n#define _SYS_TYPES_H 1\ntypedef long ssize_t;\n#endif\n"), 0o600); err != nil {
		cleanup()
		return "", "", func() {}, err
	}
	sysTypesDir := filepath.Join(root, "sys")
	if err := os.Mkdir(sysTypesDir, 0o700); err != nil {
		cleanup()
		return "", "", func() {}, err
	}
	if err := os.WriteFile(filepath.Join(sysTypesDir, "types.h"), []byte("#ifndef _SYS_TYPES_H\n#define _SYS_TYPES_H 1\ntypedef long ssize_t;\n#endif\n"), 0o600); err != nil {
		cleanup()
		return "", "", func() {}, err
	}
	return prelude, root, cleanup, nil
}

func clangOutput(ctx context.Context, clang string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, clang, args...)
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("clang %s: %w\nstderr: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return []byte(stdout.String()), nil
}
