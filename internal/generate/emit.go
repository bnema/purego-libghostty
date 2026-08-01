package generate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Output struct {
	Dir      string
	Package  string
	Upstream Upstream
}

// EmitTypes writes the generated type, constant, ABI, and coverage files.
// Function variables and loaders are intentionally emitted by the next phase.
func EmitTypes(model Model, layouts map[string]map[string]RecordLayout, output Output) error {
	if output.Dir == "" {
		return fmt.Errorf("output directory is empty")
	}
	if output.Package == "" {
		return fmt.Errorf("output package is empty")
	}
	if len(layouts) == 0 {
		return fmt.Errorf("no target layouts")
	}
	names, err := AssignGoNames(model)
	if err != nil {
		return err
	}
	targets := sortedArchitectures(layouts)
	for _, arch := range targets {
		for _, typ := range model.Types {
			if typ.Kind != TypeStruct && typ.Kind != TypeUnion {
				continue
			}
			if _, ok := layouts[arch][typ.CName]; !ok {
				return fmt.Errorf("missing record layout: %s (%s)", typ.CName, arch)
			}
		}
	}

	split := make(map[string]bool)
	for _, typ := range model.Types {
		if typ.Kind != TypeStruct && typ.Kind != TypeUnion {
			continue
		}
		split[typ.CName], err = RequiresArchSplit(layouts, typ.CName)
		if err != nil {
			return err
		}
	}

	files := make(map[string][]byte)
	commonTypes, _, err := renderTypeFile(model, names, layouts[targets[0]], split, false, "", output)
	if err != nil {
		return err
	}
	files["types_gen.go"] = commonTypes
	for _, arch := range targets {
		if !hasSplitForArch(model, split) {
			break
		}
		body, _, err := renderTypeFile(model, names, layouts[arch], split, true, arch, output)
		if err != nil {
			return err
		}
		if body != nil && len(body) > 0 {
			files["types_gen_"+arch+".go"] = body
		}
	}

	constants, err := renderConstants(model, names, output)
	if err != nil {
		return err
	}
	files["constants_gen.go"] = constants

	abiFiles, err := renderABI(model, names, layouts, split, output)
	if err != nil {
		return err
	}
	for name, data := range abiFiles {
		files[name] = data
	}

	coverage, err := renderCoverage(model, output)
	if err != nil {
		return err
	}
	files["coverage_gen.json"] = coverage
	return writeGeneratedFiles(filepath.Join(output.Dir, output.Package), files)
}

func sortedArchitectures(layouts map[string]map[string]RecordLayout) []string {
	arches := make([]string, 0, len(layouts))
	for arch := range layouts {
		arches = append(arches, arch)
	}
	sort.Strings(arches)
	return arches
}

func hasSplitForArch(model Model, split map[string]bool) bool {
	for _, typ := range model.Types {
		if (typ.Kind == TypeStruct || typ.Kind == TypeUnion) && split[typ.CName] {
			return true
		}
	}
	return false
}

func renderTypeFile(model Model, names map[string]string, layouts map[string]RecordLayout, split map[string]bool, archFile bool, arch string, output Output) ([]byte, bool, error) {
	var body strings.Builder
	needsUnsafe := false
	for _, typ := range model.Types {
		if typ.Kind == TypeStruct || typ.Kind == TypeUnion {
			if split[typ.CName] != archFile {
				continue
			}
			layout := layouts[typ.CName]
			definition, unsafeNeeded, err := renderRecord(typ, layout, names, model, layouts, arch)
			if err != nil {
				return nil, false, err
			}
			body.WriteString(definition)
			needsUnsafe = needsUnsafe || unsafeNeeded
			continue
		}
		if archFile {
			continue
		}
		definition, err := renderNamedType(typ, names, output)
		if err != nil {
			return nil, false, err
		}
		body.WriteString(definition)
	}
	if body.Len() == 0 && !archFile {
		body.WriteString("// no declarations\n")
	}
	if body.Len() == 0 {
		return nil, needsUnsafe, nil
	}
	return goFile(output, "types_gen.go", body.String(), needsUnsafe, arch), needsUnsafe, nil
}

func renderNamedType(typ TypeDecl, names map[string]string, output Output) (string, error) {
	name := names[typ.CName]
	if name == "" {
		return "", fmt.Errorf("missing Go name: %s", typ.CName)
	}
	var out strings.Builder
	switch typ.Kind {
	case TypeAlias:
		if typ.Type.CName == "void" && typ.Type.Pointers == 1 {
			out.WriteString("type " + name + " uintptr\n\n")
		} else {
			out.WriteString("type " + name + " " + goTypeRef(typ.Type, names, typeKinds(nil), false) + "\n\n")
		}
	case TypeEnum:
		out.WriteString("type " + name + " int32\n\n")
	case TypeCallback:
		out.WriteString("type " + name + " uintptr\n\n")
	default:
		return "", fmt.Errorf("unsupported named type kind %q: %s", typ.Kind, typ.CName)
	}
	return out.String(), nil
}

