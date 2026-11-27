package agent

import (
	"strings"
)

const (
	codexCatalogName  = "aigate-models.json"
	codexHeadersTable = codexTable + ".http_headers"
	codexActorHeader  = "x-openai-actor-authorization"
	codexActorValue   = "aigate"
)

type inlineMember struct {
	keyRaw string
	key    string
	value  string
}

func (d codexDoc) tableSpan(name string) (int, int, bool) {
	for i, line := range d.lines {
		got, ok := tomlTable(line)
		if !ok || got != name {
			continue
		}
		j := i + 1
		for j < len(d.lines) {
			if _, isTable := tomlTable(d.lines[j]); isTable {
				break
			}
			j++
		}
		return i, j, true
	}
	return 0, 0, false
}

func (d codexDoc) providerSpan() (int, int, bool) {
	return d.tableSpan(codexTable)
}

func (d codexDoc) tableCount(name string) int {
	n := 0
	for _, line := range d.lines {
		if got, ok := tomlTable(line); ok && got == name {
			n++
		}
	}
	return n
}

func (d codexDoc) assignmentIndexes(start, end int, key string) []int {
	var out []int
	for i := start + 1; i < end; i++ {
		got, _, ok := tomlAssign(d.lines[i])
		if ok && got == key {
			out = append(out, i)
		}
	}
	return out
}

func (d codexDoc) providerValues(key string) []string {
	start, end, ok := d.providerSpan()
	if !ok {
		return nil
	}
	var out []string
	for _, i := range d.assignmentIndexes(start, end, key) {
		_, raw, _ := tomlAssign(d.lines[i])
		out = append(out, raw)
	}
	return out
}

func (d codexDoc) providerKeys() []string {
	start, end, ok := d.providerSpan()
	if !ok {
		return nil
	}
	var out []string
	for i := start + 1; i < end; i++ {
		key, _, ok := tomlAssign(d.lines[i])
		if ok {
			out = append(out, key)
		}
	}
	return out
}

func (d codexDoc) providerString(key string) (string, bool) {
	values := d.providerValues(key)
	if len(values) != 1 {
		return "", false
	}
	return tomlString(values[0])
}

func (d codexDoc) providerRaw(key string) (string, bool) {
	values := d.providerValues(key)
	if len(values) != 1 {
		return "", false
	}
	return strings.TrimSpace(values[0]), true
}

// setTableString replaces one string key in an existing table or appends it
// before the next table. The line's indentation and trailing comment stay.
func (d *codexDoc) setTableString(table, key, value string) error {
	quoted, ok := tomlQuote(value)
	if !ok {
		return fail("unknown")
	}
	start, end, found := d.tableSpan(table)
	if !found {
		return fail("unknown")
	}
	idx := d.assignmentIndexes(start, end, key)
	if len(idx) > 1 {
		return fail("unknown")
	}
	if len(idx) == 1 {
		_, raw, _ := tomlAssign(d.lines[idx[0]])
		if _, ok := tomlString(raw); !ok {
			return fail("unknown")
		}
		d.lines[idx[0]] = replaceTomlValue(d.lines[idx[0]], quoted)
		return nil
	}
	d.lines = insertLine(d.lines, end, key+" = "+quoted)
	return nil
}

func (d *codexDoc) removeTableKey(table, key string) error {
	start, end, found := d.tableSpan(table)
	if !found {
		return nil
	}
	idx := d.assignmentIndexes(start, end, key)
	if len(idx) > 1 {
		return fail("unknown")
	}
	if len(idx) == 1 {
		d.lines = append(d.lines[:idx[0]], d.lines[idx[0]+1:]...)
	}
	return nil
}

func (d *codexDoc) ensureProviderTable() error {
	if d.tableCount(codexTable) > 1 {
		return fail("unknown")
	}
	if _, _, ok := d.providerSpan(); ok {
		return nil
	}
	block := []string{
		"[" + codexTable + "]",
	}
	at := len(d.lines)
	if start, _, ok := d.tableSpan(codexHeadersTable); ok {
		at = start
	}
	if at > 0 && strings.TrimSpace(d.lines[at-1]) != "" {
		block = append([]string{""}, block...)
	}
	if at < len(d.lines) && strings.TrimSpace(d.lines[at]) != "" {
		block = append(block, "")
	}
	d.lines = insertLines(d.lines, at, block)
	return nil
}

