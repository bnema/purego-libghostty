package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bnema/purego-libghostty/internal/generate"
)

func main() {
	upstreamPath := flag.String("upstream", "upstream.json", "upstream pin file")
	sourceDir := flag.String("source", "", "matching Ghostty checkout")
	clang := flag.String("clang", "clang", "Clang executable")
	inspect := flag.Bool("inspect", false, "write the normalized header model")
	flag.Parse()
	if !*inspect {
		fmt.Fprintln(os.Stderr, "ghosttygen: -inspect is required")
		os.Exit(2)
	}
	upstream, err := generate.LoadUpstream(*upstreamPath)
	if err != nil {
		fail(err)
	}
	source, err := generate.ResolveSource(context.Background(), upstream, *sourceDir)
	if err != nil {
		fail(err)
	}
	defer source.Close()
	header := filepath.Join(source.Dir, "include", "ghostty.h")
	if err := generate.WriteModel(context.Background(), *clang, header, filepath.Dir(header), generate.LinuxTargets, os.Stdout); err != nil {
		fail(err)
	}
}

func fail(err error) { fmt.Fprintln(os.Stderr, "ghosttygen:", err); os.Exit(1) }
