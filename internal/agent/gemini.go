package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const geminiHint = "重新打开 Gemini CLI 后，请求会发到网关。项目里的 .env 会盖过用户目录里的这份；环境里已经有的同名变量也不会被改写。"

var geminiEnvKeys = []string{"GOOGLE_GEMINI_BASE_URL", "GEMINI_API_KEY", "GEMINI_MODEL"}

type geminiApp struct{}

func (geminiApp) id() string { return "gemini" }

func geminiEnvPath(m Machine) string {
	return filepath.Join(m.Home, ".gemini", ".env")
}

func geminiSettingsPath(m Machine) string {
	return filepath.Join(m.Home, ".gemini", "settings.json")
}

func (geminiApp) located(m Machine) (bool, string) {
	if m.has("gemini") {
		return true, geminiEnvPath(m)
	}
	return false, ""
}

func (g geminiApp) status(m Machine, gw Target) Status {
	st := Status{ID: g.id(), Name: "Gemini CLI", Support: "wire"}
	ok, where := g.located(m)
	if !ok {
		st.State = "absent"
		return st
	}
	st.Installed = true
	st.Where = where
	got, model, err := geminiRead(m)
	if err != nil {
		st.State = "unknown"
		return finishNote(st, geminiHint)
	}
	st.Model = model
	rec, have, err := loadRecord(m, g.id())
	if err != nil {
		st.State = "unknown"
		return finishNote(st, geminiHint)
	}
	if state := wiredState(have, rec, got, gw.Origin); state != "" {
		st.State = state
		return finishNote(st, geminiHint)
	}
	if gw.Origin != "" && sameOrigin(got["GOOGLE_GEMINI_BASE_URL"], gw.Origin) && got["GEMINI_API_KEY"] == caller(g.id()) {
		st.State = "hand"
	} else {
		st.State = "off"
	}
	return finishNote(st, geminiHint)
}

func (g geminiApp) connect(m Machine, gw Target, models []Model) error {
	if err := requireModels(gw, models); err != nil {
		return err
	}
	ok, _ := g.located(m)
	if !ok {
		return fail("absent")
	}
	envPath := geminiEnvPath(m)
	settingsPath := geminiSettingsPath(m)
	envText, envOrig, envExisted, err := readText(envPath)
	if err != nil {
		return err
	}
	envNow, err := readEnvMap(envText, geminiEnvKeys)
	if err != nil {
		return err
	}
	doc, settingsOrig, settingsExisted, err := readJSONFile(settingsPath)
	if err != nil {
		return err
	}
	current, err := geminiModelName(doc)
	if err != nil {
		return err
	}
	if current == "" {
		current = envNow["GEMINI_MODEL"]
	}
	bare, ok := pickBare(models, current)
	if !ok {
		return fail("no_models")
	}
	rec, have, err := loadRecord(m, g.id())
	if err != nil {
		return err
	}
	fields := rec.Fields
	if !have {
		fields = snapGemini(envText, doc, envExisted, settingsExisted)
	}
	envNext, err := writeEnv(envText, geminiEnvKeys, map[string]string{
		"GOOGLE_GEMINI_BASE_URL": gw.Origin,
		"GEMINI_API_KEY":         caller(g.id()),
		"GEMINI_MODEL":           bare,
	})
	if err != nil {
		return err
	}
	if err := setGeminiModel(doc, bare); err != nil {
		return err
	}
	settingsNext, err := marshalJSON(doc)
	if err != nil {
		return err
	}
	if err := writeAtomic(envPath, []byte(envNext)); err != nil {
		return err
	}
	if err := writeAtomic(settingsPath, settingsNext); err != nil {
		restoreFile(envPath, envExisted, envOrig)
		return err
	}
	next := record{Fields: fields, Written: map[string]string{
		"GOOGLE_GEMINI_BASE_URL": gw.Origin,
		"GEMINI_API_KEY":         caller(g.id()),
		"GEMINI_MODEL":           bare,
		"model.name":             bare,
	}}
	if err := saveRecord(m, g.id(), next); err != nil {
		restoreFile(envPath, envExisted, envOrig)
		restoreFile(settingsPath, settingsExisted, settingsOrig)
		return err
	}
	return nil
}

