package generate

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmitTypes(t *testing.T) {
	header := fixtureHeader(t)
	model, err := InspectHeader(context.Background(), "clang", header, filepath.Dir(header), LinuxTargets, nil)
	if err != nil {
		t.Fatal(err)
	}
	layouts := fixtureLayouts(t, header, model)
	out := t.TempDir()
	if err := EmitTypes(model, layouts, Output{Dir: out, Package: "ghostty", Upstream: Upstream{Commit: "0123456789abcdef0123456789abcdef01234567"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, "ghostty", "types_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	code := string(data)
	for _, want := range []string{
		"type AppHandle uintptr",
		"type Mode int32",
		"_ [",
		"type Value struct",
		"_ [0]uint64",
		"func (u *Value) SetCodepoint(value uint32)",
		"func (u Value) Codepoint() uint32",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("types output missing %q:\n%s", want, code)
		}
	}
	if strings.Contains(code, "func ghostty_") || strings.Contains(code, "RegisterFunc") || strings.Contains(code, "Dlopen") {
		t.Fatal("types output contains functions or loader code")
	}

	second := t.TempDir()
	if err := EmitTypes(model, layouts, Output{Dir: second, Package: "ghostty", Upstream: Upstream{Commit: "0123456789abcdef0123456789abcdef01234567"}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"types_gen.go", "constants_gen.go", "abi_gen.go", "abi_gen_test.go", "coverage_gen.json"} {
		firstData, err := os.ReadFile(filepath.Join(out, "ghostty", name))
		if err != nil {
			t.Fatal(err)
		}
		secondData, err := os.ReadFile(filepath.Join(second, "ghostty", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(firstData, secondData) {
			t.Fatalf("%s is not deterministic", name)
		}
	}
}

func TestArchBuildTagsLeadGeneratedFiles(t *testing.T) {
	model := Model{Types: []TypeDecl{{CName: "ghostty_value_s", Kind: TypeStruct}}}
	names := map[string]string{"ghostty_value_s": "Value"}
	layouts := map[string]RecordLayout{"ghostty_value_s": {CName: "ghostty_value_s", Size: 0, Align: 1}}
	output := Output{Package: "ghostty", Upstream: Upstream{Commit: "0123456789abcdef0123456789abcdef01234567"}}

	types, _, err := renderTypeFile(model, names, layouts, map[string]bool{"ghostty_value_s": true}, true, "arm64", output)
	if err != nil {
		t.Fatal(err)
	}
	data, test, err := renderABIFor(&model, names, layouts, output, "abi_gen_arm64.go", "abi_gen_test_arm64.go", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	for name, generated := range map[string][]byte{"types": types, "abi": data, "abi_test": test} {
		text := string(generated)
		tag := strings.Index(text, "//go:build arm64")
		pkg := strings.Index(text, "package ghostty")
		if tag < 0 || pkg < 0 || tag > pkg {
			t.Fatalf("%s build tag position = %d, package position = %d\n%s", name, tag, pkg, text)
		}
	}
}

func TestEmitFunctions(t *testing.T) {
	model := Model{
		Types: []TypeDecl{
			{CName: "ghostty_config_t", Kind: TypeAlias, Type: TypeRef{CName: "void", Pointers: 1}},
			{CName: "ghostty_info_s", Kind: TypeStruct},
		},
		Functions: []FunctionDecl{
			{CName: "ghostty_config_free", Result: TypeRef{CName: "void"}, Parameters: []Parameter{{Type: TypeRef{CName: "ghostty_config_t"}}}},
			{CName: "ghostty_config_new", Result: TypeRef{CName: "ghostty_config_t"}},
			{CName: "ghostty_info", Result: TypeRef{CName: "ghostty_info_s"}},
			{CName: "ghostty_init", Result: TypeRef{CName: "int"}, Parameters: []Parameter{{Type: TypeRef{CName: "uintptr_t"}}, {Type: TypeRef{CName: "char", Pointers: 2}}}},
		},
	}
	layouts := map[string]map[string]RecordLayout{
		"amd64": {"ghostty_info_s": {CName: "ghostty_info_s", Size: 24, Align: 8}},
	}
	out := t.TempDir()
	if err := EmitTypes(model, layouts, Output{Dir: out, Package: "ghostty", Upstream: Upstream{Commit: "0123456789abcdef0123456789abcdef01234567"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, "ghostty", "functions_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	code := string(data)
	for _, want := range []string{
		"var Init func(uintptr, **byte) int32",
		"var Info func() InfoS",
		"var ConfigNew func() ConfigHandle",
		"var ConfigFree func(ConfigHandle)",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("functions output missing %q:\\n%s", want, code)
		}
	}
}

func TestEmitRegistration(t *testing.T) {
	model := Model{Functions: []FunctionDecl{
		{CName: "ghostty_config_new", Result: TypeRef{CName: "void"}},
		{CName: "ghostty_init", Result: TypeRef{CName: "void"}},
	}}
	out := t.TempDir()
	if err := EmitTypes(model, map[string]map[string]RecordLayout{"amd64": {}}, Output{Dir: out, Package: "ghostty", Upstream: Upstream{Commit: "0123456789abcdef0123456789abcdef01234567"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, "ghostty", "register_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	code := string(data)
	firstSymbol := strings.Index(code, `"ghostty_config_new"`)
	secondSymbol := strings.Index(code, `"ghostty_init"`)
	if firstSymbol < 0 || secondSymbol < 0 || firstSymbol > secondSymbol {
		t.Fatalf("symbols are not sorted: %s", code)
	}
	for _, want := range []string{
		"addresses := make(map[string]uintptr, len(allSymbols))",
		"return fmt.Errorf(\"resolve %s: %w\", symbol, err)",
		"purego.RegisterFunc(&ConfigNew, addresses[\"ghostty_config_new\"])",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("registration output missing %q:\\n%s", want, code)
		}
	}
	if strings.Index(code, "purego.RegisterFunc") < strings.Index(code, "for _, symbol := range allSymbols") {
		t.Fatal("registration assigns functions before symbol preflight")
	}
}

func TestEmitConstants(t *testing.T) {
	header := fixtureHeader(t)
	model, err := InspectHeader(context.Background(), "clang", header, filepath.Dir(header), LinuxTargets, nil)
	if err != nil {
		t.Fatal(err)
	}
	layouts := fixtureLayouts(t, header, model)
	out := t.TempDir()
	if err := EmitTypes(model, layouts, Output{Dir: out, Package: "ghostty", Upstream: Upstream{Commit: "0123456789abcdef0123456789abcdef01234567"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, "ghostty", "constants_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "ModeA Mode = 0") || !strings.Contains(string(data), "ModeB Mode = 4") {
		t.Fatalf("constants output =\n%s", data)
	}
}

func TestUnionOverridesAreExactlyEmbeddingInventory(t *testing.T) {
	if got, want := len(embeddingUnionOverrides), 8; got != want {
		t.Fatalf("union override count = %d, want %d", got, want)
	}
	seen := make(map[string]bool, len(embeddingUnionOverrides))
	for _, override := range embeddingUnionOverrides {
		if seen[override.CName] {
			t.Fatalf("duplicate union override %s", override.CName)
		}
		seen[override.CName] = true
	}
	for _, name := range []string{
		"ghostty_input_trigger_key_u", "ghostty_platform_u", "ghostty_quick_terminal_size_value_u", "ghostty_target_u",
		"ghostty_action_key_table_u", "ghostty_action_u", "ghostty_ipc_target_u", "ghostty_ipc_action_u",
	} {
		if !seen[name] {
			t.Errorf("missing union override %s", name)
		}
	}
}

func TestEmitCMapping(t *testing.T) {
	model := Model{
		Types: []TypeDecl{
			{CName: "ghostty_handle_t", Kind: TypeAlias, Type: TypeRef{CName: "void", Pointers: 1}},
			{CName: "ghostty_callback_t", Kind: TypeCallback},
			{CName: "ghostty_record_s", Kind: TypeStruct, Fields: []Field{
				{CName: "values", Type: TypeRef{CName: "uint16_t", ArrayLen: 3}},
				{CName: "callback", Type: TypeRef{CName: "ghostty_callback_t"}},
				{CName: "opaque", Type: TypeRef{CName: "void", Pointers: 1}},
			}},
		},
	}
	layouts := map[string]map[string]RecordLayout{
		"amd64": {"ghostty_record_s": {CName: "ghostty_record_s", Size: 24, Align: 8, Fields: []FieldLayout{{CName: "values", Offset: 0}, {CName: "callback", Offset: 8}, {CName: "opaque", Offset: 16}}}},
	}
	out := t.TempDir()
	if err := EmitTypes(model, layouts, Output{Dir: out, Package: "ghostty", Upstream: Upstream{Commit: "0123456789abcdef0123456789abcdef01234567"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, "ghostty", "types_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	code := string(data)
	for _, want := range []string{"Values", "[3]uint16", "Callback uintptr", "Opaque", "unsafe.Pointer", "[2]byte"} {
		if !strings.Contains(code, want) {
			t.Errorf("mapping output missing %q:\n%s", want, code)
		}
	}
}

func TestCoverage(t *testing.T) {
	header := fixtureHeader(t)
	model, err := InspectHeader(context.Background(), "clang", header, filepath.Dir(header), LinuxTargets, []string{"ghostty_override_only_u"})
	if err != nil {
		t.Fatal(err)
	}
	layouts := fixtureLayouts(t, header, model)
	out := t.TempDir()
	if err := EmitTypes(model, layouts, Output{Dir: out, Package: "ghostty", Upstream: Upstream{Commit: "0123456789abcdef0123456789abcdef01234567"}}); err != nil {
		t.Fatal(err)
	}
	var coverage struct {
		Declarations []struct {
			CName  string `json:"c_name"`
			Kind   string `json:"kind"`
			Status string `json:"status"`
		} `json:"declarations"`
		Exclusions []Exclusion `json:"exclusions"`
	}
	data, err := os.ReadFile(filepath.Join(out, "ghostty", "coverage_gen.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &coverage); err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]string, len(coverage.Declarations))
	for _, declaration := range coverage.Declarations {
		seen[declaration.CName] = declaration.Status
		if declaration.Status != "generated" && declaration.Status != "override" {
			t.Errorf("%s has unexplained status %q", declaration.CName, declaration.Status)
		}
	}
	for _, typ := range model.Types {
		if seen[typ.CName] == "" {
			t.Errorf("coverage missing type %s", typ.CName)
		}
		for _, value := range typ.EnumValues {
			declaration, ok := coverageDeclaration(coverage.Declarations, value.CName)
			if !ok {
				t.Errorf("coverage missing enum value %s", value.CName)
				continue
			}
			if declaration.Kind != "enum_value" {
				t.Errorf("coverage enum value %s kind = %q, want enum_value", value.CName, declaration.Kind)
			}
			if declaration.Status != "generated" && declaration.Status != "override" {
				t.Errorf("coverage enum value %s has unexplained status %q", value.CName, declaration.Status)
			}
		}
	}
	for _, constant := range model.Constants {
		if seen[constant.CName] == "" {
			t.Errorf("coverage missing constant %s", constant.CName)
		}
	}
	if !hasExclusion(Model{Exclusions: coverage.Exclusions}, "ghostty_apple_only", "platform-excluded: __APPLE__") {
		t.Fatalf("coverage exclusions = %#v", coverage.Exclusions)
	}
}

func coverageDeclaration(declarations []struct {
	CName  string `json:"c_name"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}, cName string) (struct {
	CName  string `json:"c_name"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}, bool) {
	for _, declaration := range declarations {
		if declaration.CName == cName {
			return declaration, true
		}
	}
	return struct {
		CName  string `json:"c_name"`
		Kind   string `json:"kind"`
		Status string `json:"status"`
	}{}, false
}

func TestTargetPointerWidthMappings(t *testing.T) {
	for _, target := range LinuxTargets {
		t.Run(target.GOARCH, func(t *testing.T) {
			for _, test := range []struct {
				cName string
				want  string
			}{
				{cName: "uintptr_t", want: "uintptr"},
				{cName: "size_t", want: "uintptr"},
				{cName: "intptr_t", want: "int"},
				{cName: "ssize_t", want: "int"},
			} {
				if got := scalarGoTypeTarget(test.cName, target.GOARCH); got != test.want {
					t.Errorf("%s on %s = %q, want %q", test.cName, target.GOARCH, got, test.want)
				}
				if got, err := cTypeSize(TypeRef{CName: test.cName}, nil, nil, target.GOARCH); err != nil || got != 8 {
					t.Errorf("%s size on %s = %d, %v; want 8", test.cName, target.GOARCH, got, err)
				}
			}
		})
	}
}

func fixtureLayouts(t *testing.T, header string, model Model) map[string]map[string]RecordLayout {
	t.Helper()
	layouts := make(map[string]map[string]RecordLayout, len(LinuxTargets))
	for _, target := range LinuxTargets {
		layout, err := InspectRecordLayouts(context.Background(), "clang", header, filepath.Dir(header), target, model)
		if err != nil {
			t.Fatal(err)
		}
		layouts[target.GOARCH] = layout
	}
	return layouts
}
