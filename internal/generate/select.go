package generate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var explicitEmbeddingRoots = []string{
	"ghostty_input_trigger_key_u", "ghostty_platform_u", "ghostty_quick_terminal_size_value_u", "ghostty_target_u",
	"ghostty_action_key_table_u", "ghostty_action_u", "ghostty_ipc_target_u", "ghostty_ipc_action_u",
}

func selectPublic(astJSON, macros []byte, header string, roots []string) (Model, error) {
	var root astNode
	if err := json.Unmarshal(astJSON, &root); err != nil {
		return Model{}, fmt.Errorf("decode clang AST: %w", err)
	}
	nodes := map[string]*astNode{}
	var all []*astNode
	var walk func(*astNode)
	walk = func(node *astNode) {
		nodes[node.ID] = node
		all = append(all, node)
		for _, child := range node.Inner {
			walk(child)
		}
	}
	walk(&root)
	candidates := map[string]*astNode{}
	for _, node := range all {
		if node.Kind == "TypedefDecl" && strings.HasPrefix(node.Name, "ghostty_") && inHeader(node, header) {
			candidates[node.Name] = node
		}
	}
	model := Model{}
	needed := map[string]bool{}
	for _, node := range all {
		if node.Kind != "FunctionDecl" || !strings.HasPrefix(node.Name, "ghostty_") || !inHeader(node, header) || !hasVisibility(node) {
			continue
		}
		function := FunctionDecl{CName: node.Name, Source: sourcePath(node, header), Result: resultType(node.Type.QualType)}
		addRefs(needed, function.Result)
		for _, child := range node.Inner {
			if child.Kind != "ParmVarDecl" {
				continue
			}
			parameter := Parameter{CName: child.Name, Type: typeRef(child.Type.QualType)}
			function.Parameters = append(function.Parameters, parameter)
			addRefs(needed, parameter.Type)
		}
		model.Functions = append(model.Functions, function)
	}
	for _, name := range explicitEmbeddingRoots {
		if _, ok := candidates[name]; ok {
			needed[name] = true
		}
	}
	for _, name := range roots {
		if _, ok := candidates[name]; ok {
			needed[name] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for name := range needed {
			node, ok := candidates[name]
			if !ok {
				continue
			}
			decl, refs, err := declaration(node, nodes, header)
			if err != nil {
				return Model{}, err
			}
			if !containsType(model.Types, decl.CName) {
				model.Types = append(model.Types, decl)
			}
			for _, nested := range anonymousDeclarations(node, nodes, header) {
				if !containsType(model.Types, nested.CName) {
					model.Types = append(model.Types, nested)
				}
			}
			for _, ref := range refs {
				if _, ok := candidates[ref]; ok && !needed[ref] {
					needed[ref] = true
					changed = true
				}
			}
		}
	}
	model.Constants = selectMacros(macros, header)
	sortModel(&model)
	return model, nil
}

func declaration(node *astNode, nodes map[string]*astNode, header string) (TypeDecl, []string, error) {
	decl := TypeDecl{CName: node.Name, Source: sourcePath(node, header), Kind: TypeAlias, Type: typeRef(node.Type.QualType)}
	var target *astNode
	for _, child := range node.Inner {
		if child.Decl.ID != "" {
			target = nodes[child.Decl.ID]
			break
		}
	}
	if target == nil {
		if strings.Contains(node.Type.QualType, "(*)") {
			result, parameters, ok := callbackSignature(node)
			if !ok {
				return TypeDecl{}, nil, &UnsupportedDeclarationError{CName: node.Name}
			}
			decl.Kind = TypeCallback
			decl.Result = &result
			decl.Parameters = parameters
		}
		return decl, refsInType(node.Type.QualType), nil
	}
	switch target.Kind {
	case "EnumDecl":
		decl.Kind = TypeEnum
		var value int64
		for _, child := range target.Inner {
			if child.Kind != "EnumConstantDecl" || !strings.HasPrefix(child.Name, "GHOSTTY_") {
				continue
			}
			if explicit, ok := enumValue(child); ok {
				value = explicit
			}
			decl.EnumValues = append(decl.EnumValues, EnumValue{CName: child.Name, Value: value})
			value++
		}
	case "RecordDecl":
		if target.TagUsed == "union" {
			decl.Kind = TypeUnion
		} else if target.TagUsed == "struct" {
			decl.Kind = TypeStruct
		} else {
			return TypeDecl{}, nil, &UnsupportedDeclarationError{CName: node.Name}
		}
		return recordDeclaration(decl, target, header)
	default:
		return TypeDecl{}, nil, &UnsupportedDeclarationError{CName: node.Name}
	}
	return decl, refsInType(node.Type.QualType), nil
}

func anonymousDeclarations(node *astNode, nodes map[string]*astNode, header string) []TypeDecl {
	var record *astNode
	for _, child := range node.Inner {
		if child.Decl.ID != "" {
			record = nodes[child.Decl.ID]
			break
		}
	}
	if record == nil || record.Kind != "RecordDecl" {
		return nil
	}
	var declarations []TypeDecl
	for i, child := range record.Inner {
		if child.Kind != "RecordDecl" {
			continue
		}
		for _, field := range record.Inner[i+1:] {
			if field.Kind != "FieldDecl" {
				continue
			}
			if !strings.Contains(field.Type.QualType, "(unnamed") {
				break
			}
			kind := TypeStruct
			if child.TagUsed == "union" {
				kind = TypeUnion
			}
			decl := TypeDecl{CName: node.Name + "." + field.Name, Kind: kind, Source: sourcePath(child, header)}
			for _, nestedField := range child.Inner {
				if nestedField.Kind == "FieldDecl" {
					decl.Fields = append(decl.Fields, Field{CName: nestedField.Name, Source: sourcePath(nestedField, header), Type: typeRef(nestedField.Type.QualType)})
				}
			}
			declarations = append(declarations, decl)
			break
		}
	}
	return declarations
}

func recordDeclaration(decl TypeDecl, record *astNode, header string) (TypeDecl, []string, error) {
	refs := []string{}
	var nested *astNode
	for _, child := range record.Inner {
		if child.Kind == "RecordDecl" {
			nested = child
			continue
		}
		if child.Kind != "FieldDecl" {
			continue
		}
		fieldType := typeRef(child.Type.QualType)
		if nested != nil && strings.Contains(child.Type.QualType, "(unnamed") {
			fieldType.CName = decl.CName + "." + child.Name
			for _, nestedField := range nested.Inner {
				if nestedField.Kind == "FieldDecl" {
					refs = append(refs, refsInType(nestedField.Type.QualType)...)
				}
			}
		}
		decl.Fields = append(decl.Fields, Field{CName: child.Name, Source: sourcePath(child, header), Type: fieldType})
		refs = append(refs, refsInType(child.Type.QualType)...)
		nested = nil
	}
	// Anonymous child records are public only through this selected declaration. They are represented
	// as a stable companion type so later layout work can identify them without Clang node IDs.
	for i, child := range record.Inner {
		if child.Kind != "RecordDecl" {
			continue
		}
		for _, field := range record.Inner[i+1:] {
			if field.Kind != "FieldDecl" {
				continue
			}
			if strings.Contains(field.Type.QualType, "(unnamed") {
				refs = append(refs, decl.CName+"."+field.Name)
				break
			}
		}
	}
	return decl, refs, nil
}

func callbackSignature(node *astNode) (TypeRef, []Parameter, bool) {
	var prototype *astNode
	var find func(*astNode)
	find = func(current *astNode) {
		if prototype != nil {
			return
		}
		if current.Kind == "FunctionProtoType" {
			prototype = current
			return
		}
		for _, child := range current.Inner {
			find(child)
		}
	}
	find(node)
	if prototype == nil || len(prototype.Inner) == 0 {
		return TypeRef{}, nil, false
	}
	result := typeRef(prototype.Inner[0].Type.QualType)
	parameters := make([]Parameter, 0, len(prototype.Inner)-1)
	for _, parameter := range prototype.Inner[1:] {
		parameters = append(parameters, Parameter{Type: typeRef(parameter.Type.QualType)})
	}
	return result, parameters, true
}

func refsInType(qual string) []string { return ghosttyName.FindAllString(qual, -1) }
func addRefs(needed map[string]bool, ref TypeRef) {
	if strings.HasPrefix(ref.CName, "ghostty_") {
		needed[ref.CName] = true
	}
}
func containsType(types []TypeDecl, name string) bool {
	for _, typ := range types {
		if typ.CName == name {
			return true
		}
	}
	return false
}
func resultType(qual string) TypeRef {
	if i := strings.IndexByte(qual, '('); i >= 0 {
		return typeRef(strings.TrimSpace(qual[:i]))
	}
	return typeRef(qual)
}
func hasVisibility(node *astNode) bool {
	for _, child := range node.Inner {
		if child.Kind == "VisibilityAttr" {
			return true
		}
	}
	return false
}
func inHeader(node *astNode, header string) bool {
	file := sourcePath(node, header)
	return file == filepath.Clean(header) || samePath(file, header)
}
func sourcePath(node *astNode, header string) string {
	path, _ := sourceLocation(node, header)
	return path
}

func sourceLocation(node *astNode, header string) (string, int) {
	for _, loc := range []*astLoc{&node.Loc, node.Range.Begin.SpellingLoc, node.Range.Begin.ExpansionLoc} {
		if loc != nil && loc.File != "" {
			return filepath.Clean(loc.File), loc.Line
		}
	}
	return filepath.Clean(header), node.Loc.Line
}
func samePath(a, b string) bool {
	aa, ea := filepath.Abs(a)
	bb, eb := filepath.Abs(b)
	return ea == nil && eb == nil && aa == bb
}

func enumValue(node *astNode) (int64, bool) {
	var find func(*astNode) (string, bool)
	find = func(n *astNode) (string, bool) {
		if n.Value != "" {
			return n.Value, true
		}
		for _, child := range n.Inner {
			if value, ok := find(child); ok {
				return value, true
			}
		}
		return "", false
	}
	value, ok := find(node)
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 0, 64)
	return parsed, err == nil
}