func (g geminiApp) disconnect(m Machine) error {
	ok, _ := g.located(m)
	if !ok {
		return fail("absent")
	}
	rec, have, err := loadRecord(m, g.id())
	if err != nil {
		return err
	}
	if !have {
		return fail("not_connected")
	}
	envPath := geminiEnvPath(m)
	settingsPath := geminiSettingsPath(m)
	envText, _, envExisted, err := readText(envPath)
	if err != nil {
		return err
	}
	doc, _, settingsExisted, err := readJSONFile(settingsPath)
	if err != nil {
		return err
	}
	envNext, err := restoreEnv(envText, geminiEnvKeys, rec.Fields)
	if err != nil {
		return err
	}
	if err := restoreGeminiModel(doc, rec.Fields); err != nil {
		return err
	}
	if !rec.Fields["settings"].Present && len(doc) == 0 {
		if settingsExisted {
			if err := os.Remove(settingsPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	} else {
		body, err := marshalJSON(doc)
		if err != nil {
			return err
		}
		if err := writeAtomic(settingsPath, body); err != nil {
			return err
		}
	}
	if !rec.Fields["envfile"].Present && strings.TrimSpace(envNext) == "" {
		if envExisted {
			if err := os.Remove(envPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	} else if err := writeAtomic(envPath, []byte(envNext)); err != nil {
		return err
	}
	return removeRecord(m, g.id())
}

func geminiRead(m Machine) (map[string]string, string, error) {
	text, _, _, err := readText(geminiEnvPath(m))
	if err != nil {
		return nil, "", err
	}
	got, err := readEnvMap(text, geminiEnvKeys)
	if err != nil {
		return nil, "", err
	}
	doc, _, _, err := readJSONFile(geminiSettingsPath(m))
	if err != nil {
		return nil, "", err
	}
	name, err := geminiModelName(doc)
	if err != nil {
		return nil, "", err
	}
	if got == nil {
		got = map[string]string{}
	}
	got["model.name"] = name
	if got["GEMINI_MODEL"] == "" {
		return got, name, nil
	}
	return got, got["GEMINI_MODEL"], nil
}

func geminiModelName(doc map[string]any) (string, error) {
	model, err := childObject(doc, "model")
	if err != nil || model == nil {
		return "", err
	}
	name, _, err := stringField(model, "name")
	return name, err
}

func setGeminiModel(doc map[string]any, name string) error {
	model, err := childObject(doc, "model")
	if err != nil {
		return err
	}
	if model == nil {
		model = map[string]any{}
		doc["model"] = model
	}
	model["name"] = name
	return nil
}

func restoreGeminiModel(doc map[string]any, fields map[string]field) error {
	model, err := childObject(doc, "model")
	if err != nil {
		return err
	}
	if model == nil {
		model = map[string]any{}
	}
	restoreString(model, "name", fields["model.name"])
	if len(model) == 0 && !fields["model"].Present {
		delete(doc, "model")
		return nil
	}
	doc["model"] = model
	return nil
}

func snapGemini(envText string, doc map[string]any, envExisted, settingsExisted bool) map[string]field {
	fields := map[string]field{
		"envfile":  {Present: envExisted},
		"settings": {Present: settingsExisted},
		"model":    {Present: doc["model"] != nil},
	}
	got, _ := readEnvMap(envText, geminiEnvKeys)
	for _, key := range geminiEnvKeys {
		if lineHasKey(envText, key) {
			fields["env."+key] = field{Present: true, Value: got[key]}
		} else {
			fields["env."+key] = field{}
		}
	}
	if model, err := childObject(doc, "model"); err == nil && model != nil {
		fields["model.name"] = snapString(model, "name")
	} else {
		fields["model.name"] = field{}
	}
	return fields
}

func lineHasKey(text, key string) bool {
	for _, line := range splitText(text).lines {
		_, got, _, ok := envAssign(line)
		if ok && got == key {
			return true
		}
	}
	return false
}
