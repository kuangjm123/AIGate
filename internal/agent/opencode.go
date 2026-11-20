package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const opencodeHint = "重新打开 OpenCode 后，默认模型会走网关。项目里的 opencode.json 可以盖过这份用户配置。"

type opencodeApp struct{}

func (opencodeApp) id() string { return "opencode" }

func opencodeDir(m Machine) string {
	if m.XDGConfigHome != "" {
		return filepath.Join(m.XDGConfigHome, "opencode")
	}
	return filepath.Join(m.Home, ".config", "opencode")
}

func opencodePaths(m Machine) (jsonPath, jsoncPath string) {
	dir := opencodeDir(m)
	return filepath.Join(dir, "opencode.json"), filepath.Join(dir, "opencode.jsonc")
}

func (o opencodeApp) file(m Machine) (string, error) {
	js, jc := opencodePaths(m)
	if exists(js) {
		return js, nil
	}
	if exists(jc) {
		if _, _, _, err := readJSONFile(jc); err != nil {
			return jc, err
		}
		return jc, nil
	}
	return js, nil
}

func (o opencodeApp) located(m Machine) (bool, string) {
	js, jc := opencodePaths(m)
	if exists(js) || exists(jc) || m.has("opencode") {
		path, err := o.file(m)
		if err != nil {
			if exists(jc) {
				return true, jc
			}
			return true, js
		}
		return true, path
	}
	return false, ""
}

type ocEntry struct {
	key   string
	name  string
	apiID string
}

func openCodeEntries(models []Model, current string) ([]ocEntry, string) {
	used := map[string]bool{}
	entries := make([]ocEntry, 0, len(models))
	for i, model := range models {
		key := ""
		if tokenKey(model.Bare) && !used[model.Bare] {
			key = model.Bare
		}
		if key == "" {
			key = "m" + itoa(i+1)
			for used[key] {
				key = key + "x"
			}
		}
		used[key] = true
		name := model.Name
		if name == "" {
			name = model.ID
		}
		entries = append(entries, ocEntry{key: key, name: name, apiID: model.ID})
	}
	def := ""
	if len(entries) > 0 {
		def = "aigate/" + entries[0].key
	}
	if rest, ok := strings.CutPrefix(current, "aigate/"); ok {
		for _, entry := range entries {
			if entry.key == rest {
				def = current
				break
			}
		}
	}
	return entries, def
}

