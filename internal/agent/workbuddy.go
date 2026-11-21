package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const workbuddyHint = "重新打开 WorkBuddy 后，在模型列表里选择新加的模型。原来的模型条目会留在 models.json 里。"

type workbuddyApp struct{}

func (workbuddyApp) id() string { return "workbuddy" }

func workbuddyPath(m Machine) string {
	return filepath.Join(m.Home, ".workbuddy", "models.json")
}

func (w workbuddyApp) located(m Machine) (bool, string) {
	path := workbuddyPath(m)
	if exists(path) || m.has("workbuddy") || m.has("WorkBuddy") || workbuddyExe(m) {
		return true, path
	}
	return false, ""
}

func workbuddyExe(m Machine) bool {
	for _, dir := range m.AppDirs {
		for _, rel := range []string{
			filepath.Join("WorkBuddy", "WorkBuddy.exe"),
			filepath.Join("workbuddy", "WorkBuddy.exe"),
		} {
			if exists(filepath.Join(dir, rel)) {
				return true
			}
		}
	}
	return false
}

type wbDoc struct {
	form   string
	root   map[string]any
	models []map[string]any
}

func loadWorkBuddy(path string) (wbDoc, []byte, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return wbDoc{form: "array"}, nil, false, nil
		}
		return wbDoc{}, nil, false, fail("unknown")
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return wbDoc{form: "array"}, b, true, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return wbDoc{}, b, true, fail("unknown")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return wbDoc{}, b, true, fail("unknown")
	}
	switch doc := v.(type) {
	case []any:
		models, err := wbModels(doc)
		if err != nil {
			return wbDoc{}, b, true, err
		}
		return wbDoc{form: "array", models: models}, b, true, nil
	case map[string]any:
		if err := wbObjectOK(doc); err != nil {
			return wbDoc{}, b, true, err
		}
		var models []map[string]any
		if raw, ok := doc["models"]; ok && raw != nil {
			models, err = wbModels(raw.([]any))
			if err != nil {
				return wbDoc{}, b, true, err
			}
		}
		return wbDoc{form: "object", root: doc, models: models}, b, true, nil
	default:
		return wbDoc{}, b, true, fail("unknown")
	}
}

func wbObjectOK(doc map[string]any) error {
	if raw, ok := doc["models"]; ok && raw != nil {
		if _, ok := raw.([]any); !ok {
			return fail("unknown")
		}
	}
	if raw, ok := doc["availableModels"]; ok && raw != nil {
		arr, ok := raw.([]any)
		if !ok {
			return fail("unknown")
		}
		for _, item := range arr {
			if _, ok := item.(string); !ok {
				return fail("unknown")
			}
		}
	}
	return nil
}

func wbModels(arr []any) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		one, ok := item.(map[string]any)
		if !ok {
			return nil, fail("unknown")
		}
		if _, _, err := stringField(one, "id"); err != nil {
			return nil, err
		}
		if _, _, err := stringField(one, "apiKey"); err != nil {
			return nil, err
		}
		if _, _, err := stringField(one, "url"); err != nil {
			return nil, err
		}
		out = append(out, one)
	}
	return out, nil
}

func wbOwned(entry map[string]any) bool {
	return asString(entry["apiKey"]) == caller("workbuddy")
}

func (d wbDoc) kept() []map[string]any {
	var out []map[string]any
	for _, entry := range d.models {
		if !wbOwned(entry) {
			out = append(out, entry)
		}
	}
	return out
}

func (d wbDoc) owned() []map[string]any {
	var out []map[string]any
	for _, entry := range d.models {
		if wbOwned(entry) {
			out = append(out, entry)
		}
	}
	return out
}

func wbIDs(entries []map[string]any) string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if id := asString(entry["id"]); id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

func (d wbDoc) current(url string) map[string]string {
	owned := d.owned()
	got := map[string]string{
		"apiKey": caller("workbuddy"),
		"url":    "",
		"ids":    wbIDs(owned),
	}
	if len(owned) == 0 {
		got["apiKey"] = ""
		return got
	}
	same := true
	for _, entry := range owned {
		if asString(entry["url"]) != url && url != "" {
			same = false
		}
		if got["url"] == "" {
			got["url"] = asString(entry["url"])
		} else if got["url"] != asString(entry["url"]) {
			same = false
		}
	}
	if !same {
		got["url"] = got["url"] + "\n"
	}
	return got
}

func wbEntry(model Model, endpoint string) map[string]any {
	images := false
	if model.Vision != nil {
		images = *model.Vision
	}
	entry := map[string]any{
		"id":                model.ID,
		"name":              model.Name,
		"vendor":            "Custom",
		"url":               endpoint,
		"apiKey":            caller("workbuddy"),
		"supportsToolCall":  true,
		"supportsImages":    images,
		"supportsReasoning": model.Reasoning,
		"useCustomProtocol": false,
	}
	if model.Context > 0 {
		entry["maxInputTokens"] = model.Context
	}
	return entry
}

func (d *wbDoc) apply(models []Model, endpoint string) error {
	taken := map[string]bool{}
	for _, entry := range d.kept() {
		if id := asString(entry["id"]); id != "" {
			taken[id] = true
		}
	}
	var chosen []Model
	for _, model := range models {
		if model.ID == "" || taken[model.ID] {
			continue
		}
		chosen = append(chosen, model)
		taken[model.ID] = true
	}
	if len(chosen) == 0 {
		return fail("id_taken")
	}
	next := append([]map[string]any{}, d.kept()...)
	ids := make([]string, 0, len(chosen))
	for _, model := range chosen {
		next = append(next, wbEntry(model, endpoint))
		ids = append(ids, model.ID)
	}
	d.models = next
	if d.form == "object" {
		d.root["models"] = mapsToAny(next)
		d.root["availableModels"] = mergeAvailable(d.root["availableModels"], ids)
	}
	return nil
}