func (d *codexDoc) setProvider(base, token, defaultToken string, keepToken func(string) bool) (string, error) {
	if err := d.ensureProviderTable(); err != nil {
		return "", err
	}
	if err := d.setTableString(codexTable, "name", "AIGate"); err != nil {
		return "", err
	}
	if err := d.setTableString(codexTable, "base_url", base); err != nil {
		return "", err
	}
	if err := d.setTableString(codexTable, "wire_api", "responses"); err != nil {
		return "", err
	}
	usedToken := token
	if old, ok := d.providerString("experimental_bearer_token"); ok && old != "" && old != defaultToken && keepToken(old) {
		usedToken = old
	}
	if err := d.setTableString(codexTable, "experimental_bearer_token", usedToken); err != nil {
		return "", err
	}
	if raw, ok := d.providerRaw("requires_openai_auth"); ok {
		switch raw {
		case "true":
			if err := d.removeTableKey(codexTable, "requires_openai_auth"); err != nil {
				return "", err
			}
		case "false", `"false"`:
		default:
			return "", fail("unknown")
		}
	}
	if err := d.setProviderHeaders(); err != nil {
		return "", err
	}
	return usedToken, nil
}

// syncRemoteCompaction adds the capability Codex needs to run a background
// compaction it has already been told to use, and takes back one AIGate
// wrote once the feature is off. The feature switch itself stays the user's.
// Existing capabilities, of any shape, are never replaced. owned says a
// previous write by AIGate left the capability, so only the exact value it
// writes is removed.
func (d *codexDoc) syncRemoteCompaction(owned bool) (bool, error) {
	if d.asyncCompactionOn() {
		start, end, ok := d.providerSpan()
		if !ok {
			return false, fail("unknown")
		}
		if len(d.assignmentIndexes(start, end, "capabilities")) > 1 {
			return false, fail("unknown")
		}
		if d.capabilitiesPresent() {
			return owned && d.ownsRemoteCompaction(), nil
		}
		d.lines = insertLine(d.lines, end, `capabilities = { remote_compaction = "v2" }`)
		return true, nil
	}
	if owned && d.ownsRemoteCompaction() {
		return false, d.removeTableKey(codexTable, "capabilities")
	}
	return false, nil
}

// capabilitiesPresent reports any capabilities key, dotted key, or table in
// the AIGate provider table.
func (d codexDoc) capabilitiesPresent() bool {
	start, end, ok := d.providerSpan()
	if !ok {
		return false
	}
	if _, _, ok := d.tableSpan(codexTable + ".capabilities"); ok {
		return true
	}
	for i := start + 1; i < end; i++ {
		key, _, ok := tomlAssign(d.lines[i])
		if !ok {
			continue
		}
		if key == "capabilities" || strings.HasPrefix(key, "capabilities.") {
			return true
		}
	}
	return false
}

// ownsRemoteCompaction reports the exact value AIGate writes and nothing
// else, as one inline capability in the provider table.
func (d codexDoc) ownsRemoteCompaction() bool {
	start, end, ok := d.providerSpan()
	if !ok {
		return false
	}
	idx := d.assignmentIndexes(start, end, "capabilities")
	if len(idx) != 1 {
		return false
	}
	if _, _, ok := d.tableSpan(codexTable + ".capabilities"); ok {
		return false
	}
	for i := start + 1; i < end; i++ {
		key, _, ok := tomlAssign(d.lines[i])
		if ok && strings.HasPrefix(key, "capabilities.") {
			return false
		}
	}
	_, raw, _ := tomlAssign(d.lines[idx[0]])
	members, ok := parseInlineTable(raw)
	if !ok || len(members) != 1 || members[0].key != "remote_compaction" {
		return false
	}
	value, ok := tomlString(members[0].value)
	return ok && value == "v2"
}

