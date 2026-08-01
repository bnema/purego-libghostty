package generate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Upstream struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
}

type ResolvedSource struct {
	Dir     string
	cleanup func()
}

func LoadUpstream(path string) (Upstream, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Upstream{}, fmt.Errorf("read upstream config %q: %w", path, err)
	}
	var upstream Upstream
	if err := json.Unmarshal(data, &upstream); err != nil {
		return Upstream{}, fmt.Errorf("parse upstream config %q: %w", path, err)
	}
	if strings.TrimSpace(upstream.Repository) == "" {
		return Upstream{}, fmt.Errorf("ghostty upstream repository is empty")
	}
	if !isCommit(upstream.Commit) {
		return Upstream{}, fmt.Errorf("ghostty upstream commit must be 40 lowercase hexadecimal characters")
	}
	return upstream, nil
}

func ResolveSource(ctx context.Context, upstream Upstream, override string) (ResolvedSource, error) {
	if override != "" {
		head, err := gitOutput(ctx, override, "rev-parse", "HEAD")
		if err != nil {
			return ResolvedSource{}, fmt.Errorf("ghostty source override rev-parse HEAD: %w", err)
		}
		if got := strings.TrimSpace(head); got != upstream.Commit {
			return ResolvedSource{}, fmt.Errorf("ghostty source override must be commit %s, found %s", upstream.Commit, got)
		}
		status, err := gitOutput(ctx, override, "status", "--porcelain")
		if err != nil {
			return ResolvedSource{}, fmt.Errorf("ghostty source override status: %w", err)
		}
		if strings.TrimSpace(status) != "" {
			return ResolvedSource{}, fmt.Errorf("ghostty source override has uncommitted changes")
		}
		return ResolvedSource{Dir: override}, nil
	}

	dir, err := os.MkdirTemp("", "ghostty-")
	if err != nil {
		return ResolvedSource{}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	for _, args := range [][]string{
		{"init"},
		{"remote", "add", "origin", upstream.Repository},
		{"fetch", "--depth=1", "origin", upstream.Commit},
		{"checkout", "--detach", "FETCH_HEAD"},
	} {
		commandCtx := ctx
		cancel := func() {}
		if args[0] == "fetch" {
			commandCtx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		}
		_, err := gitOutput(commandCtx, dir, args...)
		cancel()
		if err != nil {
			cleanup()
			return ResolvedSource{}, err
		}
	}
	return ResolvedSource{Dir: dir, cleanup: cleanup}, nil
}

func (s ResolvedSource) Close() {
	if s.cleanup != nil {
		s.cleanup()
	}
}

func isCommit(commit string) bool {
	if len(commit) != 40 {
		return false
	}
	for _, r := range commit {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}