func renderRecord(typ TypeDecl, layout RecordLayout, names map[string]string, model Model, layouts map[string]RecordLayout, arch string) (string, bool, error) {
	name := names[typ.CName]
	if name == "" {
		return "", false, fmt.Errorf("missing Go name: %s", typ.CName)
	}
	if typ.Kind == TypeUnion {
		storage, err := UnionStorageFor(layout)
		if err != nil {
			return "", false, err
		}
		var out strings.Builder
		out.WriteString("type " + name + " struct {\n")
		out.WriteString("\t_ [0]" + storage.AlignmentGoType + "\n")
		out.WriteString("\t_ [" + strconv.Itoa(storage.Size) + "]byte\n")
		out.WriteString("}\n\n")
		fields, err := unionFields(typ)
		if err != nil {
			return "", false, err
		}
		for _, field := range fields {
			fieldName := goValueIdentifier(field.CName)
			fieldType := goTypeRefTarget(field.Type, names, typeKinds(model.Types), true, arch)
			out.WriteString("func (u *" + name + ") Set" + fieldName + "(value " + fieldType + ") {\n")
			out.WriteString("\t*(*" + fieldType + ")(unsafe.Pointer(u)) = value\n")
			out.WriteString("}\n\n")
			out.WriteString("func (u " + name + ") " + fieldName + "() " + fieldType + " {\n")
			out.WriteString("\treturn *(*" + fieldType + ")(unsafe.Pointer(&u))\n")
			out.WriteString("}\n\n")
		}
		return out.String(), true, nil
	}

	var out strings.Builder
	out.WriteString("type " + name + " struct {\n")
	cursor := 0
	needsUnsafe := false
	for _, field := range typ.Fields {
		fieldLayout, ok := findFieldLayout(layout, field.CName)
		if !ok {
			return "", false, fmt.Errorf("missing field layout: %s.%s", typ.CName, field.CName)
		}
		if fieldLayout.Offset < cursor {
			return "", false, fmt.Errorf("overlapping field layout: %s.%s", typ.CName, field.CName)
		}
		if gap := fieldLayout.Offset - cursor; gap > 0 {
			out.WriteString("\t_ [" + strconv.Itoa(gap) + "]byte\n")
			cursor += gap
		}
		fieldType := goTypeRefTarget(field.Type, names, typeKinds(model.Types), true, arch)
		out.WriteString("\t" + goValueIdentifier(field.CName) + " " + fieldType + "\n")
		fieldSize, err := cTypeSize(field.Type, layouts, model.Types, arch)
		if err != nil {
			return "", false, fmt.Errorf("%s.%s: %w", typ.CName, field.CName, err)
		}
		cursor = fieldLayout.Offset + fieldSize
		needsUnsafe = needsUnsafe || strings.Contains(fieldType, "unsafe.")
	}
	if gap := layout.Size - cursor; gap > 0 {
		out.WriteString("\t_ [" + strconv.Itoa(gap) + "]byte\n")
		cursor += gap
	}
	if cursor != layout.Size {
		return "", false, fmt.Errorf("generated field size for %s = %d, want %d", typ.CName, cursor, layout.Size)
	}
	out.WriteString("}\n\n")
	return out.String(), needsUnsafe, nil
}

func unionFields(typ TypeDecl) ([]Field, error) {
	override, ok := unionOverride(typ.CName)
	if !ok {
		return typ.Fields, nil
	}
	byName := make(map[string]Field, len(typ.Fields))
	for _, field := range typ.Fields {
		byName[field.CName] = field
	}
	fields := make([]Field, 0, len(override.Fields))
	for _, name := range override.Fields {
		field, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("union override field missing from Clang model: %s.%s", typ.CName, name)
		}
		fields = append(fields, field)
		delete(byName, name)
	}
	if len(byName) != 0 {
		return nil, fmt.Errorf("union override missing Clang field: %s", typ.CName)
	}
	return fields, nil
}

func findFieldLayout(layout RecordLayout, name string) (FieldLayout, bool) {
	for _, field := range layout.Fields {
		if field.CName == name {
			return field, true
		}
	}
	return FieldLayout{}, false
}

