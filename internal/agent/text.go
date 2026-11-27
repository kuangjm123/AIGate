package agent

import (
	"errors"
	"os"
	"strings"
	"unicode"
)

type textFile struct {
	lines    []string
	nl       string
	trailing bool
}

func splitText(s string) textFile {
	nl := "\n"
	if strings.Contains(s, "\r\n") {
		nl = "\r\n"
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	trailing := strings.HasSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return textFile{nl: nl, trailing: trailing}
	}
	return textFile{lines: strings.Split(s, "\n"), nl: nl, trailing: true}
}

func (f textFile) join() string {
	if len(f.lines) == 0 {
		if f.trailing {
			return f.nl
		}
		return ""
	}
	return strings.Join(f.lines, f.nl) + f.nl
}

func readText(path string) (string, []byte, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, false, nil
		}
		return "", nil, false, fail("unknown")
	}
	if strings.HasPrefix(string(b), "\uFEFF") {
		return "", b, true, fail("unknown")
	}
	return string(b), b, true, nil
}

func stripTOMLComment(line string) (string, bool) {
	in := byte(0)
	esc := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if in != 0 {
			if esc {
				esc = false
				continue
			}
			if c == '\\' && in == '"' {
				esc = true
				continue
			}
			if c == in {
				in = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			in = c
			continue
		}
		if c == '#' {
			return line[:i], true
		}
	}
	return line, in == 0
}

func tomlUnsafe(text string) bool {
	if strings.Contains(text, `"""`) || strings.Contains(text, "'''") {
		return true
	}
	file := splitText(text)
	for _, line := range file.lines {
		if _, ok := stripTOMLComment(line); !ok {
			return true
		}
	}
	return false
}

func tomlTable(line string) (string, bool) {
	body, ok := stripTOMLComment(line)
	if !ok {
		return "", false
	}
	s := strings.TrimSpace(body)
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		return "", false
	}
	if strings.HasPrefix(s, "[[") {
		if len(s) < 4 || !strings.HasSuffix(s, "]]") {
			return s, true
		}
		if parts, ok := tomlKeyParts(s[2 : len(s)-2]); ok && len(parts) > 0 {
			return "[[" + strings.Join(parts, ".") + "]]", true
		}
		return s, true
	}
	if parts, ok := tomlKeyParts(s[1 : len(s)-1]); ok && len(parts) > 0 {
		return strings.Join(parts, "."), true
	}
	return strings.TrimSpace(s[1 : len(s)-1]), true
}

func tomlAssign(line string) (string, string, bool) {
	body, ok := stripTOMLComment(line)
	if !ok {
		return "", "", false
	}
	raw := strings.TrimSpace(body)
	if raw == "" || strings.HasPrefix(raw, "[") {
		return "", "", false
	}
	at, found := topLevelByte(raw, '=')
	if !found {
		return "", "", false
	}
	parts, ok := tomlKeyParts(strings.TrimSpace(raw[:at]))
	if !ok || len(parts) == 0 || parts[0] == "" {
		return "", "", false
	}
	return strings.Join(parts, "."), strings.TrimSpace(raw[at+1:]), true
}

func tomlKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func tomlKeyParts(raw string) ([]string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, false
	}
	var parts []string
	for {
		s = strings.TrimLeft(s, " \t")
		if s == "" {
			return nil, false
		}
		if s[0] == '"' || s[0] == '\'' {
			end, ok := tomlKeyEnd(s)
			if !ok {
				return nil, false
			}
			name, ok := tomlString(s[:end])
			if !ok {
				return nil, false
			}
			parts = append(parts, name)
			s = strings.TrimLeft(s[end:], " \t")
		} else {
			i := 0
			for i < len(s) && tomlBareKeyByte(s[i]) {
				i++
			}
			if i == 0 {
				return nil, false
			}
			parts = append(parts, s[:i])
			s = strings.TrimLeft(s[i:], " \t")
		}
		if s == "" {
			break
		}
		if s[0] != '.' {
			return nil, false
		}
		s = s[1:]
		if strings.TrimSpace(s) == "" {
			return nil, false
		}
	}
	return parts, true
}

