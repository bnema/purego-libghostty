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
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ghosttygen:", err)
		os.Exit(1)
	}
}

func run() error {
	upstreamPath := flag.String("upstream", "upstream.json", "upstream pin file")
	sourceDir := flag.String("source", os.Getenv("GHOSTTY_SOURCE_DIR"), "matching Ghostty checkout")
	clang := flag.String("clang", "clang", "Clang executable")
	inspect := flag.Bool("inspect", false, "write the normalized header model")
	out := flag.String("out", "", "output directory for generated packages")
	flag.Parse()
	if !*inspect && *out == "" {
		return fmt.Errorf("-out is required without -inspect")
	}
	upstream, err := generate.LoadUpstream(*upstreamPath)
	if err != nil {
		return err
	}
	source, err := generate.ResolveSource(context.Background(), upstream, *sourceDir)
	if err != nil {
		return err
	}
	defer source.Close()
	header := filepath.Join(source.Dir, "include", "ghostty.h")
	if *inspect {
		return generate.WriteModel(context.Background(), *clang, header, filepath.Dir(header), generate.LinuxTargets, os.Stdout)
	}
	model, err := generate.InspectHeader(context.Background(), *clang, header, filepath.Dir(header), generate.LinuxTargets, nil)
	if err != nil {
		return err
	}
	layouts := make(map[string]map[string]generate.RecordLayout, len(generate.LinuxTargets))
	for _, target := range generate.LinuxTargets {
		layout, err := generate.InspectRecordLayouts(context.Background(), *clang, header, filepath.Dir(header), target, model)
		if err != nil {
			return err
		}
		layouts[target.GOARCH] = layout
	}
	return generate.EmitTypes(model, layouts, generate.Output{Dir: *out, Package: "ghostty", Upstream: upstream})
}
