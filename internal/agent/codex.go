package agent

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/kuangjm123/AIGate/internal/codexcat"
)

const codexHint = "重新打开 Codex 后，对话会发到网关。experimental_bearer_token 是网关的调用凭证，不是上游密钥；断开时会保留提供商表。"

const codexTable = "model_providers.aigate"

type codexApp struct{}

func (codexApp) id() string { return "codex" }

func codexPath(m Machine) string {
	return filepath.Join(m.Home, ".codex", "config.toml")
}

func codexCatalogPath(m Machine) string {
	return filepath.Join(filepath.Dir(codexPath(m)), codexCatalogName)
}

func codexCatalogRef(m Machine, path string) string {
	if runtime.GOOS == "windows" {
		return filepath.Base(path)
	}
	return path
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
	topSeen := map[string]int{}
	providerFoo := 0
	headerFoo := 0
	for _, line := range d.lines {
		name, isTable := tomlTable(line)
		if isTable {
			table = name
			switch name {
			case codexTable:
				providerFoo++
				if providerFoo > 1 {
					return true
				}
			case codexHeadersTable:
				headerFoo++
				if headerFoo > 1 {
					return true
				}
			default:
				if strings.HasPrefix(name, "[[") {
					inner := strings.TrimSuffix(strings.TrimPrefix(name, "[["), "]]")
					if inner == codexTable || strings.HasPrefix(inner, codexTable+".") {
						return true
					}
				}
			}
			continue
		}
		key, _, ok := tomlAssign(line)
		if !ok {
			continue
		}
		if table == "model_providers" && (key == "aigate" || strings.HasPrefix(key, "aigate.")) {
			return true
		}
		if table == "" {
			if key == "model_providers" || strings.HasPrefix(key, "model_providers.") {
				return true
			}
			switch key {
			case "model", "model_reasoning_effort", "model_provider", "model_catalog_json", "openai_base_url":
				topSeen[key]++
				if topSeen[key] > 1 {
					return true
				}
			default:
				for _, managed := range []string{"model", "model_reasoning_effort", "model_provider", "model_catalog_json", "openai_base_url"} {
					if strings.HasPrefix(key, managed+".") {
						return true
					}
				}
			}
		}
	}
	if providerFoo == 1 {
		keys := d.providerKeys()
		for _, key := range []string{"name", "base_url", "wire_api", "experimental_bearer_token", "requires_openai_auth", "http_headers"} {
			if len(d.providerValues(key)) > 1 {
				return true
			}
			if key == "http_headers" {
				continue
			}
			for _, got := range keys {
				if strings.HasPrefix(got, key+".") {
					return true
				}
			}
		}
	}
	return false
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
	if len(idx) == 1 {
		_, raw, _ := tomlAssign(d.lines[idx[0]])
		if _, ok := tomlString(raw); !ok {
			return fail("unknown")
		}
		d.lines[idx[0]] = replaceTomlValue(d.lines[idx[0]], quoted)
		return nil
	}
	at := d.firstTable()
	d.lines = insertLine(d.lines, at, key+" = "+quoted)
	return nil
}

