package generate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
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

// EmitTypes writes the generated type, constant, ABI, coverage, function, and registration files.
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
		if err := validateTargetGOARCH(arch); err != nil {
			return err
		}
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
	commonTypes, _, err := renderTypeFile(model, names, layouts[targets[0]], split, false, targets[0], output)
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
	functions, err := renderFunctions(model, names, output)
	if err != nil {
		return err
	}
	files["functions_gen.go"] = functions
	registration, err := renderRegistration(model, names, output)
	if err != nil {
		return err
	}
	files["register_gen.go"] = registration
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
		needsUnsafe = needsUnsafe || strings.Contains(definition, "unsafe.")
	}
	if body.Len() == 0 && !archFile {
		body.WriteString("// no declarations\n")
	}
	if body.Len() == 0 {
		return nil, needsUnsafe, nil
	}
	return goFile(output, "types_gen.go", body.String(), needsUnsafe, archIf(archFile, arch)), needsUnsafe, nil
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
			goType, err := goTypeRef(typ.Type, names, typeKinds(nil), false)
			if err != nil {
				return "", fmt.Errorf("%s: %w", typ.CName, err)
			}
			out.WriteString("type " + name + " " + goType + "\n\n")
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
			fieldType, err := goTypeRefTarget(field.Type, names, typeKinds(model.Types), true, arch)
			if err != nil {
				return "", false, fmt.Errorf("%s.%s: %w", typ.CName, field.CName, err)
			}
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
		fieldType, err := goTypeRefTarget(field.Type, names, typeKinds(model.Types), true, arch)
		if err != nil {
			return "", false, fmt.Errorf("%s.%s: %w", typ.CName, field.CName, err)
		}
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
		if _, duplicate := byName[field.CName]; duplicate {
			return nil, fmt.Errorf("union has duplicate Clang field: %s.%s", typ.CName, field.CName)
		}
		byName[field.CName] = field
	}
	fields := make([]Field, 0, len(override.Fields))
	seen := make(map[string]bool, len(override.Fields))
	for _, name := range override.Fields {
		if seen[name] {
			return nil, fmt.Errorf("union override for %s lists field more than once: %s", typ.CName, name)
		}
		seen[name] = true
		field, ok := byName[name]
		if !ok {
			available := make([]string, 0, len(byName))
			for candidate := range byName {
				available = append(available, candidate)
			}
			sort.Strings(available)
			return nil, fmt.Errorf("union override field missing from Clang model: %s.%s (available fields: %s)", typ.CName, name, strings.Join(available, ", "))
		}
		fields = append(fields, field)
		delete(byName, name)
	}
	if len(byName) != 0 {
		missing := make([]string, 0, len(byName))
		for name := range byName {
			missing = append(missing, name)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("union override for %s does not list Clang fields: %s", typ.CName, strings.Join(missing, ", "))
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

func goTypeRef(ref TypeRef, names map[string]string, kinds map[string]TypeKind, callbackAsUintptr bool) (string, error) {
	return goTypeRefTarget(ref, names, kinds, callbackAsUintptr, "")
}

func goTypeRefTarget(ref TypeRef, names map[string]string, kinds map[string]TypeKind, callbackAsUintptr bool, arch string) (string, error) {
	if err := validateGOARCH(arch); err != nil {
		return "", err
	}
	if ref.CName == "" {
		return "", fmt.Errorf("missing C type name")
	}
	base := ref
	base.ArrayLen = 0
	name := names[base.CName]
	if kinds[base.CName] != TypeCallback && name == "" {
		var err error
		name, err = scalarGoTypeTarget(base.CName, arch)
		if err != nil {
			return "", err
		}
	}
	if kinds[base.CName] == TypeCallback {
		if names[base.CName] == "" {
			return "", fmt.Errorf("missing Go name: %s", base.CName)
		}
		if callbackAsUintptr && ref.Pointers == 0 {
			name = "uintptr"
		}
	}
	if name == "" {
		return "", fmt.Errorf("missing Go name: %s", base.CName)
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
	return name, nil
}

func scalarGoType(cName string) string {
	name, _ := scalarGoTypeTarget(cName, "")
	return name
}

func scalarGoTypeTarget(cName, arch string) (string, error) {
	if err := validateGOARCH(arch); err != nil {
		return "", err
	}
	switch cName {
	case "_Bool", "bool":
		return "bool", nil
	case "char", "unsigned char", "uint8_t":
		return "byte", nil
	case "int8_t", "signed char":
		return "int8", nil
	case "uint16_t", "unsigned short":
		return "uint16", nil
	case "int16_t", "short":
		return "int16", nil
	case "uint32_t", "unsigned", "unsigned int":
		return "uint32", nil
	case "int32_t", "int", "signed", "signed int":
		return "int32", nil
	case "uint64_t", "unsigned long", "unsigned long long":
		return "uint64", nil
	case "int64_t", "long", "long long", "signed long", "signed long long":
		return "int64", nil
	case "uintptr_t", "size_t":
		return "uintptr", nil
	case "intptr_t", "ssize_t":
		return "int", nil
	case "float":
		return "float32", nil
	case "double":
		return "float64", nil
	case "void":
		return "byte", nil
	default:
		return "", fmt.Errorf("unsupported C type %q", cName)
	}
}

func validateGOARCH(arch string) error {
	if arch == "" {
		return nil
	}
	if arch != "amd64" && arch != "arm64" {
		return fmt.Errorf("unsupported GOARCH %q (supported: amd64, arm64)", arch)
	}
	return nil
}

func validateTargetGOARCH(arch string) error {
	if arch == "" {
		return fmt.Errorf("unsupported GOARCH %q (supported: amd64, arm64)", arch)
	}
	return validateGOARCH(arch)
}

func pointerSize(arch string) (int, error) {
	if err := validateGOARCH(arch); err != nil {
		return 0, err
	}
	if arch == "" {
		return 0, fmt.Errorf("GOARCH is required for pointer-sized C type")
	}
	return 8, nil
}

func archIf(enabled bool, arch string) string {
	if enabled {
		return arch
	}
	return ""
}

func cTypeSize(ref TypeRef, layouts map[string]RecordLayout, types []TypeDecl, arch string) (int, error) {
	if err := validateGOARCH(arch); err != nil {
		return 0, err
	}
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
		return pointerSize(arch)
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
			return pointerSize(arch)
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
		return pointerSize(arch)
	default:
		return 0, fmt.Errorf("unsupported C type %q", ref.CName)
	}
}

func renderFunctions(model Model, names map[string]string, output Output) ([]byte, error) {
	functions := append([]FunctionDecl(nil), model.Functions...)
	sort.Slice(functions, func(i, j int) bool { return functions[i].CName < functions[j].CName })
	var body strings.Builder
	needsUnsafe := false
	for _, function := range functions {
		name := names[function.CName]
		if name == "" {
			return nil, fmt.Errorf("missing Go name: %s", function.CName)
		}
		signature, err := functionSignature(function, names, typeKinds(model.Types))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", function.CName, err)
		}
		body.WriteString("var " + name + " " + signature + "\n\n")
		needsUnsafe = needsUnsafe || strings.Contains(signature, "unsafe.")
	}
	if body.Len() == 0 {
		body.WriteString("// no functions\n")
	}
	return goFile(output, "functions_gen.go", body.String(), needsUnsafe, ""), nil
}

func functionSignature(function FunctionDecl, names map[string]string, kinds map[string]TypeKind) (string, error) {
	var out strings.Builder
	out.WriteString("func(")
	for i, parameter := range function.Parameters {
		if i > 0 {
			out.WriteString(", ")
		}
		parameterType, err := goTypeRefTarget(parameter.Type, names, kinds, true, "")
		if err != nil {
			return "", fmt.Errorf("parameter %q: %w", parameter.CName, err)
		}
		out.WriteString(parameterType)
	}
	out.WriteString(")")
	if function.Result.CName != "void" || function.Result.Pointers > 0 || function.Result.ArrayLen > 0 {
		out.WriteByte(' ')
		resultType, err := goTypeRefTarget(function.Result, names, kinds, true, "")
		if err != nil {
			return "", fmt.Errorf("result: %w", err)
		}
		out.WriteString(resultType)
	}
	return out.String(), nil
}

func renderRegistration(model Model, names map[string]string, output Output) ([]byte, error) {
	functions := append([]FunctionDecl(nil), model.Functions...)
	sort.Slice(functions, func(i, j int) bool { return functions[i].CName < functions[j].CName })
	var body strings.Builder
	body.WriteString("var allSymbols = []string{\n")
	for _, function := range functions {
		body.WriteString("\t\"" + function.CName + "\",\n")
	}
	body.WriteString("}\n\n")
	body.WriteString("func register(handle uintptr) error {\n")
	body.WriteString("\taddresses := make(map[string]uintptr, len(allSymbols))\n")
	body.WriteString("\tfor _, symbol := range allSymbols {\n")
	body.WriteString("\t\taddress, err := purego.Dlsym(handle, symbol)\n")
	body.WriteString("\t\tif err != nil {\n")
	body.WriteString("\t\t\treturn fmt.Errorf(\"resolve %s: %w\", symbol, err)\n")
	body.WriteString("\t\t}\n")
	body.WriteString("\t\taddresses[symbol] = address\n")
	body.WriteString("\t}\n")
	for _, function := range functions {
		name := names[function.CName]
		if name == "" {
			return nil, fmt.Errorf("missing Go name: %s", function.CName)
		}
		body.WriteString("\tpurego.RegisterFunc(&" + name + ", addresses[\"" + function.CName + "\"])\n")
	}
	body.WriteString("\treturn nil\n")
	body.WriteString("}\n")
	return goFileWithImports(output, "register_gen.go", body.String(), []string{"fmt", "github.com/bnema/purego"}), nil
}

func renderConstants(model Model, names map[string]string, output Output) ([]byte, error) {
	var body strings.Builder
	for _, typ := range model.Types {
		if typ.Kind != TypeEnum || len(typ.EnumValues) == 0 {
			continue
		}
		typeName, err := generatedName(names, typ.CName)
		if err != nil {
			return nil, err
		}
		body.WriteString("const (\n")
		for _, value := range typ.EnumValues {
			valueName, err := generatedName(names, value.CName)
			if err != nil {
				return nil, err
			}
			body.WriteString("\t" + valueName + " " + typeName + " = " + strconv.FormatInt(value.Value, 10) + "\n")
		}
		body.WriteString(")\n\n")
	}
	for _, constant := range model.Constants {
		name, err := generatedName(names, constant.CName)
		if err != nil {
			return nil, err
		}
		if err := validateConstantExpression(constant.Value); err != nil {
			return nil, fmt.Errorf("%s: %w", constant.CName, err)
		}
		body.WriteString("const " + name + " = " + constant.Value + "\n\n")
	}
	if body.Len() == 0 {
		body.WriteString("// no constants\n")
	}
	return goFile(output, "constants_gen.go", body.String(), false, ""), nil
}

func generatedName(names map[string]string, cName string) (string, error) {
	name := names[cName]
	if name == "" {
		return "", fmt.Errorf("missing Go name: %s", cName)
	}
	if name == "_" || !token.IsIdentifier(name) {
		return "", fmt.Errorf("invalid Go name %q for %s", name, cName)
	}
	return name, nil
}

func validateConstantExpression(value string) error {
	expr, err := parser.ParseExpr(value)
	if err != nil {
		return fmt.Errorf("invalid constant expression %q: %w", value, err)
	}
	if !isConstantExpression(expr) {
		return fmt.Errorf("unsupported constant expression %q", value)
	}
	return nil
}

func isConstantExpression(expr ast.Expr) bool {
	switch expr := expr.(type) {
	case *ast.BasicLit:
		return true
	case *ast.Ident:
		return expr.Name == "true" || expr.Name == "false" || expr.Name == "iota"
	case *ast.ParenExpr:
		return isConstantExpression(expr.X)
	case *ast.UnaryExpr:
		switch expr.Op {
		case token.ADD, token.SUB, token.XOR, token.NOT:
			return isConstantExpression(expr.X)
		default:
			return false
		}
	case *ast.BinaryExpr:
		return isConstantExpression(expr.X) && isConstantExpression(expr.Y)
	default:
		return false
	}
}

func renderABI(model Model, names map[string]string, layouts map[string]map[string]RecordLayout, split map[string]bool, output Output) (map[string][]byte, error) {
	files := make(map[string][]byte)
	arches := sortedArchitectures(layouts)
	if !hasSplitForArch(model, split) {
		data, test, err := renderABIFor(&model, &model, names, layouts[arches[0]], output, "abi_gen.go", "abi_gen_test.go", "")
		if err != nil {
			return nil, err
		}
		files["abi_gen.go"] = data
		files["abi_gen_test.go"] = test
		files["abi_records_gen.go"] = goFile(output, "abi_records_gen.go", renderABIRecords(model), false, "integration")
		return files, nil
	}
	commonModel := modelWithoutSplit(model, split)
	data, test, err := renderABIFor(&commonModel, nil, names, layouts[arches[0]], output, "abi_gen.go", "abi_gen_test.go", "")
	if err != nil {
		return nil, err
	}
	files["abi_gen.go"] = data
	files["abi_gen_test.go"] = test
	for _, arch := range arches {
		archModel := modelOnlySplit(model, split)
		data, test, err := renderABIFor(&archModel, &model, names, layouts[arch], output, "abi_gen_"+arch+".go", "abi_gen_"+arch+"_test.go", arch)
		if err != nil {
			return nil, err
		}
		files["abi_gen_"+arch+".go"] = data
		files["abi_gen_"+arch+"_test.go"] = test
	}
	files["abi_records_gen.go"] = goFile(output, "abi_records_gen.go", renderABIRecords(model), false, "integration")
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

func renderABIFor(model *Model, metadataModel *Model, names map[string]string, layouts map[string]RecordLayout, output Output, goName, testName, arch string) ([]byte, []byte, error) {
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
	needsUnsafe := false
	for _, typ := range model.Types {
		if typ.Kind != TypeStruct && typ.Kind != TypeUnion {
			continue
		}
		layout := layouts[typ.CName]
		name := names[typ.CName]
		if name == "" {
			return nil, nil, fmt.Errorf("missing Go name: %s", typ.CName)
		}
		needsUnsafe = true
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
	return goFile(output, goName, body.String(), false, arch), goTestFile(output, testName, tests.String(), arch, needsUnsafe), nil
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

func renderABIRecords(model Model) string {
	var body strings.Builder
	body.WriteString("type abiField struct { name string; offset uintptr }\n")
	body.WriteString("type abiRecord struct { name string; size uintptr; align uintptr; fields []abiField }\n\n")
	body.WriteString("func abiRecords() []abiRecord {\n")
	body.WriteString("\treturn []abiRecord{\n")
	for _, typ := range model.Types {
		if typ.Kind != TypeStruct && typ.Kind != TypeUnion {
			continue
		}
		prefix := abiIdentifier(typ.CName)
		body.WriteString("\t\t{name: " + strconv.Quote(typ.CName) + ", size: " + prefix + "Size, align: " + prefix + "Align, fields: []abiField{")
		for _, field := range typ.Fields {
			body.WriteString("{name: " + strconv.Quote(field.CName) + ", offset: " + prefix + "_" + abiIdentifier(field.CName) + "Offset}, ")
		}
		body.WriteString("}},\n")
	}
	body.WriteString("\t}\n}\n\n")
	return body.String()
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

func goFileWithImports(output Output, name, body string, imports []string) []byte {
	var source bytes.Buffer
	source.WriteString("// Code generated by ghosttygen; DO NOT EDIT.\n")
	source.WriteString("// Ghostty commit: " + output.Upstream.Commit + "\n\n")
	source.WriteString("package " + output.Package + "\n\n")
	source.WriteString("import (\n")
	for _, path := range imports {
		source.WriteString("\t\"" + path + "\"\n")
	}
	source.WriteString(")\n\n")
	source.WriteString(body)
	return source.Bytes()
}

func goFile(output Output, name, body string, needsUnsafe bool, buildConstraint string) []byte {
	var source bytes.Buffer
	if buildConstraint != "" {
		source.WriteString("//go:build " + buildConstraint + "\n\n")
	}
	source.WriteString("// Code generated by ghosttygen; DO NOT EDIT.\n")
	source.WriteString("// Ghostty commit: " + output.Upstream.Commit + "\n\n")
	source.WriteString("package " + output.Package + "\n\n")
	if needsUnsafe {
		source.WriteString("import \"unsafe\"\n\n")
	}
	source.WriteString(body)
	return source.Bytes()
}

func goTestFile(output Output, name, body, buildConstraint string, needsUnsafe bool) []byte {
	var source bytes.Buffer
	if buildConstraint != "" {
		source.WriteString("//go:build " + buildConstraint + "\n\n")
	}
	source.WriteString("// Code generated by ghosttygen; DO NOT EDIT.\n")
	source.WriteString("// Ghostty commit: " + output.Upstream.Commit + "\n\n")
	source.WriteString("package " + output.Package + "\n\nimport (\n\t\"testing\"\n")
	if needsUnsafe {
		source.WriteString("\t\"unsafe\"\n")
	}
	source.WriteString(")\n\n")
	source.WriteString(body)
	return source.Bytes()
}

var generatedFileNames = []string{
	"types_gen.go", "types_gen_amd64.go", "types_gen_arm64.go",
	"constants_gen.go", "abi_gen.go", "abi_gen_amd64.go", "abi_gen_arm64.go", "abi_records_gen.go",
	"abi_gen_test.go", "abi_gen_amd64_test.go", "abi_gen_arm64_test.go",
	// Remove the pre-fix spelling when refreshing an existing generated package.
	"abi_gen_test_amd64.go", "abi_gen_test_arm64.go",
	"coverage_gen.json", "functions_gen.go", "register_gen.go",
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
	for _, name := range generatedFileNames {
		if _, ok := files[name]; ok {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
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