func mergeAvailable(existing any, ids []string) []any {
	var out []any
	seen := map[string]bool{}
	if arr, ok := existing.([]any); ok {
		for _, item := range arr {
			s, ok := item.(string)
			if !ok || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func (d wbDoc) bytes() ([]byte, error) {
	if d.form == "object" {
		if d.root == nil {
			d.root = map[string]any{}
		}
		d.root["models"] = mapsToAny(d.models)
		return marshalJSON(d.root)
	}
	if d.models == nil {
		d.models = []map[string]any{}
	}
	return marshalJSON(mapsToAny(d.models))
}

func mapsToAny(in []map[string]any) []any {
	out := make([]any, len(in))
	for i, entry := range in {
		out[i] = entry
	}
	return out
}

func (w workbuddyApp) status(m Machine, gw Target) Status {
	st := Status{ID: w.id(), Name: "WorkBuddy", Support: "wire"}
	ok, where := w.located(m)
	if !ok {
		st.State = "absent"
		return st
	}
	st.Installed = true
	st.Where = where
	doc, _, _, err := loadWorkBuddy(where)
	if err != nil {
		st.State = "unknown"
		return finishNote(st, workbuddyHint)
	}
	rec, have, err := loadRecord(m, w.id())
	if err != nil {
		st.State = "unknown"
		return finishNote(st, workbuddyHint)
	}
	endpoint := ""
	if gw.Origin != "" {
		endpoint = gw.Origin + "/v1/chat/completions"
	}
	got := doc.current(endpoint)
	if len(doc.owned()) > 0 {
		st.Model = asString(doc.owned()[0]["id"])
	}
	if state := wiredState(have, rec, got, gw.Origin); state != "" {
		st.State = state
		return finishNote(st, workbuddyHint)
	}
	if gw.Origin != "" && len(doc.owned()) > 0 && sameOrigin(got["url"], gw.Origin) && !strings.Contains(got["url"], "\n") {
		st.State = "hand"
	} else {
		st.State = "off"
	}
	return finishNote(st, workbuddyHint)
}

func (w workbuddyApp) connect(m Machine, gw Target, models []Model) error {
	if err := requireModels(gw, models); err != nil {
		return err
	}
	ok, _ := w.located(m)
	if !ok {
		return fail("absent")
	}
	path := workbuddyPath(m)
	doc, orig, existed, err := loadWorkBuddy(path)
	if err != nil {
		return err
	}
	rec, have, err := loadRecord(m, w.id())
	if err != nil {
		return err
	}
	fields := rec.Fields
	if !have {
		fields = snapWorkBuddy(doc, existed)
	}
	endpoint := gw.Origin + "/v1/chat/completions"
	if err := doc.apply(models, endpoint); err != nil {
		return err
	}
	body, err := doc.bytes()
	if err != nil {
		return err
	}
	next := record{Fields: fields, Written: map[string]string{
		"url":    endpoint,
		"apiKey": caller(w.id()),
		"ids":    wbIDs(doc.owned()),
	}}
	return commit(m, w.id(), path, existed, orig, body, next)
}

func snapWorkBuddy(doc wbDoc, existed bool) map[string]field {
	raw, _ := json.Marshal(doc.owned())
	fields := map[string]field{
		"file":  {Present: existed},
		"form":  {Present: true, Value: doc.form},
		"owned": {Present: len(doc.owned()) > 0, Value: string(raw)},
	}
	if doc.form == "object" && doc.root != nil {
		fields["availableModels"] = snapRaw(doc.root, "availableModels")
	}
	return fields
}

func (w workbuddyApp) disconnect(m Machine) error {
	ok, _ := w.located(m)
	if !ok {
		return fail("absent")
	}
	rec, have, err := loadRecord(m, w.id())
	if err != nil {
		return err
	}
	if !have {
		return fail("not_connected")
	}
	path := workbuddyPath(m)
	if !exists(path) {
		return removeRecord(m, w.id())
	}
	doc, _, _, err := loadWorkBuddy(path)
	if err != nil {
		return err
	}
	if rec.Fields["form"].Present && rec.Fields["form"].Value != "" && doc.form != rec.Fields["form"].Value {
		return fail("unknown")
	}
	var kept []map[string]any
	for _, entry := range doc.models {
		if !wbOwned(entry) {
			kept = append(kept, entry)
		}
	}
	if rec.Fields["owned"].Present {
		var prior []map[string]any
		if json.Unmarshal([]byte(rec.Fields["owned"].Value), &prior) != nil {
			return fail("unknown")
		}
		kept = append(kept, prior...)
	}
	doc.models = kept
	if doc.form == "object" && doc.root != nil {
		if err := restoreRaw(doc.root, "availableModels", rec.Fields["availableModels"]); err != nil {
			return err
		}
		doc.root["models"] = mapsToAny(doc.models)
	}
	if !rec.Fields["file"].Present && len(doc.models) == 0 && (doc.form == "array" || len(doc.root) == 0 || onlyEmptyModels(doc.root)) {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return removeRecord(m, w.id())
	}
	body, err := doc.bytes()
	if err != nil {
		return err
	}
	if err := writeAtomic(path, body); err != nil {
		return err
	}
	return removeRecord(m, w.id())
}

func onlyEmptyModels(root map[string]any) bool {
	if root == nil {
		return true
	}
	for key, value := range root {
		if key == "models" {
			arr, ok := value.([]any)
			if ok && len(arr) == 0 {
				continue
			}
			if value == nil {
				continue
			}
		}
		return false
	}
	return true
}