func tomlKeyEnd(s string) (int, bool) {
	quote := s[0]
	escaped := false
	for i := 1; i < len(s); i++ {
		c := s[i]
		if quote == '"' && escaped {
			escaped = false
			continue
		}
		if quote == '"' && c == '\\' {
			escaped = true
			continue
		}
		if c == quote {
			return i + 1, true
		}
	}
	return 0, false
}

func tomlBareKeyByte(c byte) bool {
	if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
		return true
	}
	return c == '_' || c == '-'
}

func tomlString(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 {
		return "", false
	}
	if raw[0] == '"' && raw[len(raw)-1] == '"' {
		return unescapeTOML(raw[1 : len(raw)-1])
	}
	if raw[0] == '\'' && raw[len(raw)-1] == '\'' && !strings.Contains(raw[1:len(raw)-1], "'") {
		return raw[1 : len(raw)-1], true
	}
	return "", false
}

func unescapeTOML(s string) (string, bool) {
	var b strings.Builder
	esc := false
	for _, r := range s {
		if esc {
			switch r {
			case '\\', '"':
				b.WriteRune(r)
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				return "", false
			}
			esc = false
			continue
		}
		if r == '\\' {
			esc = true
			continue
		}
		if r == '\n' || r == '\r' {
			return "", false
		}
		b.WriteRune(r)
	}
	if esc {
		return "", false
	}
	return b.String(), true
}

func tomlQuote(s string) (string, bool) {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == '\n' || r == '\r' {
				return "", false
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String(), true
}

func yamlAssign(line string) (string, string, bool) {
	if line == "" || line[0] == ' ' || line[0] == '\t' || strings.HasPrefix(strings.TrimSpace(line), "#") {
		return "", "", false
	}
	raw := line
	in := byte(0)
	for i := 0; i < len(line); i++ {
		c := line[i]
		if in != 0 {
			if c == in {
				in = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			in = c
			continue
		}
		if c == '#' {
			raw = line[:i]
			break
		}
	}
	if in != 0 {
		return "", "", false
	}
	key, value, found := strings.Cut(raw, ":")
	if !found {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, " \t") {
		return "", "", false
	}
	return key, strings.TrimSpace(value), true
}

func yamlString(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "|" || raw == ">" || strings.HasPrefix(raw, "|") || strings.HasPrefix(raw, ">") {
		return "", false
	}
	if strings.HasPrefix(raw, "&") || strings.HasPrefix(raw, "*") || strings.HasPrefix(raw, "[") || strings.HasPrefix(raw, "{") {
		return "", false
	}
	if len(raw) >= 2 && ((raw[0] == '"' && raw[len(raw)-1] == '"') || (raw[0] == '\'' && raw[len(raw)-1] == '\'')) {
		inner := raw[1 : len(raw)-1]
		if raw[0] == '"' {
			return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(inner), true
		}
		return strings.ReplaceAll(inner, "''", "'"), true
	}
	return raw, true
}

func yamlQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func envAssign(line string) (export bool, key, value string, ok bool) {
	trim := strings.TrimSpace(line)
	if trim == "" || strings.HasPrefix(trim, "#") {
		return false, "", "", false
	}
	rest := trim
	if strings.HasPrefix(rest, "export ") {
		export = true
		rest = strings.TrimSpace(strings.TrimPrefix(rest, "export "))
	}
	key, value, found := strings.Cut(rest, "=")
	if !found {
		return false, "", "", false
	}
	key = strings.TrimSpace(key)
	if !envKey(key) {
		return false, "", "", false
	}
	return export, key, unquoteEnv(strings.TrimSpace(value)), true
}

func envKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if i == 0 && !unicode.IsLetter(r) && r != '_' {
			return false
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			continue
		}
		return false
	}
	return true
}

func unquoteEnv(raw string) string {
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(raw[1 : len(raw)-1])
	}
	if len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'' {
		return raw[1 : len(raw)-1]
	}
	if i := strings.Index(raw, " #"); i >= 0 {
		return strings.TrimSpace(raw[:i])
	}
	return raw
}