func typeKinds(types []TypeDecl) map[string]TypeKind {
	kinds := make(map[string]TypeKind, len(types))
	for _, typ := range types {
		kinds[typ.CName] = typ.Kind
	}
	return kinds
}

func goTypeRef(ref TypeRef, names map[string]string, kinds map[string]TypeKind, callbackAsUintptr bool) string {
	return goTypeRefTarget(ref, names, kinds, callbackAsUintptr, "")
}

func goTypeRefTarget(ref TypeRef, names map[string]string, kinds map[string]TypeKind, callbackAsUintptr bool, arch string) string {
	base := ref
	base.ArrayLen = 0
	name := names[base.CName]
	if callbackAsUintptr && kinds[base.CName] == TypeCallback && ref.Pointers == 0 {
		name = "uintptr"
	}
	if name == "" {
		name = scalarGoTypeTarget(base.CName, arch)
	}
	if ref.Pointers > 0 && ref.CName == "void" {
		if ref.Pointers == 1 {
			name = "unsafe.Pointer"
		} else {
			name = strings.Repeat("*", ref.Pointers-1) + "unsafe.Pointer"
		}
	} else if ref.Pointers > 0 {
		name = strings.Repeat("*", ref.Pointers) + name
	}
	if ref.ArrayLen > 0 {
		name = "[" + strconv.Itoa(ref.ArrayLen) + "]" + name
	}
	return name
}

func scalarGoType(cName string) string {
	return scalarGoTypeTarget(cName, "")
}

func scalarGoTypeTarget(cName, arch string) string {
	switch cName {
	case "_Bool", "bool":
		return "bool"
	case "char", "unsigned char", "uint8_t":
		return "byte"
	case "int8_t", "signed char":
		return "int8"
	case "uint16_t", "unsigned short":
		return "uint16"
	case "int16_t", "short":
		return "int16"
	case "uint32_t", "unsigned", "unsigned int":
		return "uint32"
	case "int32_t", "int", "signed", "signed int":
		return "int32"
	case "uint64_t", "unsigned long", "unsigned long long":
		return "uint64"
	case "int64_t", "long", "long long", "signed long", "signed long long":
		return "int64"
	case "uintptr_t", "size_t":
		return "uintptr"
	case "intptr_t", "ssize_t":
		return "int"
	case "float":
		return "float32"
	case "double":
		return "float64"
	case "void":
		return "byte"
	default:
		return "uintptr"
	}
}

func cTypeSize(ref TypeRef, layouts map[string]RecordLayout, types []TypeDecl, arch string) (int, error) {
	if ref.ArrayLen > 0 {
		element := ref
		element.ArrayLen = 0
		size, err := cTypeSize(element, layouts, types, arch)
		if err != nil {
			return 0, err
		}
		return size * ref.ArrayLen, nil
	}
	if ref.Pointers > 0 {
		return 8, nil
	}
	for _, typ := range types {
		if typ.CName != ref.CName {
			continue
		}
		switch typ.Kind {
		case TypeStruct, TypeUnion:
			layout, ok := layouts[ref.CName]
			if !ok {
				return 0, fmt.Errorf("missing nested record layout: %s", ref.CName)
			}
			return layout.Size, nil
		case TypeEnum:
			return 4, nil
		case TypeCallback:
			return 8, nil
		case TypeAlias:
			return cTypeSize(typ.Type, layouts, types, arch)
		}
	}
	switch ref.CName {
	case "_Bool", "bool", "char", "int8_t", "uint8_t", "signed char", "unsigned char":
		return 1, nil
	case "int16_t", "uint16_t", "short", "unsigned short":
		return 2, nil
	case "int", "unsigned", "int32_t", "uint32_t", "signed", "signed int", "unsigned int", "float":
		return 4, nil
	case "int64_t", "uint64_t", "long", "unsigned long", "long long", "unsigned long long", "double":
		return 8, nil
	case "size_t", "uintptr_t", "intptr_t", "ssize_t":
		return 8, nil
	default:
		return 0, fmt.Errorf("unsupported C type %q", ref.CName)
	}
}

func renderConstants(model Model, names map[string]string, output Output) ([]byte, error) {
	var body strings.Builder
	for _, typ := range model.Types {
		if typ.Kind != TypeEnum || len(typ.EnumValues) == 0 {
			continue
		}
		body.WriteString("const (\n")
		for _, value := range typ.EnumValues {
			body.WriteString("\t" + names[value.CName] + " " + names[typ.CName] + " = " + strconv.FormatInt(value.Value, 10) + "\n")
		}
		body.WriteString(")\n\n")
	}
	for _, constant := range model.Constants {
		body.WriteString("const " + names[constant.CName] + " = " + constant.Value + "\n\n")
	}
	if body.Len() == 0 {
		body.WriteString("// no constants\n")
	}
	return goFile(output, "constants_gen.go", body.String(), false, ""), nil
}

