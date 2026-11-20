package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const codexHint = "重新打开 Codex 后，对话会发到网关。experimental_bearer_token 写的是本机调用名，不是上游密钥。"

const codexTable = "model_providers.aigate"

type codexApp struct{}

func (codexApp) id() string { return "codex" }

func codexPath(m Machine) string {
	return filepath.Join(m.Home, ".codex", "config.toml")
}

func (codexApp) located(m Machine) (bool, string) {
	path := codexPath(m)
	if exists(path) || m.has("codex") {
		return true, path
	}
	return false, ""
}

type codexDoc struct{ textFile }

func loadCodex(path string) (codexDoc, []byte, bool, error) {
	text, raw, existed, err := readText(path)
	if err != nil {
		return codexDoc{}, raw, existed, err
	}
	if !existed {
		return codexDoc{textFile: splitText("")}, nil, false, nil
	}
	if tomlUnsafe(text) {
		return codexDoc{}, raw, true, fail("unknown")
	}
	doc := codexDoc{textFile: splitText(text)}
	if doc.alien() {
		return codexDoc{}, raw, true, fail("unknown")
	}
	return doc, raw, true, nil
}

func (d codexDoc) alien() bool {
	table := ""
	for _, line := range d.lines {
		name, isTable := tomlTable(line)
		if isTable {
			table = name
			if name != codexTable && strings.Contains(name, "aigate") && strings.Contains(name, "model_providers") {
				return true
			}
			continue
		}
		key, _, ok := tomlAssign(line)
		if ok && table == "" && strings.HasPrefix(key, "model_providers.aigate") {
			return true
		}
	}
	return len(d.top("model")) > 1 || len(d.top("model_provider")) > 1
}

func (d codexDoc) top(key string) []int {
	table := ""
	var idx []int
	for i, line := range d.lines {
		if name, ok := tomlTable(line); ok {
			table = name
			continue
		}
		if table != "" {
			continue
		}
		got, _, ok := tomlAssign(line)
		if ok && got == key {
			idx = append(idx, i)
		}
	}
	return idx
}

func (d codexDoc) firstTable() int {
	for i, line := range d.lines {
		if _, ok := tomlTable(line); ok {
			return i
		}
	}
	return -1
}

func (d codexDoc) stringAt(key string) (string, bool) {
	idx := d.top(key)
	if len(idx) != 1 {
		return "", false
	}
	_, raw, ok := tomlAssign(d.lines[idx[0]])
	if !ok {
		return "", false
	}
	return tomlString(raw)
}

func (d *codexDoc) setTop(key, value string) error {
	quoted, ok := tomlQuote(value)
	if !ok {
		return fail("unknown")
	}
	idx := d.top(key)
	if len(idx) > 1 {
		return fail("unknown")
	}
	line := key + " = " + quoted
	if len(idx) == 1 {
		_, raw, _ := tomlAssign(d.lines[idx[0]])
		if _, ok := tomlString(raw); !ok {
			return fail("unknown")
		}
		d.lines[idx[0]] = line
		return nil
	}
	at := d.firstTable()
	d.lines = insertLine(d.lines, at, line)
	return nil
}

