package generate

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

var goInitialisms = map[string]string{
	"cli": "CLI",
	"ime": "IME",
	"ipc": "IPC",
	"rgb": "RGB",
	"tty": "TTY",
	"url": "URL",
}

var allTypeSuffixes = map[string]bool{"t": true, "s": true, "e": true, "u": true}

type NameCollisionError struct {
	GoName string
	First  string
	Second string
}

func (e *NameCollisionError) Error() string {
	return fmt.Sprintf("Go name collision %q: %s and %s", e.GoName, e.First, e.Second)
}

// GoIdentifier converts a Ghostty C type identity to its deterministic exported Go name.
func GoIdentifier(cName string) string {
	return goIdentifier(cName, allTypeSuffixes)
}

func goTypeIdentifier(cName string, kind TypeKind) string {
	if strings.ContainsRune(cName, '.') {
		return goIdentifier(cName, allTypeSuffixes)
	}
	suffix := map[TypeKind]string{
		TypeAlias:  "t",
		TypeEnum:   "e",
		TypeStruct: "s",
		TypeUnion:  "u",
	}[kind]
	if suffix == "" {
		return goIdentifier(cName, nil)
	}
	return goIdentifier(cName, map[string]bool{suffix: true})
}

func goIdentifier(cName string, suffixes map[string]bool) string {
	name := strings.TrimPrefix(strings.TrimPrefix(cName, "ghostty_"), "GHOSTTY_")
	path := strings.Split(name, ".")
	for i := range path {
		path[i] = strings.ToLower(path[i])
	}
	if len(path) > 0 {
		parts := strings.Split(path[0], "_")
		if len(parts) > 1 && suffixes[parts[len(parts)-1]] {
			path[0] = strings.Join(parts[:len(parts)-1], "_")
		}
	}

	var result strings.Builder
	for _, segment := range path {
		var part strings.Builder
		flush := func() {
			if part.Len() == 0 {
				return
			}
			word := part.String()
			if initialism, ok := goInitialisms[word]; ok {
				result.WriteString(initialism)
			} else {
				runes := []rune(word)
				result.WriteRune(unicode.ToUpper(runes[0]))
				result.WriteString(string(runes[1:]))
			}
			part.Reset()
		}
		for _, r := range segment {
			if r == '_' || !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				flush()
				continue
			}
			part.WriteRune(r)
		}
		flush()
	}
	if result.Len() == 0 {
		return "X"
	}
	first := []rune(result.String())[0]
	if !unicode.IsLetter(first) && first != '_' {
		return "X" + result.String()
	}
	return result.String()
}

func goValueIdentifier(cName string) string {
	return goIdentifier(cName, nil)
}

func typeSuffix(cName string) string {
	for _, suffix := range []string{"_t", "_s", "_e", "_u"} {
		if strings.HasSuffix(cName, suffix) {
			return suffix
		}
	}
	return ""
}

type nameEntry struct {
	cName, goName, fallback string
	isType                  bool
}

// AssignGoNames assigns every package-level declaration before reporting a collision.
func AssignGoNames(model Model) (map[string]string, error) {
	var entries []nameEntry
	for _, typ := range model.Types {
		name := goTypeIdentifier(typ.CName, typ.Kind)
		fallback := goValueIdentifier(typ.CName)
		if typ.Kind == TypeAlias && typ.Type.CName == "void" && typ.Type.Pointers == 1 {
			name += "Handle"
			fallback += "Handle"
		}
		entries = append(entries, nameEntry{cName: typ.CName, goName: name, fallback: fallback, isType: true})
		for _, value := range typ.EnumValues {
			entries = append(entries, nameEntry{cName: value.CName, goName: goValueIdentifier(value.CName)})
		}
	}
	for _, constant := range model.Constants {
		entries = append(entries, nameEntry{cName: constant.CName, goName: goValueIdentifier(constant.CName)})
	}
	for _, function := range model.Functions {
		entries = append(entries, nameEntry{cName: function.CName, goName: goValueIdentifier(function.CName)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].cName < entries[j].cName })

	for {
		groups := make(map[string][]int)
		for i, entry := range entries {
			groups[entry.goName] = append(groups[entry.goName], i)
		}
		changed := false
		groupNames := make([]string, 0, len(groups))
		for name := range groups {
			groupNames = append(groupNames, name)
		}
		sort.Strings(groupNames)
		for _, name := range groupNames {
			indexes := groups[name]
			if sameCNames(entries, indexes) {
				continue
			}
			var types, values []int
			for _, index := range indexes {
				if entries[index].isType {
					types = append(types, index)
				} else {
					values = append(values, index)
				}
			}
			if len(types) == 0 || (len(values) == 0 && !distinctTypeSuffixes(entries, types)) {
				return nil, collisionError(entries, indexes)
			}
			for _, index := range types {
				if entries[index].fallback == entries[index].goName || typeSuffix(entries[index].cName) == "" {
					return nil, collisionError(entries, indexes)
				}
				entries[index].goName = entries[index].fallback
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	names := make(map[string]string, len(entries))
	for _, entry := range entries {
		names[entry.cName] = entry.goName
	}
	return names, nil
}

func sameCNames(entries []nameEntry, indexes []int) bool {
	first := entries[indexes[0]].cName
	for _, index := range indexes[1:] {
		if entries[index].cName != first {
			return false
		}
	}
	return true
}

func distinctTypeSuffixes(entries []nameEntry, indexes []int) bool {
	seen := make(map[string]bool, len(indexes))
	for _, index := range indexes {
		suffix := typeSuffix(entries[index].cName)
		if suffix == "" || seen[suffix] {
			return false
		}
		seen[suffix] = true
	}
	return true
}

func collisionError(entries []nameEntry, indexes []int) error {
	return &NameCollisionError{GoName: entries[indexes[0]].goName, First: entries[indexes[0]].cName, Second: entries[indexes[1]].cName}
}