func renderABI(model Model, names map[string]string, layouts map[string]map[string]RecordLayout, split map[string]bool, output Output) (map[string][]byte, error) {
	files := make(map[string][]byte)
	arches := sortedArchitectures(layouts)
	if !hasSplitForArch(model, split) {
		data, test, err := renderABIFor(&model, names, layouts[arches[0]], output, "abi_gen.go", "abi_gen_test.go", "")
		if err != nil {
			return nil, err
		}
		files["abi_gen.go"] = data
		files["abi_gen_test.go"] = test
		return files, nil
	}
	commonModel := modelWithoutSplit(model, split)
	data, test, err := renderABIFor(&commonModel, names, layouts[arches[0]], output, "abi_gen.go", "abi_gen_test.go", "")
	if err != nil {
		return nil, err
	}
	files["abi_gen.go"] = data
	files["abi_gen_test.go"] = test
	for _, arch := range arches {
		archModel := modelOnlySplit(model, split)
		data, test, err := renderABIFor(&archModel, names, layouts[arch], output, "abi_gen_"+arch+".go", "abi_gen_test_"+arch+".go", arch)
		if err != nil {
			return nil, err
		}
		files["abi_gen_"+arch+".go"] = data
		files["abi_gen_test_"+arch+".go"] = test
	}
	return files, nil
}

func modelWithoutSplit(model Model, split map[string]bool) Model {
	copy := model
	copy.Types = nil
	for _, typ := range model.Types {
		if !split[typ.CName] || (typ.Kind != TypeStruct && typ.Kind != TypeUnion) {
			copy.Types = append(copy.Types, typ)
		}
	}
	return copy
}

func modelOnlySplit(model Model, split map[string]bool) Model {
	copy := model
	copy.Types = nil
	for _, typ := range model.Types {
		if split[typ.CName] && (typ.Kind == TypeStruct || typ.Kind == TypeUnion) {
			copy.Types = append(copy.Types, typ)
		}
	}
	return copy
}

func renderABIFor(model *Model, names map[string]string, layouts map[string]RecordLayout, output Output, goName, testName, arch string) ([]byte, []byte, error) {
	var body, tests strings.Builder
	for _, typ := range model.Types {
		if typ.Kind != TypeStruct && typ.Kind != TypeUnion {
			continue
		}
		layout, ok := layouts[typ.CName]
		if !ok {
			return nil, nil, fmt.Errorf("missing ABI layout: %s", typ.CName)
		}
		prefix := abiIdentifier(typ.CName)
		body.WriteString("const " + prefix + "Size = uintptr(" + strconv.Itoa(layout.Size) + ")\n")
		body.WriteString("const " + prefix + "Align = uintptr(" + strconv.Itoa(layout.Align) + ")\n")
		for _, field := range layout.Fields {
			body.WriteString("const " + prefix + "_" + abiIdentifier(field.CName) + "Offset = uintptr(" + strconv.Itoa(field.Offset) + ")\n")
		}
		body.WriteString("\n")
	}
	if body.Len() == 0 {
		body.WriteString("// no record ABI metadata\n")
	}
	testFunc := "TestGeneratedABI"
	if arch != "" {
		testFunc += upperFirst(arch)
	}
	tests.WriteString("func " + testFunc + "(t *testing.T) {\n")
	for _, typ := range model.Types {
		if typ.Kind != TypeStruct && typ.Kind != TypeUnion {
			continue
		}
		layout := layouts[typ.CName]
		name := names[typ.CName]
		prefix := abiIdentifier(typ.CName)
		tests.WriteString("\tif got, want := unsafe.Sizeof(" + name + "{}), " + prefix + "Size; got != want { t.Errorf(\"" + typ.CName + " size = %d, want %d\", got, want) }\n")
		tests.WriteString("\tif got, want := unsafe.Alignof(" + name + "{}), " + prefix + "Align; got != want { t.Errorf(\"" + typ.CName + " align = %d, want %d\", got, want) }\n")
		if typ.Kind == TypeStruct {
			for _, field := range typ.Fields {
				_, ok := findFieldLayout(layout, field.CName)
				if !ok {
					return nil, nil, fmt.Errorf("missing ABI field layout: %s.%s", typ.CName, field.CName)
				}
				tests.WriteString("\tif got, want := unsafe.Offsetof(" + name + "{}." + goValueIdentifier(field.CName) + "), " + prefix + "_" + abiIdentifier(field.CName) + "Offset; got != want { t.Errorf(\"" + typ.CName + "." + field.CName + " offset = %d, want %d\", got, want) }\n")
			}
		}
	}
	tests.WriteString("}\n")
	return goFile(output, goName, body.String(), false, arch), goTestFile(output, testName, tests.String(), arch), nil
}