func tokenKey(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func modelSig(entries []ocEntry) string {
	parts := make([]string, len(entries))
	for i, entry := range entries {
		parts[i] = entry.key + "=" + entry.apiID
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func (o opencodeApp) status(m Machine, gw Target) Status {
	st := Status{ID: o.id(), Name: "OpenCode", Support: "wire"}
	ok, where := o.located(m)
	if !ok {
		st.State = "absent"
		return st
	}
	st.Installed = true
	st.Where = where
	doc, _, _, err := readJSONFile(where)
	if err != nil {
		st.State = "unknown"
		return finishNote(st, opencodeHint)
	}
	rec, have, err := loadRecord(m, o.id())
	if err != nil {
		st.State = "unknown"
		return finishNote(st, opencodeHint)
	}
	got := opencodeValues(doc)
	st.Model = got["model"]
	if state := wiredState(have, rec, got, gw.Origin); state != "" {
		st.State = state
		return finishNote(st, opencodeHint)
	}
	if gw.Origin != "" && sameOrigin(got["baseURL"], gw.Origin) && got["apiKey"] == caller(o.id()) {
		st.State = "hand"
	} else {
		st.State = "off"
	}
	return finishNote(st, opencodeHint)
}

func (o opencodeApp) connect(m Machine, gw Target, models []Model) error {
	if err := requireModels(gw, models); err != nil {
		return err
	}
	ok, _ := o.located(m)
	if !ok {
		return fail("absent")
	}
	path, err := o.file(m)
	if err != nil {
		return err
	}
	doc, orig, existed, err := readJSONFile(path)
	if err != nil {
		return err
	}
	if _, _, err := stringField(doc, "model"); err != nil {
		return err
	}
	providers, err := childObject(doc, "providers")
	if err != nil {
		return err
	}
	if providers != nil {
		if v, exists := providers["aigate"]; exists && !isObject(v) {
			return fail("unknown")
		}
	}
	rec, have, err := loadRecord(m, o.id())
	if err != nil {
		return err
	}
	fields := rec.Fields
	if !have {
		fields = snapOpenCode(doc, providers, existed)
	}
	if providers == nil {
		providers = map[string]any{}
		doc["providers"] = providers
	}
	entries, def := openCodeEntries(models, asString(doc["model"]))
	modelsObj := map[string]any{}
	for _, entry := range entries {
		one := map[string]any{"name": entry.name}
		if entry.apiID != entry.key {
			one["modelID"] = entry.apiID
		}
		modelsObj[entry.key] = one
	}
	providers["aigate"] = map[string]any{
		"name":    "AIGate",
		"package": "@opencode/ai/providers/openai-compatible",
		"settings": map[string]any{
			"baseURL": gw.v1(),
			"apiKey":  caller(o.id()),
		},
		"models": modelsObj,
	}
	doc["model"] = def
	body, err := marshalJSON(doc)
	if err != nil {
		return err
	}
	next := record{Fields: fields, Written: map[string]string{
		"baseURL": gw.v1(),
		"apiKey":  caller(o.id()),
		"model":   def,
		"models":  modelSig(entries),
	}}
	return commit(m, o.id(), path, existed, orig, body, next)
}

func (o opencodeApp) disconnect(m Machine) error {
	ok, _ := o.located(m)
	if !ok {
		return fail("absent")
	}
	rec, have, err := loadRecord(m, o.id())
	if err != nil {
		return err
	}
	if !have {
		return fail("not_connected")
	}
	path, err := o.file(m)
	if err != nil {
		return err
	}
	doc, _, _, err := readJSONFile(path)
	if err != nil {
		return err
	}
	providers, err := childObject(doc, "providers")
	if err != nil {
		return err
	}
	if providers == nil {
		providers = map[string]any{}
	}
	if err := restoreRaw(providers, "aigate", rec.Fields["aigate"]); err != nil {
		return err
	}
	if len(providers) == 0 && !rec.Fields["providers"].Present {
		delete(doc, "providers")
	} else {
		doc["providers"] = providers
	}
	restoreString(doc, "model", rec.Fields["model"])
	if !rec.Fields["file"].Present && len(doc) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return removeRecord(m, o.id())
	}
	body, err := marshalJSON(doc)
	if err != nil {
		return err
	}
	if err := writeAtomic(path, body); err != nil {
		return err
	}
	return removeRecord(m, o.id())
}

func snapOpenCode(doc, providers map[string]any, existed bool) map[string]field {
	fields := map[string]field{
		"file":      {Present: existed},
		"providers": {Present: doc["providers"] != nil},
		"model":     snapString(doc, "model"),
	}
	if providers != nil {
		fields["aigate"] = snapRaw(providers, "aigate")
	} else {
		fields["aigate"] = field{}
	}
	return fields
}

func opencodeValues(doc map[string]any) map[string]string {
	got := map[string]string{"model": asString(doc["model"])}
	providers, err := childObject(doc, "providers")
	if err != nil || providers == nil {
		return got
	}
	block, err := childObject(providers, "aigate")
	if err != nil || block == nil {
		return got
	}
	if settings, err := childObject(block, "settings"); err == nil && settings != nil {
		got["baseURL"] = asString(settings["baseURL"])
		got["apiKey"] = asString(settings["apiKey"])
	}
	models, err := childObject(block, "models")
	if err != nil || models == nil {
		got["models"] = ""
		return got
	}
	keys := make([]string, 0, len(models))
	for key := range models {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		apiID := key
		if one, err := childObject(models, key); err == nil && one != nil && asString(one["modelID"]) != "" {
			apiID = asString(one["modelID"])
		}
		parts = append(parts, key+"="+apiID)
	}
	got["models"] = strings.Join(parts, ",")
	return got
}

func isObject(v any) bool {
	_, ok := v.(map[string]any)
	return ok
}

func snapRaw(doc map[string]any, key string) field {
	v, ok := doc[key]
	if !ok {
		return field{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return field{}
	}
	return field{Present: true, Value: string(b)}
}

func restoreRaw(doc map[string]any, key string, f field) error {
	if !f.Present {
		delete(doc, key)
		return nil
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(f.Value))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return fail("unknown")
	}
	doc[key] = v
	return nil
}
