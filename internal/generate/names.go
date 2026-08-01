package generate

import (
	"fmt"
	"sort"
	"strings"
)

var goInitialisms = map[string]string{
	"cli": "CLI",
	"ime": "IME",
	"ipc": "IPC",
	"rgb": "RGB",
	"tty": "TTY",
	"url": "URL",
}

type NameCollisionError struct {
	GoName string
	First  string
	Second string
}

func (e *NameCollisionError) Error() string {
	return fmt.Sprintf("Go name collision %q: %s and %s", e.GoName, e.First, e.Second)
}

// GoIdentifier converts a Ghostty C identifier to its deterministic exported Go name.
func GoIdentifier(cName string) string {
	name := strings.TrimPrefix(strings.TrimPrefix(cName, "ghostty_"), "GHOSTTY_")
	name = strings.ToLower(name)
	parts := strings.Split(name, "_")
	if len(parts) > 1 {
		switch parts[len(parts)-1] {
		case "t", "s", "e", "u":
			parts = parts[:len(parts)-1]
		}
	}
	var result strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		if initialism, ok := goInitialisms[part]; ok {
			result.WriteString(initialism)
			continue
		}
		result.WriteString(strings.ToUpper(part[:1]))
		result.WriteString(part[1:])
	}
	return result.String()
}

// AssignGoNames assigns every package-level declaration before reporting a collision.
func AssignGoNames(model Model) (map[string]string, error) {
	type entry struct{ cName, goName string }
	var entries []entry
	for _, typ := range model.Types {
		name := GoIdentifier(typ.CName)
		if typ.Kind == TypeAlias && typ.Type.CName == "void" && typ.Type.Pointers == 1 {
			name += "Handle"
		}
		entries = append(entries, entry{typ.CName, name})
		for _, value := range typ.EnumValues {
			entries = append(entries, entry{value.CName, GoIdentifier(value.CName)})
		}
	}
	for _, constant := range model.Constants {
		entries = append(entries, entry{constant.CName, GoIdentifier(constant.CName)})
	}
	for _, function := range model.Functions {
		entries = append(entries, entry{function.CName, GoIdentifier(function.CName)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].cName < entries[j].cName })
	names := make(map[string]string, len(entries))
	used := make(map[string]string, len(entries))
	for _, entry := range entries {
		if previous, ok := used[entry.goName]; ok && previous != entry.cName {
			return nil, &NameCollisionError{GoName: entry.goName, First: previous, Second: entry.cName}
		}
		used[entry.goName] = entry.cName
		names[entry.cName] = entry.goName
	}
	return names, nil
}