func (d *codexDoc) removeTop(key string) {
	idx := d.top(key)
	if len(idx) == 1 {
		d.lines = append(d.lines[:idx[0]], d.lines[idx[0]+1:]...)
	}
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
		if len(idx) > 1 {
			return
		}
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
	effort, _ := d.stringAt("model_reasoning_effort")
	provider, _ := d.stringAt("model_provider")
	catalog, _ := d.stringAt("model_catalog_json")
	openai, _ := d.stringAt("openai_base_url")
	base, _ := d.providerString("base_url")
	wire, _ := d.providerString("wire_api")
	token, _ := d.providerString("experimental_bearer_token")
	name, _ := d.providerString("name")
	requires, _ := d.providerRaw("requires_openai_auth")
	actor, _ := d.actorHeader()
	return map[string]string{
		"model":                          model,
		"model_reasoning_effort":         effort,
		"model_provider":                 provider,
		"model_catalog_json":             catalog,
		"openai_base_url":                openai,
		"base_url":                       base,
		"wire_api":                       wire,
		"experimental_bearer_token":      token,
		"experimental_bearer_token_hash": secretDigest(token),
		"name":                           name,
		"requires_openai_auth":           requires,
		"actor":                          actor,
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
		if state == "on" && (got["model_catalog_json"] == "" || got["actor"] != codexActorValue ||
			codexCatalogMissing(m, got["model_catalog_json"]) || !gw.keepsCredential(got["experimental_bearer_token"])) {
			st.State = "drift"
		}
		return finishNote(st, codexHint)
	}
	if gw.Origin != "" && got["model_provider"] == "aigate" && sameOrigin(got["base_url"], gw.Origin) && gw.keepsCredential(got["experimental_bearer_token"]) {
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
	defaultToken, ok := gw.bearer(c.id())
	if !ok {
		return fail("no_credential")
	}
	installed, _ := c.located(m)
	if !installed {
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
	catalogPath := codexCatalogPath(m)
	catalogOrig, catalogExisted, err := readOptionalFile(catalogPath)
	if err != nil {
		return fail("unknown")
	}
	if catalogExisted && !codexcat.Valid(catalogOrig) {
		return fail("unknown")
	}
	catalogRef := codexCatalogRef(m, catalogPath)
	fields := rec.Fields
	catalogKnown := true
	if !have {
		fields = map[string]field{
			"file":           {Present: existed},
			"model":          doc.rawTop("model"),
			"effort":         doc.rawTop("model_reasoning_effort"),
			"model_provider": doc.rawTop("model_provider"),
			"catalog":        doc.rawTop("model_catalog_json"),
			"openai":         doc.rawTop("openai_base_url"),
			"catalog_file":   {Present: catalogExisted, Value: catalogPath},
		}
	} else {
		catalogKnown = rec.CatalogKnown
		// A record from an earlier AIGate connected Codex without stashing
		// these. The values still in the file are what that record could
		// not name, so keep them for this disconnect.
		if _, ok := fields["effort"]; !ok {
			fields["effort"] = doc.rawTop("model_reasoning_effort")
		}
		if _, ok := fields["catalog"]; !ok {
			fields["catalog"] = doc.rawTop("model_catalog_json")
		}
		if _, ok := fields["catalog_file"]; !ok {
			fields["catalog_file"] = field{Present: catalogExisted, Value: catalogPath}
			if !catalogExisted {
				catalogKnown = true
			}
		}
	}
	current, _ := doc.stringAt("model")
	effort, _ := doc.stringAt("model_reasoning_effort")
	usedToken, err := doc.setProvider(gw.v1(), defaultToken, defaultToken, gw.keepsCredential)
	if err != nil {
		return err
	}
	models = gw.modelsFor(usedToken, models)
	if len(models) == 0 {
		return fail("no_models")
	}
	capability, err := doc.syncRemoteCompaction(rec.Capability)
	if err != nil {
		return err
	}
	model := pickCodexModel(models, current)
	if err := doc.setTop("model_provider", "aigate"); err != nil {
		return err
	}
	if err := doc.setTop("model_catalog_json", catalogRef); err != nil {
		return err
	}
	if err := doc.setTop("model", model); err != nil {
		return err
	}
	wroteOpenAI := false
	if old, ok := doc.stringAt("openai_base_url"); !ok || strings.TrimSpace(old) == "" ||
		sameURL(old, gw.codexURL()) || (have && rec.Written["openai_base_url"] == old) {
		if err := doc.setTop("openai_base_url", gw.codexURL()); err != nil {
			return err
		}
		wroteOpenAI = true
	}
	generated := codexcat.Catalog(codexModels(models), effort, gw.Used)
	catalogNext := codexcat.Keep(catalogOrig, generated)
	if err := writeAtomic(catalogPath, catalogNext); err != nil {
		return fail("unknown")
	}
	written := map[string]string{
		"model":                          model,
		"model_provider":                 "aigate",
		"model_catalog_json":             catalogRef,
		"base_url":                       gw.v1(),
		"wire_api":                       "responses",
		"experimental_bearer_token_hash": secretDigest(usedToken),
		"name":                           "AIGate",
		"actor":                          codexActorValue,
	}
	if wroteOpenAI {
		written["openai_base_url"] = gw.codexURL()
	}
	next := record{
		Fields:        fields,
		Written:       written,
		Catalog:       string(generated),
		CatalogKnown:  catalogKnown,
		CatalogBefore: string(catalogOrig),
		Capability:    capability,
	}
	if have {
		next.CatalogBefore = rec.CatalogBefore
	}
	if err := commit(m, c.id(), path, existed, orig, []byte(doc.join()), next); err != nil {
		restoreFile(catalogPath, catalogExisted, catalogOrig)
		return err
	}
	return nil
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
	doc, configBefore, configExisted, err := loadCodex(path)
	if err != nil {
		return err
	}
	if f, ok := rec.Fields["model"]; ok {
		doc.restoreTop("model", f)
	}
	if f, ok := rec.Fields["model_provider"]; ok {
		doc.restoreTop("model_provider", f)
	}
	if f, ok := rec.Fields["catalog"]; ok {
		doc.restoreTop("model_catalog_json", f)
	}
	if f, ok := rec.Fields["effort"]; ok {
		doc.restoreEffort(f)
	}
	if rec.Capability && !doc.asyncCompactionOn() && doc.ownsRemoteCompaction() {
		if err := doc.removeTableKey(codexTable, "capabilities"); err != nil {
			return err
		}
	}
	if written := rec.Written["openai_base_url"]; written != "" {
		if current, ok := doc.stringAt("openai_base_url"); ok && current == written {
			if f, known := rec.Fields["openai"]; known {
				doc.restoreTop("openai_base_url", f)
			}
		}
	}
	catalogPath := codexCatalogPath(m)
	catalogNow, catalogExisted, err := readOptionalFile(catalogPath)
	if err != nil {
		return fail("unknown")
	}
	catalogAction := codexCatalogAction(rec, catalogNow, catalogExisted)
	fileField, haveFile := rec.Fields["file"]
	removeConfig := haveFile && !fileField.Present && doc.blank()
	configChanged := false
	if removeConfig {
		if configExisted {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			configChanged = true
		}
	} else if !configExisted || !bytes.Equal(configBefore, []byte(doc.join())) {
		if err := writeAtomic(path, []byte(doc.join())); err != nil {
			return err
		}
		configChanged = true
	}
	if catalogAction.apply(catalogPath) != nil {
		if configChanged {
			_ = restoreFileState(path, configExisted, configBefore)
		}
		return fail("unknown")
	}
	if err := removeRecord(m, c.id()); err != nil {
		if catalogAction.changed() {
			_ = restoreFileState(catalogPath, catalogExisted, catalogNow)
		}
		if configChanged {
			_ = restoreFileState(path, configExisted, configBefore)
		}
		return err
	}
	return nil
}

// SyncCodex repairs the catalog reference after a failed connection or a
// manual edit. It runs from the Agent page's refresh. A missing catalog is
// rewritten only while AIGate is still the provider. Once the user moved
// Codex to another provider, a key that names AIGate's generated catalog is
// removed so a deleted file no longer keeps Codex from loading.
func SyncCodex(m Machine) error {
	wireMu.Lock()
	defer wireMu.Unlock()
	return (codexApp{}).sync(m)
}

func (codexApp) sync(m Machine) error {
	path := codexPath(m)
	doc, orig, existed, err := loadCodex(path)
	if err != nil || !existed {
		return err
	}
	rec, have, err := loadRecord(m, "codex")
	if err != nil {
		return err
	}
	provider, _ := doc.stringAt("model_provider")
	ref, _ := doc.stringAt("model_catalog_json")
	ours := codexCatalogIsOurs(m, ref)
	if provider != "aigate" {
		if !ours {
			return nil
		}
		doc.removeTop("model_catalog_json")
		if err := writeAtomic(path, []byte(doc.join())); err != nil {
			return fail("unknown")
		}
		return nil
	}
	if !have || rec.Catalog == "" {
		return nil
	}
	catalogPath := codexCatalogPath(m)
	wroteCatalog := false
	if ref == "" {
		ref = codexCatalogRef(m, catalogPath)
		if err := doc.setTop("model_catalog_json", ref); err != nil {
			return err
		}
	}
	if codexCatalogIsOurs(m, ref) {
		if _, err := os.Stat(catalogPath); errors.Is(err, os.ErrNotExist) {
			if err := writeAtomic(catalogPath, []byte(rec.Catalog)); err != nil {
				return fail("unknown")
			}
			wroteCatalog = true
		} else if err != nil {
			return fail("unknown")
		}
	}
	if !bytes.Equal(orig, []byte(doc.join())) {
		if err := writeAtomic(path, []byte(doc.join())); err != nil {
			if wroteCatalog {
				_ = os.Remove(catalogPath)
			}
			return fail("unknown")
		}
	}
	return nil
}

type codexCatalogChange struct {
	remove bool
	write  bool
	body   []byte
}

func (c codexCatalogChange) changed() bool {
	return c.remove || c.write
}

func (c codexCatalogChange) apply(path string) error {
	switch {
	case c.remove:
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	case c.write:
		return writeAtomic(path, c.body)
	}
	return nil
}

// codexCatalogAction decides whether disconnect may put the pre-connect
// catalog back. It touches the generated file only while the file still
// equals what connect wrote, so a file the user changed stays theirs.
func codexCatalogAction(rec record, current []byte, existed bool) codexCatalogChange {
	if !existed || rec.Catalog == "" || !rec.CatalogKnown {
		return codexCatalogChange{}
	}
	if !codexcat.Valid(current) {
		return codexCatalogChange{}
	}
	original, known := rec.Fields["catalog_file"]
	if !known {
		return codexCatalogChange{}
	}
	expected := []byte(rec.Catalog)
	if original.Present {
		if rec.CatalogBefore == "" {
			return codexCatalogChange{}
		}
		expected = codexcat.Keep([]byte(rec.CatalogBefore), expected)
	}
	if !bytes.Equal(current, expected) {
		return codexCatalogChange{}
	}
	if original.Present {
		return codexCatalogChange{write: true, body: []byte(rec.CatalogBefore)}
	}
	return codexCatalogChange{remove: true}
}

func pickCodexModel(models []Model, current string) string {
	current = strings.TrimSpace(current)
	for _, model := range models {
		if model.ID == current {
			return model.ID
		}
	}
	if id, ok := strings.CutPrefix(current, "group/"); ok {
		for _, model := range models {
			if model.ID == "group/"+id {
				return model.ID
			}
		}
	}
	for _, model := range models {
		if model.Bare == current && model.ID == "group/"+current {
			return model.ID
		}
	}
	if len(models) == 0 {
		return ""
	}
	return models[0].ID
}

func codexModels(models []Model) []codexcat.Model {
	out := make([]codexcat.Model, 0, len(models))
	for _, model := range models {
		out = append(out, codexcat.Model{
			ID: model.ID, Name: model.Name, Context: model.Context,
			Reasoning: model.Reasoning, ReasoningEfforts: model.ReasoningEfforts,
			ReasoningEffortsKnown: model.ReasoningEffortsKnown, Vision: model.Vision,
		})
	}
	return out
}

func codexCatalogMissing(m Machine, ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	path := ref
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(codexPath(m)), path)
	}
	_, err := os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}

func codexCatalogIsOurs(m Machine, ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	path := ref
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(codexPath(m)), path)
	}
	path = filepath.Clean(path)
	want := filepath.Clean(codexCatalogPath(m))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(path, want)
	}
	return path == want
}

// restoreEffort puts back the effort recorded before connect only when the
// file has none. Connect does not write the effort, so a value set since is
// the user's and stays.
func (d *codexDoc) restoreEffort(f field) {
	if current, ok := d.stringAt("model_reasoning_effort"); ok && strings.TrimSpace(current) != "" {
		return
	}
	d.restoreTop("model_reasoning_effort", f)
}

func sameURL(a, b string) bool {
	a = strings.TrimRight(strings.TrimSpace(a), "/")
	b = strings.TrimRight(strings.TrimSpace(b), "/")
	ua, err := urlParse(a)
	ub, err2 := urlParse(b)
	if err != nil || err2 != nil || ua.Scheme == "" || ua.Host == "" || ub.Scheme == "" || ub.Host == "" {
		return false
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) &&
		strings.EqualFold(ua.Host, ub.Host) &&
		ua.EscapedPath() == ub.EscapedPath() &&
		ua.RawQuery == ub.RawQuery
}