func (d codexDoc) asyncCompactionOn() bool {
	table := ""
	for _, line := range d.lines {
		if name, ok := tomlTable(line); ok {
			table = name
			continue
		}
		key, raw, ok := tomlAssign(line)
		if !ok {
			continue
		}
		switch {
		case table == "features" && key == "async_compaction":
			if strings.TrimSpace(raw) == "true" {
				return true
			}
			if members, ok := parseInlineTable(raw); ok && inlineEnabled(members) {
				return true
			}
		case table == "features.async_compaction" && key == "enabled":
			return strings.TrimSpace(raw) == "true"
		case table == "" && key == "features.async_compaction":
			return strings.TrimSpace(raw) == "true"
		case table == "" && key == "features.async_compaction.enabled":
			return strings.TrimSpace(raw) == "true"
		case table == "" && key == "features":
			if members, ok := parseInlineTable(raw); ok {
				for _, member := range members {
					switch member.key {
					case "async_compaction":
						if strings.TrimSpace(member.value) == "true" {
							return true
						}
						if nested, ok := parseInlineTable(member.value); ok && inlineEnabled(nested) {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

func inlineEnabled(members []inlineMember) bool {
	for _, member := range members {
		if member.key == "enabled" && strings.TrimSpace(member.value) == "true" {
			return true
		}
	}
	return false
}

func (d *codexDoc) setProviderHeaders() error {
	if d.tableCount(codexHeadersTable) > 1 {
		return fail("unknown")
	}
	start, end, hasProvider := d.providerSpan()
	if !hasProvider {
		return fail("unknown")
	}
	inline := d.assignmentIndexes(start, end, "http_headers")
	var dotted []int
	for i := start + 1; i < end; i++ {
		key, _, ok := tomlAssign(d.lines[i])
		if ok && strings.HasPrefix(key, "http_headers.") {
			dotted = append(dotted, i)
		}
	}
	_, _, hasTable := d.tableSpan(codexHeadersTable)
	if len(inline) > 1 {
		return fail("unknown")
	}
	if len(inline) == 1 && (hasTable || len(dotted) > 0) {
		return fail("unknown")
	}
	if hasTable && len(dotted) > 0 {
		return fail("unknown")
	}
	switch {
	case hasTable:
		return d.setHeaderTableValue()
	case len(inline) == 1:
		return d.setInlineHeaderValue(inline[0])
	case len(dotted) > 0:
		return d.setDottedHeaderValue(dotted)
	default:
		d.lines = insertLine(d.lines, end, `http_headers = { "`+codexActorHeader+`" = "`+codexActorValue+`" }`)
		return nil
	}
}

func (d *codexDoc) setHeaderTableValue() error {
	start, end, ok := d.tableSpan(codexHeadersTable)
	if !ok {
		return fail("unknown")
	}
	type hit struct {
		at  int
		raw string
	}
	var hits []hit
	for i := start + 1; i < end; i++ {
		key, raw, ok := tomlAnyAssignment(d.lines[i])
		if ok && key == codexActorHeader {
			hits = append(hits, hit{at: i, raw: raw})
		}
	}
	if len(hits) > 1 {
		return fail("unknown")
	}
	quoted, _ := tomlQuote(codexActorValue)
	if len(hits) == 1 {
		if _, ok := tomlString(hits[0].raw); !ok {
			return fail("unknown")
		}
		d.lines[hits[0].at] = replaceTomlValue(d.lines[hits[0].at], quoted)
		return nil
	}
	d.lines = insertLine(d.lines, end, `"`+codexActorHeader+`" = `+quoted)
	return nil
}

func (d *codexDoc) setInlineHeaderValue(at int) error {
	_, raw, ok := tomlAssign(d.lines[at])
	if !ok {
		return fail("unknown")
	}
	members, ok := parseInlineTable(raw)
	if !ok {
		return fail("unknown")
	}
	quoted, _ := tomlQuote(codexActorValue)
	found := false
	for i := range members {
		if members[i].key != codexActorHeader {
			continue
		}
		if found {
			return fail("unknown")
		}
		members[i].value = quoted
		found = true
	}
	if !found {
		members = append(members, inlineMember{keyRaw: `"` + codexActorHeader + `"`, key: codexActorHeader, value: quoted})
	}
	d.lines[at] = replaceTomlValue(d.lines[at], renderInlineTable(members))
	return nil
}

func (d *codexDoc) setDottedHeaderValue(indices []int) error {
	want := "http_headers." + codexActorHeader
	var hit []int
	for _, i := range indices {
		key, raw, ok := tomlAssign(d.lines[i])
		if !ok {
			return fail("unknown")
		}
		if key != want {
			continue
		}
		if _, ok := tomlString(raw); !ok {
			return fail("unknown")
		}
		hit = append(hit, i)
	}
	if len(hit) > 1 {
		return fail("unknown")
	}
	quoted, _ := tomlQuote(codexActorValue)
	if len(hit) == 1 {
		d.lines[hit[0]] = replaceTomlValue(d.lines[hit[0]], quoted)
		return nil
	}
	_, end, ok := d.providerSpan()
	if !ok {
		return fail("unknown")
	}
	d.lines = insertLine(d.lines, end, want+" = "+quoted)
	return nil
}

func (d codexDoc) actorHeader() (string, bool) {
	if _, _, ok := d.tableSpan(codexHeadersTable); ok {
		start, end, _ := d.tableSpan(codexHeadersTable)
		for i := start + 1; i < end; i++ {
			key, raw, ok := tomlAnyAssignment(d.lines[i])
			if ok && key == codexActorHeader {
				return tomlString(raw)
			}
		}
		return "", false
	}
	start, end, ok := d.providerSpan()
	if !ok {
		return "", false
	}
	for i := start + 1; i < end; i++ {
		key, raw, ok := tomlAssign(d.lines[i])
		if !ok {
			continue
		}
		if key == "http_headers" {
			members, ok := parseInlineTable(raw)
			if !ok {
				return "", false
			}
			for _, member := range members {
				if member.key == codexActorHeader {
					return tomlString(member.value)
				}
			}
			return "", false
		}
		if key == "http_headers."+codexActorHeader {
			return tomlString(raw)
		}
	}
	return "", false
}

func parseInlineTable(raw string) ([]inlineMember, bool) {
	s := strings.TrimSpace(raw)
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil, false
	}
	inner := s[1 : len(s)-1]
	parts, ok := splitTopLevel(inner, ',')
	if !ok {
		return nil, false
	}
	var out []inlineMember
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		at, ok := topLevelByte(part, '=')
		if !ok {
			return nil, false
		}
		keyRaw := strings.TrimSpace(part[:at])
		key, ok := tomlKeyName(keyRaw)
		if !ok {
			return nil, false
		}
		value := strings.TrimSpace(part[at+1:])
		if value == "" {
			return nil, false
		}
		out = append(out, inlineMember{keyRaw: keyRaw, key: key, value: value})
	}
	return out, true
}

func renderInlineTable(members []inlineMember) string {
	if len(members) == 0 {
		return "{}"
	}
	parts := make([]string, 0, len(members))
	for _, member := range members {
		parts = append(parts, member.keyRaw+" = "+member.value)
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

func splitTopLevel(s string, sep byte) ([]string, bool) {
	var out []string
	start := 0
	var quote byte
	escaped := false
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if quote == '"' && c == '\\' {
				escaped = true
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '{', '[':
			depth++
		case '}', ']':
			if depth > 0 {
				depth--
			}
		default:
			if c == sep && depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	if quote != 0 || depth != 0 {
		return nil, false
	}
	out = append(out, s[start:])
	return out, true
}

func topLevelByte(s string, want byte) (int, bool) {
	var quote byte
	escaped := false
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if quote == '"' && c == '\\' {
				escaped = true
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '{', '[':
			depth++
		case '}', ']':
			if depth > 0 {
				depth--
			}
		default:
			if c == want && depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

func tomlKeyName(raw string) (string, bool) {
	parts, ok := tomlKeyParts(raw)
	if !ok || len(parts) != 1 || parts[0] == "" {
		return "", false
	}
	return parts[0], true
}

func tomlAnyAssignment(line string) (key, raw string, ok bool) {
	body, ok := stripTOMLComment(line)
	if !ok {
		return "", "", false
	}
	body = strings.TrimSpace(body)
	if body == "" || strings.HasPrefix(body, "[") {
		return "", "", false
	}
	at, ok := topLevelByte(body, '=')
	if !ok {
		return "", "", false
	}
	key, ok = tomlKeyName(strings.TrimSpace(body[:at]))
	if !ok {
		return "", "", false
	}
	raw = strings.TrimSpace(body[at+1:])
	return key, raw, raw != ""
}

func replaceTomlValue(line, value string) string {
	body, ok := stripTOMLComment(line)
	if !ok {
		return line
	}
	at, ok := topLevelByte(body, '=')
	if !ok {
		return line
	}
	key := strings.TrimRight(body[:at], " \t")
	trimmedRight := strings.TrimRight(body, " \t")
	suffix := line[len(trimmedRight):]
	return key + " = " + value + suffix
}

func insertLines(lines []string, at int, add []string) []string {
	if len(add) == 0 {
		return lines
	}
	if at < 0 || at > len(lines) {
		return append(lines, add...)
	}
	out := make([]string, 0, len(lines)+len(add))
	out = append(out, lines[:at]...)
	out = append(out, add...)
	out = append(out, lines[at:]...)
	return out
}
