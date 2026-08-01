package generate

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInspectHeader(t *testing.T) {
	header := fixtureHeader(t)
	model, err := InspectHeader(context.Background(), "clang", header, filepath.Dir(header), LinuxTargets, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFunction(model, "ghostty_fixture") || hasFunction(model, "ghostty_apple_only") {
		t.Fatalf("functions = %#v", model.Functions)
	}
	if !hasExclusion(model, "ghostty_apple_only", "platform-excluded: __APPLE__") {
		t.Fatalf("exclusions = %#v", model.Exclusions)
	}
	for _, name := range []string{"ghostty_app_t", "ghostty_mode_e", "ghostty_value_u", "ghostty_value_u.named", "ghostty_fixture_s", "ghostty_callback_t"} {
		if !hasType(model, name) {
			t.Errorf("missing reachable type %q", name)
		}
	}
	if hasType(model, "size_t") || hasType(model, "ghostty_override_only_u") {
		t.Fatalf("unexpected types = %#v", model.Types)
	}
	if !hasEnumValues(model, "ghostty_mode_e", []int64{0, 4}) {
		t.Fatalf("enum values = %#v", model.Types)
	}
	first, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("model JSON is not deterministic")
	}
}

func TestSelectPublic(t *testing.T) {
	header := fixtureHeader(t)
	model, err := InspectHeader(context.Background(), "clang", header, filepath.Dir(header), LinuxTargets, []string{"ghostty_override_only_u"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasType(model, "ghostty_override_only_u") {
		t.Fatal("explicit root was not selected")
	}

	noAPI := filepath.Join(t.TempDir(), "noapi.h")
	data, err := os.ReadFile(header)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("GHOSTTY_API ghostty_fixture_s ghostty_fixture"), []byte("ghostty_fixture_s ghostty_fixture"), 1)
	if err := os.WriteFile(noAPI, data, 0o600); err != nil {
		t.Fatal(err)
	}
	without, err := InspectHeader(context.Background(), "clang", noAPI, filepath.Dir(noAPI), LinuxTargets, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hasFunction(without, "ghostty_fixture") {
		t.Fatal("function without GHOSTTY_API was selected")
	}

	unregistered := filepath.Join(t.TempDir(), "unregistered.h")
	data = append(data, []byte("\n#ifdef SOME_OTHER_PLATFORM\nGHOSTTY_API void ghostty_unknown_platform(void);\n#endif\n")...)
	if err := os.WriteFile(unregistered, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectHeader(context.Background(), "clang", unregistered, filepath.Dir(unregistered), LinuxTargets, nil); err == nil || !strings.Contains(err.Error(), "unregistered preprocessor condition") {
		t.Fatalf("unregistered condition error = %v", err)
	}
}

func TestWriteModel(t *testing.T) {
	header := fixtureHeader(t)
	var first, second bytes.Buffer
	for _, output := range []*bytes.Buffer{&first, &second} {
		if err := WriteModel(context.Background(), "clang", header, filepath.Dir(header), LinuxTargets, output); err != nil {
			t.Fatal(err)
		}
	}
	var a, b Model
	if err := json.Unmarshal(first.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) || !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("WriteModel is not deterministic")
	}
}

func fixtureHeader(t *testing.T) string {
	t.Helper()
	return filepath.Join("testdata", "embedding_fixture.h")
}
func hasFunction(m Model, name string) bool {
	for _, d := range m.Functions {
		if d.CName == name {
			return true
		}
	}
	return false
}
func hasType(m Model, name string) bool {
	for _, d := range m.Types {
		if d.CName == name {
			return true
		}
	}
	return false
}
func hasExclusion(m Model, name, reason string) bool {
	for _, d := range m.Exclusions {
		if d.CName == name && d.Reason == reason {
			return true
		}
	}
	return false
}
func hasEnumValues(m Model, name string, values []int64) bool {
	for _, d := range m.Types {
		if d.CName == name {
			got := make([]int64, len(d.EnumValues))
			for i, v := range d.EnumValues {
				got[i] = v.Value
			}
			return reflect.DeepEqual(got, values)
		}
	}
	return false
}
