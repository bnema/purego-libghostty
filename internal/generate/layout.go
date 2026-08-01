package generate

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type RecordLayout struct {
	CName  string
	Size   int
	Align  int
	Fields []FieldLayout
}

type FieldLayout struct {
	CName  string
	Offset int
}

type UnionStorage struct {
	Size            int
	Align           int
	AlignmentGoType string
}

var (
	recordStart = regexp.MustCompile(`^\s*0 \| (?:struct|union) (.+)$`)
	recordSize  = regexp.MustCompile(`^\s*\| \[sizeof=([0-9]+), align=([0-9]+)\]$`)
	recordField = regexp.MustCompile(`^\s*([0-9]+) \|(\s+)(.+)$`)
	recordLoc   = regexp.MustCompile(` at (.+):([0-9]+):[0-9]+\)`)
)

// InspectRecordLayouts obtains Clang's complete record layouts for one target.
func InspectRecordLayouts(ctx context.Context, clang, header, includeDir string, target Target, model Model) (map[string]RecordLayout, error) {
	layouts, err := recordLayoutOutput(ctx, clang, header, includeDir, target)
	if err != nil {
		return nil, err
	}
	ast, err := inspectAST(ctx, clang, header, includeDir, target)
	if err != nil {
		return nil, err
	}
	names, err := recordLayoutNames(ast, header, model)
	if err != nil {
		return nil, err
	}
	found := make(map[string]RecordLayout)
	for _, layout := range layouts {
		cName := names[layout.location]
		if cName == "" {
			for _, typ := range model.Types {
				if (typ.Kind == TypeStruct || typ.Kind == TypeUnion) && strings.Contains(layout.label, typ.CName) {
					if cName != "" && cName != typ.CName {
						return nil, fmt.Errorf("duplicate record layout match: %s and %s", cName, typ.CName)
					}
					cName = typ.CName
				}
			}
		}
		if cName == "" {
			continue
		}
		if _, ok := found[cName]; ok {
			return nil, fmt.Errorf("duplicate record layout: %s", cName)
		}
		layout.RecordLayout.CName = cName
		found[cName] = layout.RecordLayout
	}
	return normalizeRecordLayouts(model, found)
}

func recordLayoutOutput(ctx context.Context, clang, header, includeDir string, target Target) ([]parsedRecordLayout, error) {
	prelude, cleanup, err := targetPrelude(target)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	output, err := clangOutput(ctx, clang, "--target="+target.Triple, "-include", prelude, "-ffreestanding", "-std=c11", "-I", includeDir, "-fsyntax-only", "-Xclang", "-fdump-record-layouts-complete", "-x", "c", header)
	if err != nil {
		return nil, err
	}
	return parseRecordLayouts(string(output))
}

type parsedRecordLayout struct {
	RecordLayout
	label    string
	location string
}

func parseRecordLayouts(output string) ([]parsedRecordLayout, error) {
	var records []parsedRecordLayout
	var current *parsedRecordLayout
	for _, line := range strings.Split(output, "\n") {
		if match := recordStart.FindStringSubmatch(line); len(match) == 2 {
			records = append(records, parsedRecordLayout{label: match[1], location: recordLocation(match[1])})
			current = &records[len(records)-1]
			continue
		}
		if current == nil {
			continue
		}
		if match := recordSize.FindStringSubmatch(line); len(match) == 3 {
			current.Size, _ = strconv.Atoi(match[1])
			current.Align, _ = strconv.Atoi(match[2])
			current = nil
			continue
		}
		match := recordField.FindStringSubmatch(line)
		if len(match) != 4 || len(match[2]) != 3 {
			continue
		}
		offset, _ := strconv.Atoi(match[1])
		parts := strings.Fields(match[3])
		if len(parts) == 0 {
			continue
		}
		name := parts[len(parts)-1]
		current.Fields = append(current.Fields, FieldLayout{CName: name, Offset: offset})
	}
	return records, nil
}

func recordLocation(label string) string {
	match := recordLoc.FindStringSubmatch(label)
	if len(match) != 3 {
		return ""
	}
	return filepath.Clean(match[1]) + ":" + match[2]
}