var defineLine = regexp.MustCompile(`^\s*#\s*define\s+(GHOSTTY_[A-Za-z0-9_]+)(\s*\()?\s*(.*)$`)

func normalizePreprocessorCondition(condition string) string {
	condition = strings.TrimSpace(condition)
	if !strings.HasPrefix(condition, "defined") {
		return condition
	}
	rest := condition[len("defined"):]
	if rest == "" || (rest[0] != '(' && rest[0] != ' ' && rest[0] != '\t') {
		return condition
	}
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, "(") {
		if !strings.HasSuffix(rest, ")") {
			return condition
		}
		rest = strings.TrimSpace(rest[1 : len(rest)-1])
	}
	if rest == "" {
		return condition
	}
	for i, r := range rest {
		if i == 0 {
			if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
				return condition
			}
			continue
		}
		if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return condition
		}
	}
	return rest
}

func selectMacros(macros []byte, header string) []ConstantDecl {
	if len(macros) == 0 {
		return nil
	}
	data, err := os.ReadFile(header)
	if err != nil {
		return nil
	}
	lexical := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if match := defineLine.FindStringSubmatch(line); len(match) == 4 && match[2] == "" {
			lexical[match[1]] = true
		}
	}
	var constants []ConstantDecl
	for _, line := range bytes.Split(macros, []byte("\n")) {
		match := defineLine.FindStringSubmatch(string(line))
		if len(match) != 4 || match[2] != "" || !lexical[match[1]] {
			continue
		}
		value := strings.TrimSpace(match[3])
		if integerConstant(value) || stringConstant(value) {
			constants = append(constants, ConstantDecl{CName: match[1], Value: value, Source: filepath.Clean(header)})
		}
	}
	sort.Slice(constants, func(i, j int) bool { return constants[i].CName < constants[j].CName })
	return constants
}
func integerConstant(value string) bool { _, err := strconv.ParseInt(value, 0, 64); return err == nil }
func stringConstant(value string) bool {
	return len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"'
}

