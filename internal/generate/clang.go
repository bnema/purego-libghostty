package generate

import (
	"context"
	"fmt"
	"os/exec"
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
	args := []string{"--target=" + target.Triple, "-std=c11", "-I", includeDir, "-fsyntax-only", "-Xclang", "-ast-dump=json", "-x", "c"}
	for _, define := range defines {
		args = append(args, "-D"+define)
	}
	args = append(args, header)
	return clangOutput(ctx, clang, args...)
}

func inspectMacros(ctx context.Context, clang, header, includeDir string, target Target) ([]byte, error) {
	return clangOutput(ctx, clang, "--target="+target.Triple, "-std=c11", "-I", includeDir, "-dM", "-E", "-x", "c", header)
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