func recordLayoutNames(astJSON []byte, header string, model Model) (map[string]string, error) {
	var root astNode
	if err := json.Unmarshal(astJSON, &root); err != nil {
		return nil, fmt.Errorf("decode clang AST: %w", err)
	}
	nodes := map[string]*astNode{}
	var walk func(*astNode)
	walk = func(node *astNode) {
		nodes[node.ID] = node
		for _, child := range node.Inner {
			walk(child)
		}
	}
	walk(&root)
	selected := map[string]TypeDecl{}
	for _, typ := range model.Types {
		if typ.Kind == TypeStruct || typ.Kind == TypeUnion {
			selected[typ.CName] = typ
		}
	}
	names := map[string]string{}
	for _, node := range nodes {
		if node.Kind != "TypedefDecl" {
			continue
		}
		typ, ok := selected[node.Name]
		if !ok {
			continue
		}
		record := typedefRecord(node, nodes)
		if record == nil {
			return nil, fmt.Errorf("missing record declaration: %s", typ.CName)
		}
		if err := addRecordName(names, record, typ.CName, header); err != nil {
			return nil, err
		}
		for i, child := range record.Inner {
			if child.Kind != "RecordDecl" {
				continue
			}
			for _, field := range record.Inner[i+1:] {
				if field.Kind != "FieldDecl" {
					continue
				}
				if strings.Contains(field.Type.QualType, "(unnamed") {
					nestedName := typ.CName + "." + field.Name
					if _, ok := selected[nestedName]; ok {
						if err := addRecordName(names, child, nestedName, header); err != nil {
							return nil, err
						}
					}
				}
				break
			}
		}
	}
	return names, nil
}

func typedefRecord(node *astNode, nodes map[string]*astNode) *astNode {
	for _, child := range node.Inner {
		if child.Decl.ID == "" {
			continue
		}
		if record := nodes[child.Decl.ID]; record != nil && record.Kind == "RecordDecl" {
			return record
		}
	}
	return nil
}

func addRecordName(names map[string]string, record *astNode, cName, header string) error {
	path, line := sourceLocation(record, header)
	key := path + ":" + strconv.Itoa(line)
	if existing := names[key]; existing != "" && existing != cName {
		return fmt.Errorf("duplicate record declaration: %s and %s", existing, cName)
	}
	names[key] = cName
	return nil
}

func normalizeRecordLayouts(model Model, found map[string]RecordLayout) (map[string]RecordLayout, error) {
	layouts := make(map[string]RecordLayout)
	for _, typ := range model.Types {
		if typ.Kind != TypeStruct && typ.Kind != TypeUnion {
			continue
		}
		layout, ok := found[typ.CName]
		if !ok {
			return nil, fmt.Errorf("missing record layout: %s", typ.CName)
		}
		if layout.Size < 0 || layout.Align <= 0 {
			return nil, fmt.Errorf("invalid record layout: %s", typ.CName)
		}
		fields := make(map[string]FieldLayout, len(layout.Fields))
		for _, field := range layout.Fields {
			if _, duplicate := fields[field.CName]; duplicate {
				return nil, fmt.Errorf("duplicate field layout: %s.%s", typ.CName, field.CName)
			}
			fields[field.CName] = field
		}
		layout.Fields = layout.Fields[:0]
		for _, field := range typ.Fields {
			fieldLayout, ok := fields[field.CName]
			if !ok {
				return nil, fmt.Errorf("missing field layout: %s.%s", typ.CName, field.CName)
			}
			layout.Fields = append(layout.Fields, fieldLayout)
		}
		layout.CName = typ.CName
		layouts[typ.CName] = layout
	}
	return layouts, nil
}

// UnionStorageFor describes the generated [0]T alignment field and byte storage.
func UnionStorageFor(layout RecordLayout) (UnionStorage, error) {
	alignmentTypes := map[int]string{1: "byte", 2: "uint16", 4: "uint32", 8: "uint64"}
	alignmentType, ok := alignmentTypes[layout.Align]
	if !ok {
		return UnionStorage{}, fmt.Errorf("unsupported union alignment %d for %s", layout.Align, layout.CName)
	}
	return UnionStorage{Size: layout.Size, Align: layout.Align, AlignmentGoType: alignmentType}, nil
}

// RequiresArchSplit reports whether a record needs architecture-specific output.
func RequiresArchSplit(layouts map[string]map[string]RecordLayout, cName string) (bool, error) {
	arches := make([]string, 0, len(layouts))
	for arch := range layouts {
		arches = append(arches, arch)
	}
	sort.Strings(arches)
	if len(arches) == 0 {
		return false, fmt.Errorf("no target layouts")
	}
	first, ok := layouts[arches[0]][cName]
	if !ok {
		return false, fmt.Errorf("missing record layout: %s (%s)", cName, arches[0])
	}
	for _, arch := range arches[1:] {
		layout, ok := layouts[arch][cName]
		if !ok {
			return false, fmt.Errorf("missing record layout: %s (%s)", cName, arch)
		}
		if !sameRecordLayout(first, layout) {
			return true, nil
		}
	}
	return false, nil
}

func sameRecordLayout(a, b RecordLayout) bool {
	if a.Size != b.Size || a.Align != b.Align || len(a.Fields) != len(b.Fields) {
		return false
	}
	for i := range a.Fields {
		if a.Fields[i] != b.Fields[i] {
			return false
		}
	}
	return true
}