func auditPreprocessor(header string) error {
	data, err := os.ReadFile(header)
	if err != nil {
		return err
	}
	var conditions []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "#if "):
			conditions = append(conditions, normalizePreprocessorCondition(strings.TrimPrefix(trimmed, "#if")))
		case strings.HasPrefix(trimmed, "#ifdef "):
			conditions = append(conditions, strings.TrimSpace(strings.TrimPrefix(trimmed, "#ifdef")))
		case strings.HasPrefix(trimmed, "#ifndef "):
			conditions = append(conditions, strings.TrimSpace(strings.TrimPrefix(trimmed, "#ifndef")))
		case strings.HasPrefix(trimmed, "#elif "):
			if len(conditions) > 0 {
				conditions[len(conditions)-1] = normalizePreprocessorCondition(strings.TrimPrefix(trimmed, "#elif"))
			}
		case strings.HasPrefix(trimmed, "#else"):
			if len(conditions) > 0 {
				conditions[len(conditions)-1] = "else"
			}
		case strings.HasPrefix(trimmed, "#endif"):
			if len(conditions) > 0 {
				conditions = conditions[:len(conditions)-1]
			}
		}
		if strings.Contains(line, "GHOSTTY_API") && strings.Contains(line, "ghostty_") {
			for _, condition := range conditions {
				if condition != "__APPLE__" && condition != "GHOSTTY_H" {
					return fmt.Errorf("unregistered preprocessor condition %q for public declaration", condition)
				}
			}
		}
	}
	return nil
}