func abiIdentifier(name string) string {
	name = strings.ReplaceAll(name, ".", "_")
	name = strings.TrimPrefix(name, "ghostty_")
	name = strings.NewReplacer("-", "_", ".", "_").Replace(name)
	return "abi" + upperFirst(name)
}

func upperFirst(value string) string {
	if value == "" {
		return value
	}
	runeValue, size := utf8.DecodeRuneInString(value)
	return string(unicode.ToUpper(runeValue)) + value[size:]
}

func renderCoverage(model Model, output Output) ([]byte, error) {
	type declaration struct {
		CName  string `json:"c_name"`
		Kind   string `json:"kind"`
		Status string `json:"status"`
		Reason string `json:"reason,omitempty"`
	}
	coverage := struct {
		Upstream     string        `json:"upstream"`
		Declarations []declaration `json:"declarations"`
		Exclusions   []Exclusion   `json:"exclusions"`
	}{Upstream: output.Upstream.Commit}
	for _, typ := range model.Types {
		status, reason := "generated", ""
		if _, ok := unionOverride(typ.CName); ok && typ.Kind == TypeUnion {
			status, reason = "override", "embedding union accessors"
		}
		coverage.Declarations = append(coverage.Declarations, declaration{CName: typ.CName, Kind: "type", Status: status, Reason: reason})
		for _, value := range typ.EnumValues {
			coverage.Declarations = append(coverage.Declarations, declaration{CName: value.CName, Kind: "enum_value", Status: "generated"})
		}
	}
	for _, constant := range model.Constants {
		coverage.Declarations = append(coverage.Declarations, declaration{CName: constant.CName, Kind: "constant", Status: "generated"})
	}
	for _, function := range model.Functions {
		coverage.Declarations = append(coverage.Declarations, declaration{CName: function.CName, Kind: "function", Status: "generated"})
	}
	for _, exclusion := range model.Exclusions {
		coverage.Exclusions = append(coverage.Exclusions, exclusion)
	}
	sort.Slice(coverage.Declarations, func(i, j int) bool { return coverage.Declarations[i].CName < coverage.Declarations[j].CName })
	sort.Slice(coverage.Exclusions, func(i, j int) bool { return coverage.Exclusions[i].CName < coverage.Exclusions[j].CName })
	data, err := json.MarshalIndent(coverage, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func goFile(output Output, name, body string, needsUnsafe bool, build string) []byte {
	var source bytes.Buffer
	source.WriteString("// Code generated by ghosttygen; DO NOT EDIT.\n")
	source.WriteString("// Ghostty commit: " + output.Upstream.Commit + "\n\n")
	if build != "" {
		source.WriteString("//go:build " + build + "\n\n")
	}
	source.WriteString("package " + output.Package + "\n\n")
	if needsUnsafe {
		source.WriteString("import \"unsafe\"\n\n")
	}
	source.WriteString(body)
	return source.Bytes()
}

func goTestFile(output Output, name, body, build string) []byte {
	var source bytes.Buffer
	source.WriteString("// Code generated by ghosttygen; DO NOT EDIT.\n")
	source.WriteString("// Ghostty commit: " + output.Upstream.Commit + "\n\n")
	if build != "" {
		source.WriteString("//go:build " + build + "\n\n")
	}
	source.WriteString("package " + output.Package + "\n\nimport (\n\t\"testing\"\n\t\"unsafe\"\n)\n\n")
	source.WriteString(body)
	return source.Bytes()
}

func writeGeneratedFiles(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(filepath.Dir(dir), ".ghosttygen-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		data := files[name]
		if strings.HasSuffix(name, ".go") {
			formatted, err := format.Source(data)
			if err != nil {
				return fmt.Errorf("format %s: %w", name, err)
			}
			data = formatted
		}
		if err := os.WriteFile(filepath.Join(temp, name), data, 0o644); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, stale := range []string{
		"types_gen.go", "types_gen_amd64.go", "types_gen_arm64.go",
		"constants_gen.go", "abi_gen.go", "abi_gen_amd64.go", "abi_gen_arm64.go",
		"abi_gen_test.go", "abi_gen_test_amd64.go", "abi_gen_test_arm64.go", "coverage_gen.json",
	} {
		if _, ok := files[stale]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(dir, stale)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	for _, name := range names {
		if err := os.Rename(filepath.Join(temp, name), filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}