func (d codexDoc) providerSpan() (int, int, bool) {
	for i, line := range d.lines {
		name, ok := tomlTable(line)
		if !ok || name != codexTable {
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

func (d codexDoc) providerString(key string) (string, bool) {
	start, end, ok := d.providerSpan()
	if !ok {
		return "", false
	}
	for _, line := range d.lines[start+1 : end] {
		got, raw, ok := tomlAssign(line)
		if !ok || got != key {
			continue
		}
		return tomlString(raw)
	}
	return "", false
}

func (d *codexDoc) setProvider(base, token string) error {
	b, ok := tomlQuote(base)
	if !ok {
		return fail("unknown")
	}
	t, ok := tomlQuote(token)
	if !ok {
		return fail("unknown")
	}
	block := []string{
		"[" + codexTable + "]",
		`name = "AIGate"`,
		"base_url = " + b,
		`wire_api = "responses"`,
		"experimental_bearer_token = " + t,
	}
	if start, end, ok := d.providerSpan(); ok {
		lines := append([]string{}, d.lines[:start]...)
		lines = append(lines, block...)
		lines = append(lines, d.lines[end:]...)
		d.lines = lines
		return nil
	}
	if len(d.lines) > 0 && strings.TrimSpace(d.lines[len(d.lines)-1]) != "" {
		d.lines = append(d.lines, "")
	}
	d.lines = append(d.lines, block...)
	return nil
}

func (d codexDoc) rawTop(key string) field {
	idx := d.top(key)
	if len(idx) != 1 {
		return field{}
	}
	return field{Present: true, Value: d.lines[idx[0]]}
}

func (d *codexDoc) restoreTop(key string, f field) {
	idx := d.top(key)
	if f.Present {
		if len(idx) == 1 {
			d.lines[idx[0]] = f.Value
			return
		}
		d.lines = insertLine(d.lines, d.firstTable(), f.Value)
		return
	}
	if len(idx) == 1 {
		d.lines = append(d.lines[:idx[0]], d.lines[idx[0]+1:]...)
	}
}

func (d codexDoc) rawProvider() field {
	start, end, ok := d.providerSpan()
	if !ok {
		return field{}
	}
	return field{Present: true, Value: strings.Join(d.lines[start:end], "\n")}
}

func (d *codexDoc) restoreProvider(f field) {
	var block []string
	if f.Present && f.Value != "" {
		block = strings.Split(f.Value, "\n")
	}
	if start, end, ok := d.providerSpan(); ok {
		lines := append([]string{}, d.lines[:start]...)
		lines = append(lines, block...)
		d.lines = append(lines, d.lines[end:]...)
		d.lines = lines
		return
	}
	if len(block) == 0 {
		return
	}
	if len(d.lines) > 0 && strings.TrimSpace(d.lines[len(d.lines)-1]) != "" {
		d.lines = append(d.lines, "")
	}
	d.lines = append(d.lines, block...)
}

func (d codexDoc) blank() bool {
	for _, line := range d.lines {
		if strings.TrimSpace(line) != "" {
			return false
		}
	}
	return true
}

func (d codexDoc) current() map[string]string {
	model, _ := d.stringAt("model")
	provider, _ := d.stringAt("model_provider")
	base, _ := d.providerString("base_url")
	wire, _ := d.providerString("wire_api")
	token, _ := d.providerString("experimental_bearer_token")
	name, _ := d.providerString("name")
	return map[string]string{
		"model":                     model,
		"model_provider":            provider,
		"base_url":                  base,
		"wire_api":                  wire,
		"experimental_bearer_token": token,
		"name":                      name,
	}
}

func insertLine(lines []string, at int, line string) []string {
	if at < 0 || at > len(lines) {
		return append(lines, line)
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:at]...)
	out = append(out, line)
	out = append(out, lines[at:]...)
	return out
}

func (c codexApp) status(m Machine, gw Target) Status {
	st := Status{ID: c.id(), Name: "Codex", Support: "wire"}
	ok, where := c.located(m)
	if !ok {
		st.State = "absent"
		return st
	}
	st.Installed = true
	st.Where = where
	doc, _, _, err := loadCodex(where)
	if err != nil {
		st.State = "unknown"
		return finishNote(st, codexHint)
	}
	rec, have, err := loadRecord(m, c.id())
	if err != nil {
		st.State = "unknown"
		return finishNote(st, codexHint)
	}
	got := doc.current()
	st.Model = got["model"]
	if state := wiredState(have, rec, got, gw.Origin); state != "" {
		st.State = state
		return finishNote(st, codexHint)
	}
	if gw.Origin != "" && got["model_provider"] == "aigate" && sameOrigin(got["base_url"], gw.Origin) && got["experimental_bearer_token"] == caller(c.id()) {
		st.State = "hand"
	} else {
		st.State = "off"
	}
	return finishNote(st, codexHint)
}

func (c codexApp) connect(m Machine, gw Target, models []Model) error {
	if err := requireModels(gw, models); err != nil {
		return err
	}
	ok, _ := c.located(m)
	if !ok {
		return fail("absent")
	}
	path := codexPath(m)
	doc, orig, existed, err := loadCodex(path)
	if err != nil {
		return err
	}
	rec, have, err := loadRecord(m, c.id())
	if err != nil {
		return err
	}
	fields := rec.Fields
	if !have {
		fields = map[string]field{
			"file":           {Present: existed},
			"model":          doc.rawTop("model"),
			"model_provider": doc.rawTop("model_provider"),
			"provider":       doc.rawProvider(),
		}
	}
	current, _ := doc.stringAt("model")
	model := pickID(models, current)
	if err := doc.setTop("model", model); err != nil {
		return err
	}
	if err := doc.setTop("model_provider", "aigate"); err != nil {
		return err
	}
	if err := doc.setProvider(gw.v1(), caller(c.id())); err != nil {
		return err
	}
	next := record{Fields: fields, Written: map[string]string{
		"model":                     model,
		"model_provider":            "aigate",
		"base_url":                  gw.v1(),
		"wire_api":                  "responses",
		"experimental_bearer_token": caller(c.id()),
		"name":                      "AIGate",
	}}
	return commit(m, c.id(), path, existed, orig, []byte(doc.join()), next)
}

func (c codexApp) disconnect(m Machine) error {
	ok, _ := c.located(m)
	if !ok {
		return fail("absent")
	}
	rec, have, err := loadRecord(m, c.id())
	if err != nil {
		return err
	}
	if !have {
		return fail("not_connected")
	}
	path := codexPath(m)
	doc, _, _, err := loadCodex(path)
	if err != nil {
		return err
	}
	doc.restoreTop("model", rec.Fields["model"])
	doc.restoreTop("model_provider", rec.Fields["model_provider"])
	doc.restoreProvider(rec.Fields["provider"])
	if !rec.Fields["file"].Present && doc.blank() {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return removeRecord(m, c.id())
	}
	if err := writeAtomic(path, []byte(doc.join())); err != nil {
		return err
	}
	return removeRecord(m, c.id())
}
