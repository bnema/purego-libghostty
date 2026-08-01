package generate

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
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
	for _, name := range []string{"functions_gen.go", "register_gen.go"} {
		if _, err := os.Stat(filepath.Join(out, "ghostty", name)); err != nil {
			t.Fatalf("missing generated %s: %v", name, err)
		}
	}

	second := t.TempDir()
	if err := EmitTypes(model, layouts, Output{Dir: second, Package: "ghostty", Upstream: Upstream{Commit: "0123456789abcdef0123456789abcdef01234567"}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"types_gen.go", "constants_gen.go", "abi_gen.go", "abi_records_gen.go", "abi_gen_test.go", "coverage_gen.json", "functions_gen.go", "register_gen.go"} {
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
	normalABIData, err := os.ReadFile(filepath.Join(out, "ghostty", "abi_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	normalABICode := string(normalABIData)
	if strings.Contains(normalABICode, "type abiRecord struct") || strings.Contains(normalABICode, "func abiRecords() []abiRecord") {
		t.Fatalf("ABI metadata leaked into normal output:\n%s", normalABICode)
	}
	abiData, err := os.ReadFile(filepath.Join(out, "ghostty", "abi_records_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	abiCode := string(abiData)
	for _, want := range []string{
		"//go:build integration",
		"type abiRecord struct",
		"func abiRecords() []abiRecord",
		"name: \"ghostty_fixture_s\"",
		"name: \"enabled\"",
	} {
		if !strings.Contains(abiCode, want) {
			t.Errorf("ABI metadata missing %q:\n%s", want, abiCode)
		}
	}
}

func TestEmitVoidPointerAliasImportsUnsafe(t *testing.T) {
	out := t.TempDir()
	model := Model{Types: []TypeDecl{{CName: "ghostty_double_ptr_t", Kind: TypeAlias, Type: TypeRef{CName: "void", Pointers: 2}}}}
	if err := EmitTypes(model, map[string]map[string]RecordLayout{"amd64": {}}, Output{Dir: out, Package: "ghostty", Upstream: Upstream{Commit: "0123456789abcdef0123456789abcdef01234567"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, "ghostty", "types_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "import \"unsafe\"") {
		t.Fatalf("void pointer alias missing unsafe import:\n%s", data)
	}
}

func TestABIRecordsUseFullModelForSplitArchitectures(t *testing.T) {
	model := Model{Types: []TypeDecl{
		{CName: "ghostty_common_s", Kind: TypeStruct},
		{CName: "ghostty_split_s", Kind: TypeStruct},
	}}
	names := map[string]string{"ghostty_common_s": "CommonS", "ghostty_split_s": "SplitS"}
	layouts := map[string]map[string]RecordLayout{
		"amd64": {
			"ghostty_common_s": {CName: "ghostty_common_s", Size: 4, Align: 4},
			"ghostty_split_s":  {CName: "ghostty_split_s", Size: 8, Align: 8},
		},
		"arm64": {
			"ghostty_common_s": {CName: "ghostty_common_s", Size: 4, Align: 4},
			"ghostty_split_s":  {CName: "ghostty_split_s", Size: 16, Align: 8},
		},
	}
	files, err := renderABI(model, names, layouts, map[string]bool{"ghostty_common_s": false, "ghostty_split_s": true}, Output{Package: "ghostty", Upstream: Upstream{Commit: "0123456789abcdef0123456789abcdef01234567"}})
	if err != nil {
		t.Fatal(err)
	}
	normal := string(files["abi_gen.go"])
	if !strings.Contains(normal, "abiCommon_sSize = uintptr(4)") || strings.Contains(normal, "abiSplit_sSize") {
		t.Fatalf("normal ABI constants =\n%s", normal)
	}
	for arch, size := range map[string]string{"amd64": "8", "arm64": "16"} {
		archCode := string(files["abi_gen_"+arch+".go"])
		if !strings.Contains(archCode, "abiSplit_sSize = uintptr("+size+")") {
			t.Fatalf("%s ABI constants missing split record:\n%s", arch, archCode)
		}
	}
	metadata := string(files["abi_records_gen.go"])
	for _, want := range []string{"//go:build integration", "ghostty_common_s", "ghostty_split_s", "abiSplit_sSize"} {
		if !strings.Contains(metadata, want) {
			t.Errorf("split ABI metadata missing %q", want)
		}
	}
}

func TestEmitFunctions(t *testing.T) {
	model := Model{
		Types: []TypeDecl{
			{CName: "ghostty_config_t", Kind: TypeAlias, Type: TypeRef{CName: "void", Pointers: 1}},
			{CName: "ghostty_info_s", Kind: TypeStruct},
			{CName: "ghostty_callback_t", Kind: TypeCallback},
		},
		Functions: []FunctionDecl{
			{CName: "ghostty_init", Result: TypeRef{CName: "int"}, Parameters: []Parameter{{Type: TypeRef{CName: "uintptr_t"}}, {Type: TypeRef{CName: "char", Pointers: 2}}}},
			{CName: "ghostty_config_set_callback", Result: TypeRef{CName: "void"}, Parameters: []Parameter{{Type: TypeRef{CName: "ghostty_config_t"}}, {Type: TypeRef{CName: "ghostty_callback_t"}}}},
			{CName: "ghostty_info", Result: TypeRef{CName: "ghostty_info_s"}},
			{CName: "ghostty_config_new", Result: TypeRef{CName: "ghostty_config_t"}},
			{CName: "ghostty_config_free", Result: TypeRef{CName: "void"}, Parameters: []Parameter{{Type: TypeRef{CName: "ghostty_config_t"}}}},
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
		"var ConfigSetCallback func(ConfigHandle, uintptr)",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("functions output missing %q:\\n%s", want, code)
		}
	}
	ordered := []string{"var ConfigFree", "var ConfigNew", "var ConfigSetCallback", "var Info", "var Init"}
	for i := 1; i < len(ordered); i++ {
		if strings.Index(code, ordered[i-1]) > strings.Index(code, ordered[i]) {
			t.Fatalf("functions are not sorted: %s", code)
		}
	}
}

func TestEmitRegistration(t *testing.T) {
	model := Model{Functions: []FunctionDecl{
		{CName: "ghostty_init", Result: TypeRef{CName: "void"}},
		{CName: "ghostty_config_new", Result: TypeRef{CName: "void"}},
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
	if !strings.HasPrefix(code, "// Code generated by ghosttygen; DO NOT EDIT.") || !strings.Contains(code, "// Ghostty commit: ") {
		t.Fatalf("registration output is missing generated preflight markers: %s", code)
	}
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
	registerIndex := strings.Index(code, "purego.RegisterFunc")
	preflightIndex := strings.Index(code, "for _, symbol := range allSymbols")
	if registerIndex < 0 || preflightIndex < 0 {
		t.Fatalf("registration preflight markers missing: %s", code)
	}
	if registerIndex < preflightIndex {
		t.Fatal("registration assigns functions before symbol preflight")
	}
}

func TestEmitRejectsInvalidConstantNameAndExpression(t *testing.T) {
	output := Output{Package: "ghostty", Upstream: Upstream{Commit: "0123456789abcdef0123456789abcdef01234567"}}
	for _, test := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "missing", value: "1", want: "missing Go name: GHOSTTY_MISSING"},
		{name: "invalid", value: "1", want: "invalid Go name"},
		{name: "expression", value: "panic()", want: "unsupported constant expression"},
		{name: "syntax", value: "1 +", want: "invalid constant expression"},
	} {
		t.Run(test.name, func(t *testing.T) {
			name := map[string]string{}
			if test.name != "missing" {
				name["GHOSTTY_MISSING"] = "Missing"
			}
			if test.name == "invalid" {
				name["GHOSTTY_MISSING"] = "not-a-name"
			}
			_, err := renderConstants(Model{Constants: []ConstantDecl{{CName: "GHOSTTY_MISSING", Value: test.value}}}, name, output)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("constant error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestUnionOverrideDiagnostics(t *testing.T) {
	old := embeddingUnionOverrides
	embeddingUnionOverrides = append([]UnionOverride(nil), old...)
	t.Cleanup(func() { embeddingUnionOverrides = old })
	embeddingUnionOverrides[0].Fields = []string{"missing", "value"}
	_, err := unionFields(TypeDecl{CName: embeddingUnionOverrides[0].CName, Kind: TypeUnion, Fields: []Field{{CName: "value"}}})
	if err == nil || !strings.Contains(err.Error(), "missing") || !strings.Contains(err.Error(), "available fields: value") {
		t.Fatalf("union missing field error = %v", err)
	}
	embeddingUnionOverrides[0].Fields = []string{"value"}
	_, err = unionFields(TypeDecl{CName: embeddingUnionOverrides[0].CName, Kind: TypeUnion, Fields: []Field{{CName: "value"}, {CName: "extra"}}})
	if err == nil || !strings.Contains(err.Error(), "does not list Clang fields: extra") {
		t.Fatalf("union omitted field error = %v", err)
	}
	embeddingUnionOverrides[0].Fields = []string{"value", "value"}
	_, err = unionFields(TypeDecl{CName: embeddingUnionOverrides[0].CName, Kind: TypeUnion, Fields: []Field{{CName: "value"}}})
	if err == nil || !strings.Contains(err.Error(), "lists field more than once: value") {
		t.Fatalf("union duplicate field error = %v", err)
	}
}

func TestEmitRejectsUnknownFunctionType(t *testing.T) {
	model := Model{Functions: []FunctionDecl{{CName: "ghostty_unknown", Result: TypeRef{CName: "mystery_t"}}}}
	err := EmitTypes(model, map[string]map[string]RecordLayout{"amd64": {}}, Output{Dir: t.TempDir(), Package: "ghostty", Upstream: Upstream{Commit: "0123456789abcdef0123456789abcdef01234567"}})
	if err == nil || !strings.Contains(err.Error(), "unsupported C type") {
		t.Fatalf("unknown function type error = %v", err)
	}
}

func TestEmitRejectsUnsupportedArchitecture(t *testing.T) {
	model := Model{Types: []TypeDecl{{CName: "ghostty_record_s", Kind: TypeStruct, Fields: []Field{{CName: "value", Type: TypeRef{CName: "size_t"}}}}}}
	err := EmitTypes(model, map[string]map[string]RecordLayout{"386": {"ghostty_record_s": {CName: "ghostty_record_s", Size: 4, Align: 4, Fields: []FieldLayout{{CName: "value"}}}}}, Output{Dir: t.TempDir(), Package: "ghostty", Upstream: Upstream{Commit: "0123456789abcdef0123456789abcdef01234567"}})
	if err == nil || !strings.Contains(err.Error(), "unsupported GOARCH") {
		t.Fatalf("unsupported architecture error = %v", err)
	}
	if _, err := cTypeSize(TypeRef{CName: "size_t"}, nil, nil, "386"); err == nil || !strings.Contains(err.Error(), "unsupported GOARCH") {
		t.Fatalf("unsupported scalar architecture error = %v", err)
	}
}

func TestEmitArchitectureFilesAndStaleCleanup(t *testing.T) {
	model := Model{Types: []TypeDecl{{CName: "ghostty_record_s", Kind: TypeStruct, Fields: []Field{{CName: "value", Type: TypeRef{CName: "uint32_t"}}}}}}
	layouts := map[string]map[string]RecordLayout{
		"amd64": {"ghostty_record_s": {CName: "ghostty_record_s", Size: 4, Align: 4, Fields: []FieldLayout{{CName: "value", Offset: 0}}}},
		"arm64": {"ghostty_record_s": {CName: "ghostty_record_s", Size: 8, Align: 8, Fields: []FieldLayout{{CName: "value", Offset: 0}}}},
	}
	out := t.TempDir()
	packageDir := filepath.Join(out, "ghostty")
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "abi_gen_test_amd64.go"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "keep_gen.go"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EmitTypes(model, layouts, Output{Dir: out, Package: "ghostty", Upstream: Upstream{Commit: "0123456789abcdef0123456789abcdef01234567"}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"types_gen_amd64.go", "types_gen_arm64.go", "abi_gen_amd64_test.go", "abi_gen_arm64_test.go"} {
		data, err := os.ReadFile(filepath.Join(packageDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(data), "//go:build ") || !strings.Contains(string(data), "\n\n// Code generated by ghosttygen; DO NOT EDIT.") {
			t.Errorf("%s does not put build tag before generated marker:\n%s", name, data)
		}
	}
	if _, err := os.Stat(filepath.Join(packageDir, "abi_gen_test_amd64.go")); !os.IsNotExist(err) {
		t.Fatalf("stale architecture test still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(packageDir, "keep_gen.go")); err != nil {
		t.Fatalf("unrelated generated-looking file was removed: %v", err)
	}
}

func TestGeneratedFixturePackageCompiles(t *testing.T) {
	header := fixtureHeader(t)
	model, err := InspectHeader(context.Background(), "clang", header, filepath.Dir(header), LinuxTargets, nil)
	if err != nil {
		t.Fatal(err)
	}
	layouts := fixtureLayouts(t, header, model)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	moduleRoot := filepath.Clean(filepath.Join(root, "../.."))
	out, err := os.MkdirTemp(moduleRoot, ".ghosttygen-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(out) })
	if err := EmitTypes(model, layouts, Output{Dir: out, Package: "ghostty", Upstream: Upstream{Commit: "0123456789abcdef0123456789abcdef01234567"}}); err != nil {
		t.Fatal(err)
	}
	compileGeneratedPackage(t, moduleRoot, filepath.Join(out, "ghostty"))
}

func TestEmptyGeneratedPackageCompiles(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	moduleRoot := filepath.Clean(filepath.Join(root, "../.."))
	out, err := os.MkdirTemp(moduleRoot, ".ghosttygen-empty-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(out) })
	if err := EmitTypes(Model{}, map[string]map[string]RecordLayout{"amd64": {}}, Output{Dir: out, Package: "ghostty", Upstream: Upstream{Commit: "0123456789abcdef0123456789abcdef01234567"}}); err != nil {
		t.Fatal(err)
	}
	compileGeneratedPackage(t, moduleRoot, filepath.Join(out, "ghostty"))
}

func compileGeneratedPackage(t *testing.T, moduleRoot, packageDir string) {
	t.Helper()
	rel, err := filepath.Rel(moduleRoot, packageDir)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "test", "./"+filepath.ToSlash(rel), "-run", "^$")
	command.Dir = moduleRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compile generated package: %v\n%s", err, output)
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
	for _, function := range model.Functions {
		declaration, ok := coverageDeclaration(coverage.Declarations, function.CName)
		if !ok || declaration.Kind != "function" || declaration.Status != "generated" {
			t.Errorf("coverage missing function %s: %#v", function.CName, declaration)
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
				if got, err := scalarGoTypeTarget(test.cName, target.GOARCH); err != nil || got != test.want {
					t.Errorf("%s on %s = %q, %v; want %q", test.cName, target.GOARCH, got, err, test.want)
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
