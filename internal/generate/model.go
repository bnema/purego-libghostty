package generate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Model struct {
	Types      []TypeDecl     `json:"types"`
	Constants  []ConstantDecl `json:"constants"`
	Functions  []FunctionDecl `json:"functions"`
	Exclusions []Exclusion    `json:"exclusions"`
}

type Exclusion struct {
	CName  string `json:"c_name"`
	Reason string `json:"reason"`
}
type TypeKind string

const (
	TypeAlias    TypeKind = "alias"
	TypeEnum     TypeKind = "enum"
	TypeStruct   TypeKind = "struct"
	TypeUnion    TypeKind = "union"
	TypeCallback TypeKind = "callback"
)

type TypeRef struct {
	CName    string `json:"c_name,omitempty"`
	Pointers int    `json:"pointers,omitempty"`
	Const    bool   `json:"const,omitempty"`
	ArrayLen int    `json:"array_len,omitempty"`
}
type TypeDecl struct {
	CName      string      `json:"c_name"`
	Kind       TypeKind    `json:"kind"`
	Source     string      `json:"source"`
	Type       TypeRef     `json:"type,omitempty"`
	Result     *TypeRef    `json:"result,omitempty"`
	Parameters []Parameter `json:"parameters,omitempty"`
	Fields     []Field     `json:"fields,omitempty"`
	EnumValues []EnumValue `json:"enum_values,omitempty"`
}
type Field struct {
	CName  string  `json:"c_name"`
	Source string  `json:"source"`
	Type   TypeRef `json:"type"`
}
type EnumValue struct {
	CName string `json:"c_name"`
	Value int64  `json:"value"`
}
type ConstantDecl struct {
	CName  string `json:"c_name"`
	Value  string `json:"value"`
	Source string `json:"source"`
}
type FunctionDecl struct {
	CName      string      `json:"c_name"`
	Source     string      `json:"source"`
	Result     TypeRef     `json:"result"`
	Parameters []Parameter `json:"parameters"`
}
type Parameter struct {
	CName string  `json:"c_name,omitempty"`
	Type  TypeRef `json:"type"`
}
type UnsupportedDeclarationError struct{ CName string }

func (e *UnsupportedDeclarationError) Error() string {
	return "unsupported reachable declaration: " + e.CName
}

type astNode struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Value   string `json:"value"`
	TagUsed string `json:"tagUsed"`
	Decl    struct {
		ID string `json:"id"`
	} `json:"decl"`
	Type struct {
		QualType string `json:"qualType"`
	} `json:"type"`
	Loc   astLoc `json:"loc"`
	Range struct {
		Begin astLoc `json:"begin"`
	} `json:"range"`
	Inner []*astNode `json:"inner"`
}
type astLoc struct {
	File         string  `json:"file"`
	Line         int     `json:"line"`
	SpellingLoc  *astLoc `json:"spellingLoc"`
	ExpansionLoc *astLoc `json:"expansionLoc"`
	Value        string  `json:"value"`
}

var ghosttyName = regexp.MustCompile(`\bghostty_[a-zA-Z0-9_]+\b`)
var arrayType = regexp.MustCompile(`\s*\[([0-9]+)\]`)

func InspectHeader(ctx context.Context, clang, header, includeDir string, targets []Target, roots []string) (Model, error) {
	if err := auditPreprocessor(header); err != nil {
		return Model{}, err
	}
	if len(targets) == 0 {
		return Model{}, fmt.Errorf("no targets")
	}
	var first Model
	for i, target := range targets {
		ast, err := inspectAST(ctx, clang, header, includeDir, target)
		if err != nil {
			return Model{}, err
		}
		macros, err := inspectMacros(ctx, clang, header, includeDir, target)
		if err != nil {
			return Model{}, err
		}
		model, err := selectPublic(ast, macros, header, roots)
		if err != nil {
			return Model{}, err
		}
		if i == 0 {
			first = model
		} else if !sameModel(first, model) {
			return Model{}, fmt.Errorf("target %s declaration set differs from %s", target.GOARCH, targets[0].GOARCH)
		}
	}
	linuxFunctions := make(map[string]bool, len(first.Functions))
	for _, f := range first.Functions {
		linuxFunctions[f.CName] = true
	}
	for _, variant := range EmbeddingVariants {
		ast, err := inspectAST(ctx, clang, header, includeDir, targets[0], variant.Defines...)
		if err != nil {
			return Model{}, err
		}
		variantModel, err := selectPublic(ast, nil, header, roots)
		if err != nil {
			return Model{}, err
		}
		for _, f := range variantModel.Functions {
			if !linuxFunctions[f.CName] {
				first.Exclusions = append(first.Exclusions, Exclusion{CName: f.CName, Reason: "platform-excluded: " + variant.Reason})
			}
		}
	}
	sortModel(&first)
	return first, nil
}

func WriteModel(ctx context.Context, clang, header, includeDir string, targets []Target, out io.Writer) error {
	model, err := InspectHeader(ctx, clang, header, includeDir, targets, nil)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(model)
}

func sameModel(a, b Model) bool {
	a.Exclusions = nil
	b.Exclusions = nil
	sortModel(&a)
	sortModel(&b)
	dataA, _ := json.Marshal(a)
	dataB, _ := json.Marshal(b)
	return string(dataA) == string(dataB)
}

func typeRef(qual string) TypeRef {
	ref := TypeRef{Const: strings.Contains(qual, "const "), Pointers: strings.Count(qual, "*")}
	if matches := arrayType.FindStringSubmatch(qual); len(matches) == 2 {
		ref.ArrayLen, _ = strconv.Atoi(matches[1])
	}
	if name := ghosttyName.FindString(qual); name != "" {
		ref.CName = name
	} else {
		ref.CName = strings.TrimSpace(arrayType.ReplaceAllString(strings.ReplaceAll(strings.ReplaceAll(qual, "const ", ""), "*", ""), ""))
	}
	return ref
}

func sortModel(model *Model) {
	sort.Slice(model.Types, func(i, j int) bool { return model.Types[i].CName < model.Types[j].CName })
	sort.Slice(model.Constants, func(i, j int) bool { return model.Constants[i].CName < model.Constants[j].CName })
	sort.Slice(model.Functions, func(i, j int) bool { return model.Functions[i].CName < model.Functions[j].CName })
	sort.Slice(model.Exclusions, func(i, j int) bool { return model.Exclusions[i].CName < model.Exclusions[j].CName })
}
