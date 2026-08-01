package generate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordLayouts(t *testing.T) {
	header := fixtureHeader(t)
	model, err := InspectHeader(context.Background(), "clang", header, filepath.Dir(header), LinuxTargets, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range LinuxTargets {
		layouts, err := InspectRecordLayouts(context.Background(), "clang", header, filepath.Dir(header), target, model)
		if err != nil {
			t.Fatal(err)
		}
		fixture := layouts["ghostty_fixture_s"]
		if fixture.Size != 48 || fixture.Align != 8 {
			t.Errorf("%s fixture layout = %#v", target.GOARCH, fixture)
		}
		if got := fieldOffset(fixture, "enabled"); got != 40 {
			t.Errorf("%s enabled offset = %d, want 40", target.GOARCH, got)
		}
		if _, ok := layouts["ghostty_value_u.named"]; !ok {
			t.Errorf("%s missing nested record layout", target.GOARCH)
		}
	}
}

func TestRecordLayoutsRealHeader(t *testing.T) {
	source := os.Getenv("GHOSTTY_SOURCE_DIR")
	if source == "" {
		t.Skip("GHOSTTY_SOURCE_DIR is not set")
	}
	header := filepath.Join(source, "include", "ghostty.h")
	model, err := InspectHeader(context.Background(), "clang", header, filepath.Dir(header), LinuxTargets, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, typ := range model.Types {
		if typ.Kind == TypeStruct || typ.Kind == TypeUnion {
			want++
		}
	}
	for _, target := range LinuxTargets {
		layouts, err := InspectRecordLayouts(context.Background(), "clang", header, filepath.Dir(header), target, model)
		if err != nil {
			t.Fatal(err)
		}
		if len(layouts) != want {
			t.Errorf("%s layouts = %d, want %d", target.GOARCH, len(layouts), want)
		}
		for _, cName := range []string{"ghostty_string_s", "ghostty_action_move_tab_s", "ghostty_action_search_selected_s"} {
			layout := layouts[cName]
			if layout.Size != 24 && cName == "ghostty_string_s" {
				t.Errorf("%s %s size = %d, want 24", target.GOARCH, cName, layout.Size)
			}
			if layout.Size != 8 && cName != "ghostty_string_s" {
				t.Errorf("%s %s size = %d, want 8", target.GOARCH, cName, layout.Size)
			}
			if layout.Align != 8 {
				t.Errorf("%s %s align = %d, want 8", target.GOARCH, cName, layout.Align)
			}
		}
	}
}

func TestRecordLayoutsRejectIncomplete(t *testing.T) {
	model := Model{Types: []TypeDecl{{CName: "ghostty_missing_s", Kind: TypeStruct, Fields: []Field{{CName: "value"}}}}}
	_, err := normalizeRecordLayouts(model, map[string]RecordLayout{})
	if err == nil || !strings.Contains(err.Error(), "missing record layout: ghostty_missing_s") {
		t.Fatalf("error = %v", err)
	}
}

func TestAddRecordNameUsesFallbackLocationLine(t *testing.T) {
	record := &astNode{Loc: astLoc{Line: 99}}
	record.Range.Begin.SpellingLoc = &astLoc{File: "/tmp/expanded.h", Line: 12}
	names := map[string]string{}
	if err := addRecordName(names, record, "ghostty_value_s", "/tmp/header.h"); err != nil {
		t.Fatal(err)
	}
	if got := names["/tmp/expanded.h:12"]; got != "ghostty_value_s" {
		t.Fatalf("record name = %q, want fallback location line", got)
	}
}

func TestGoNames(t *testing.T) {
	model := Model{
		Types: []TypeDecl{
			{CName: "ghostty_surface_config_s", Kind: TypeStruct},
			{CName: "ghostty_config_t", Kind: TypeAlias, Type: TypeRef{CName: "void", Pointers: 1}},
		},
		Functions: []FunctionDecl{{CName: "ghostty_config_new"}},
	}
	names, err := AssignGoNames(model)
	if err != nil {
		t.Fatal(err)
	}
	for cName, want := range map[string]string{
		"ghostty_surface_config_s": "SurfaceConfig",
		"ghostty_config_t":         "ConfigHandle",
		"ghostty_config_new":       "ConfigNew",
	} {
		if got := names[cName]; got != want {
			t.Errorf("%s = %q, want %q", cName, got, want)
		}
	}
	if got := GoIdentifier("ghostty_ipc_url_t"); got != "IPCURL" {
		t.Errorf("initialisms = %q, want IPCURL", got)
	}
}

func TestGoNamesRejectCollisions(t *testing.T) {
	_, err := AssignGoNames(Model{Types: []TypeDecl{{CName: "ghostty_config_s", Kind: TypeStruct}, {CName: "ghostty_config", Kind: TypeStruct}}})
	if err == nil || !strings.Contains(err.Error(), "ghostty_config_s") || !strings.Contains(err.Error(), "ghostty_config") {
		t.Fatalf("collision error = %v", err)
	}
}

func TestUnionLayout(t *testing.T) {
	for _, test := range []struct {
		layout RecordLayout
		want   string
	}{
		{RecordLayout{CName: "four", Size: 4, Align: 4}, "uint32"},
		{RecordLayout{CName: "eight", Size: 16, Align: 8}, "uint64"},
	} {
		storage, err := UnionStorageFor(test.layout)
		if err != nil {
			t.Fatal(err)
		}
		if storage.Size != test.layout.Size || storage.Align != test.layout.Align || storage.AlignmentGoType != test.want {
			t.Errorf("UnionStorageFor(%#v) = %#v", test.layout, storage)
		}
	}

	amd64 := map[string]RecordLayout{"ghostty_value_u": {CName: "ghostty_value_u", Size: 16, Align: 8}}
	arm64 := map[string]RecordLayout{"ghostty_value_u": {CName: "ghostty_value_u", Size: 16, Align: 8}}
	split, err := RequiresArchSplit(map[string]map[string]RecordLayout{"amd64": amd64, "arm64": arm64}, "ghostty_value_u")
	if err != nil || split {
		t.Fatalf("identical layouts split = %v, %v", split, err)
	}
	arm64["ghostty_value_u"] = RecordLayout{CName: "ghostty_value_u", Size: 24, Align: 8}
	split, err = RequiresArchSplit(map[string]map[string]RecordLayout{"amd64": amd64, "arm64": arm64}, "ghostty_value_u")
	if err != nil || !split {
		t.Fatalf("different layouts split = %v, %v", split, err)
	}
}

func fieldOffset(layout RecordLayout, name string) int {
	for _, field := range layout.Fields {
		if field.CName == name {
			return field.Offset
		}
	}
	return -1
}
