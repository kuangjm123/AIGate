package agent

import "strings"

func readEnvMap(text string, keys []string) (map[string]string, error) {
	got := map[string]string{}
	for _, line := range splitText(text).lines {
		_, key, value, ok := envAssign(line)
		if !ok {
			for _, want := range keys {
				if looksLikeKey(line, want) {
					return nil, fail("unknown")
				}
			}
			continue
		}
		if containsStr(keys, key) {
			got[key] = value
		}
	}
	return got, nil
}

func looksLikeKey(line, key string) bool {
	trim := strings.TrimSpace(line)
	return strings.HasPrefix(trim, key+"=") || strings.HasPrefix(trim, "export "+key+"=")
}

func writeEnv(text string, order []string, kv map[string]string) (string, error) {
	file := splitText(text)
	seen := map[string]bool{}
	for i, line := range file.lines {
		export, key, _, ok := envAssign(line)
		if !ok {
			for want := range kv {
				if looksLikeKey(line, want) {
					return "", fail("unknown")
				}
			}
			continue
		}
		value, want := kv[key]
		if !want {
			continue
		}
		if seen[key] {
			file.lines[i] = ""
			continue
		}
		seen[key] = true
		prefix := ""
		if export {
			prefix = "export "
		}
		file.lines[i] = prefix + key + "=" + quoteDotenv(value)
	}
	for _, key := range order {
		if seen[key] {
			continue
		}
		value, want := kv[key]
		if !want {
			continue
		}
		file.lines = append(file.lines, key+"="+quoteDotenv(value))
	}
	return file.join(), nil
}

func restoreEnv(text string, keys []string, fields map[string]field) (string, error) {
	if _, err := readEnvMap(text, keys); err != nil {
		return "", err
	}
	file := splitText(text)
	seen := map[string]bool{}
	var lines []string
	for _, line := range file.lines {
		_, key, _, ok := envAssign(line)
		if !ok || !containsStr(keys, key) {
			lines = append(lines, line)
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		f := fields["env."+key]
		if !f.Present {
			continue
		}
		lines = append(lines, key+"="+quoteDotenv(f.Value))
	}
	for _, key := range keys {
		if seen[key] || !fields["env."+key].Present {
			continue
		}
		lines = append(lines, key+"="+quoteDotenv(fields["env."+key].Value))
	}
	file.lines = lines
	return file.join(), nil
}

func yamlValues(text string, keys []string) (map[string]string, error) {
	got := map[string]string{}
	lines := splitText(text).lines
	for i, line := range lines {
		if _, owned := indentedOwned(line, keys); owned {
			return nil, fail("unknown")
		}
		key, value, ok := yamlAssign(line)
		if !ok || !containsStr(keys, key) {
			continue
		}
		if value == "" || (i+1 < len(lines) && indented(lines[i+1]) && value == "") {
			return nil, fail("unknown")
		}
		parsed, ok := yamlString(value)
		if !ok {
			return nil, fail("unknown")
		}
		if _, dup := got[key]; dup {
			return nil, fail("unknown")
		}
		got[key] = parsed
	}
	return got, nil
}

func indented(line string) bool {
	return line != "" && (line[0] == ' ' || line[0] == '\t')
}

func indentedOwned(line string, keys []string) (string, bool) {
	if !indented(line) {
		return "", false
	}
	key, _, ok := yamlAssign(strings.TrimLeft(line, " \t"))
	if !ok || !containsStr(keys, key) {
		return "", false
	}
	return key, true
}

func writeYAML(text string, kv map[string]string) (string, map[string]field, error) {
	keys := make([]string, 0, len(kv))
	for _, key := range aiderKeys {
		if _, ok := kv[key]; ok {
			keys = append(keys, key)
		}
	}
	vals, err := yamlValues(text, keys)
	if err != nil {
		return "", nil, err
	}
	prev := map[string]field{}
	for _, key := range keys {
		if value, ok := vals[key]; ok {
			prev[key] = field{Present: true, Value: value}
		} else {
			prev[key] = field{}
		}
	}
	file := splitText(text)
	seen := map[string]bool{}
	for i, line := range file.lines {
		key, _, ok := yamlAssign(line)
		if !ok {
			continue
		}
		value, want := kv[key]
		if !want {
			continue
		}
		seen[key] = true
		file.lines[i] = key + ": " + yamlQuote(value)
	}
	for _, key := range keys {
		if seen[key] {
			continue
		}
		file.lines = append(file.lines, key+": "+yamlQuote(kv[key]))
	}
	return file.join(), prev, nil
}

func restoreYAML(text string, keys []string, fields map[string]field) (string, error) {
	if _, err := yamlValues(text, keys); err != nil {
		return "", err
	}
	file := splitText(text)
	seen := map[string]bool{}
	var lines []string
	for _, line := range file.lines {
		key, _, ok := yamlAssign(line)
		if !ok || !containsStr(keys, key) {
			lines = append(lines, line)
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if !fields[key].Present {
			continue
		}
		lines = append(lines, key+": "+yamlQuote(fields[key].Value))
	}
	for _, key := range keys {
		if seen[key] || !fields[key].Present {
			continue
		}
		lines = append(lines, key+": "+yamlQuote(fields[key].Value))
	}
	file.lines = lines
	return file.join(), nil
}

func containsStr(list []string, key string) bool {
	for _, item := range list {
		if item == key {
			return true
		}
	}
	return false
}
