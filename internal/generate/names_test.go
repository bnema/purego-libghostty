package generate

import (
	"context"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGoNamesPreserveEnumConstantSuffix(t *testing.T) {
	model := Model{
		Types: []TypeDecl{{
			CName: "ghostty_mode_e",
			Kind:  TypeEnum,
			EnumValues: []EnumValue{{
				CName: "GHOSTTY_MODE_E",
			}},
		}},
	}
	names, err := AssignGoNames(model)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names["GHOSTTY_MODE_E"], "ModeE"; got != want {
		t.Fatalf("enum constant name = %q, want %q", got, want)
	}
}

func TestGoNamesSanitizeNestedIdentity(t *testing.T) {
	got := GoIdentifier("ghostty_value_u.named")
	if got != "ValueNamed" {
		t.Fatalf("nested identity = %q, want %q", got, "ValueNamed")
	}
	if !token.IsIdentifier(got) || !token.IsExported(got) {
		t.Fatalf("nested identity %q is not an exported Go identifier", got)
	}
}

func TestGoNamesPreserveFunctionAndConstantSuffix(t *testing.T) {
	model := Model{
		Constants: []ConstantDecl{{CName: "GHOSTTY_VALUE_S"}},
		Functions: []FunctionDecl{{CName: "ghostty_call_s"}},
	}
	names, err := AssignGoNames(model)
	if err != nil {
		t.Fatal(err)
	}
	for cName, want := range map[string]string{
		"GHOSTTY_VALUE_S": "ValueS",
		"ghostty_call_s":  "CallS",
	} {
		if got := names[cName]; got != want {
			t.Errorf("%s = %q, want %q", cName, got, want)
		}
	}
}

func TestGoNamesCallbackSuffix(t *testing.T) {
	model := Model{Types: []TypeDecl{{CName: "ghostty_callback_t", Kind: TypeCallback}}}
	names, err := AssignGoNames(model)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names["ghostty_callback_t"], "Callback"; got != want {
		t.Fatalf("callback name = %q, want %q", got, want)
	}
}

func TestGoNamesRealPinnedModel(t *testing.T) {
	source := os.Getenv("GHOSTTY_SOURCE_DIR")
	if source == "" {
		t.Skip("GHOSTTY_SOURCE_DIR is not set")
	}
	upstream, err := LoadUpstream(filepath.Join("..", "..", "upstream.json"))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveSource(context.Background(), upstream, source)
	if err != nil {
		t.Fatal(err)
	}
	defer resolved.Close()
	header := filepath.Join(resolved.Dir, "include", "ghostty.h")
	model, err := InspectHeader(context.Background(), "clang", header, filepath.Dir(header), LinuxTargets, nil)
	if err != nil {
		t.Fatal(err)
	}
	names, err := AssignGoNames(model)
	if err != nil {
		t.Fatal(err)
	}
	again, err := AssignGoNames(model)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, again) {
		t.Fatal("real model name assignment is not deterministic")
	}
	if got, want := names["ghostty_runtime_wakeup_cb"], "RuntimeWakeupCb"; got != want {
		t.Fatalf("real callback name = %q, want %q", got, want)
	}
	for cName, goName := range names {
		if !token.IsIdentifier(goName) || !token.IsExported(goName) {
			t.Errorf("%s mapped to invalid exported Go identifier %q", cName, goName)
		}
		if strings.ContainsAny(goName, ".-/") {
			t.Errorf("%s mapped to unsanitized Go identifier %q", cName, goName)
		}
	}
}
