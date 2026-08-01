package generate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const pinnedCommit = "4d605bf0d819df901a0332bbb320dc849fdd82e4"

func TestLoadUpstream(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upstream.json")
	if err := os.WriteFile(path, []byte(`{"repository":"https://example.test/ghostty.git","commit":"`+pinnedCommit+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	upstream, err := LoadUpstream(path)
	if err != nil {
		t.Fatal(err)
	}
	if upstream.Repository != "https://example.test/ghostty.git" || upstream.Commit != pinnedCommit {
		t.Fatalf("LoadUpstream() = %#v", upstream)
	}

	for _, input := range []string{
		`{"repository":"","commit":"` + pinnedCommit + `"}`,
		`{"repository":"https://example.test/ghostty.git","commit":"` + strings.ToUpper(pinnedCommit) + `"}`,
		`{"repository":"https://example.test/ghostty.git","commit":"abc"}`,
	} {
		if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadUpstream(path); err == nil {
			t.Fatalf("LoadUpstream(%s) succeeded", input)
		}
	}
}

func TestResolveSourceAcceptsMatchingOverride(t *testing.T) {
	repository, override, commit, _ := localRepository(t)
	git(t, override, "checkout", "--detach", commit)
	source, err := ResolveSource(context.Background(), Upstream{Repository: repository, Commit: commit}, override)
	if err != nil {
		t.Fatal(err)
	}
	source.Close()
	if _, err := os.Stat(override); err != nil {
		t.Fatalf("override was removed: %v", err)
	}
}

func TestResolveSourceRejectsMismatchedOverride(t *testing.T) {
	repository, override, commit, _ := localRepository(t)
	if _, err := ResolveSource(context.Background(), Upstream{Repository: repository, Commit: commit}, override); err == nil {
		t.Fatal("ResolveSource() succeeded for mismatched override")
	}
}

func TestResolveSourceFetchesPinnedCommit(t *testing.T) {
	repository, _, first, _ := localRepository(t)
	source, err := ResolveSource(context.Background(), Upstream{Repository: repository, Commit: first}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, source.Dir, "rev-parse", "HEAD"); got != first {
		t.Fatalf("resolved HEAD = %s, want %s", got, first)
	}
	source.Close()
	if _, err := os.Stat(source.Dir); !os.IsNotExist(err) {
		t.Fatalf("temporary source still exists: %v", err)
	}
}

func localRepository(t *testing.T) (string, string, string, string) {
	t.Helper()
	origin := filepath.Join(t.TempDir(), "origin.git")
	git(t, "", "init", "--bare", origin)
	work := t.TempDir()
	git(t, work, "init")
	git(t, work, "config", "user.email", "test@example.com")
	git(t, work, "config", "user.name", "Test User")
	git(t, work, "remote", "add", "origin", origin)

	writeCommit(t, work, "one", "one")
	first := git(t, work, "rev-parse", "HEAD")
	git(t, work, "push", "origin", "HEAD")
	writeCommit(t, work, "two", "two")
	second := git(t, work, "rev-parse", "HEAD")
	git(t, work, "push", "origin", "HEAD")
	return origin, work, first, second
}

func writeCommit(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", name)
	git(t, dir, "commit", "-m", name)
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	if dir != "" {
		command.Dir = dir
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
