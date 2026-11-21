package agent

import (
	"errors"
	"os"
	"path/filepath"
)

const claudeHint = "重新打开 Claude Code 后，对话会发到网关。基地址写在 settings.json 的 env.ANTHROPIC_BASE_URL，密钥写在 ANTHROPIC_AUTH_TOKEN，请求带 Authorization: Bearer。"

var claudeEnvKeys = []string{
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY",
}

type claudeApp struct{}

func (claudeApp) id() string { return "claude" }

func claudePath(m Machine) string {
	return filepath.Join(m.Home, ".claude", "settings.json")
}

func (claudeApp) located(m Machine) (bool, string) {
	path := claudePath(m)
	if exists(path) || m.has("claude") {
		return true, path
	}
	return false, ""
}

func (c claudeApp) status(m Machine, gw Target) Status {
	st := Status{ID: c.id(), Name: "Claude Code", Support: "wire"}
	ok, where := c.located(m)
	if !ok {
		st.State = "absent"
		return st
	}
	st.Installed = true
	st.Where = where
	doc, _, _, err := readJSONFile(where)
	if err != nil {
		st.State = "unknown"
		return finishNote(st, claudeHint)
	}
	rec, have, err := loadRecord(m, c.id())
	if err != nil {
		st.State = "unknown"
		return finishNote(st, claudeHint)
	}
	got := claudeValues(doc)
	st.Model = got["model"]
	if state := wiredState(have, rec, got, gw.Origin); state != "" {
		st.State = state
		return finishNote(st, claudeHint)
	}
	if gw.Origin != "" && sameOrigin(got["ANTHROPIC_BASE_URL"], gw.Origin) && got["ANTHROPIC_AUTH_TOKEN"] == caller(c.id()) {
		st.State = "hand"
	} else {
		st.State = "off"
	}
	return finishNote(st, claudeHint)
}

func (c claudeApp) connect(m Machine, gw Target, models []Model) error {
	if err := requireModels(gw, models); err != nil {
		return err
	}
	ok, _ := c.located(m)
	if !ok {
		return fail("absent")
	}
	path := claudePath(m)
	doc, orig, existed, err := readJSONFile(path)
	if err != nil {
		return err
	}
	env, err := childObject(doc, "env")
	if err != nil {
		return err
	}
	if _, _, err := stringField(doc, "model"); err != nil {
		return err
	}
	if env != nil {
		for _, key := range claudeEnvKeys {
			if _, _, err := stringField(env, key); err != nil {
				return err
			}
		}
	}
	rec, have, err := loadRecord(m, c.id())
	if err != nil {
		return err
	}
	fields := rec.Fields
	if !have {
		fields = snapClaude(doc, env, existed)
	}
	if env == nil {
		env = map[string]any{}
		doc["env"] = env
	}
	model := pickID(models, asString(doc["model"]))
	env["ANTHROPIC_BASE_URL"] = gw.Origin
	env["ANTHROPIC_AUTH_TOKEN"] = caller(c.id())
	env["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"] = "1"
	doc["model"] = model
	body, err := marshalJSON(doc)
	if err != nil {
		return err
	}
	next := record{Fields: fields, Written: map[string]string{
		"ANTHROPIC_BASE_URL":                         gw.Origin,
		"ANTHROPIC_AUTH_TOKEN":                       caller(c.id()),
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
		"model": model,
	}}
	return commit(m, c.id(), path, existed, orig, body, next)
}

func (c claudeApp) disconnect(m Machine) error {
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
	path := claudePath(m)
	doc, _, _, err := readJSONFile(path)
	if err != nil {
		return err
	}
	env, err := childObject(doc, "env")
	if err != nil {
		return err
	}
	if env == nil {
		env = map[string]any{}
	}
	for _, key := range claudeEnvKeys {
		restoreString(env, key, rec.Fields["env."+key])
	}
	if len(env) == 0 && !rec.Fields["env"].Present {
		delete(doc, "env")
	} else {
		doc["env"] = env
	}
	restoreString(doc, "model", rec.Fields["model"])
	if !rec.Fields["file"].Present && len(doc) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return removeRecord(m, c.id())
	}
	body, err := marshalJSON(doc)
	if err != nil {
		return err
	}
	if err := writeAtomic(path, body); err != nil {
		return err
	}
	return removeRecord(m, c.id())
}

func snapClaude(doc, env map[string]any, existed bool) map[string]field {
	fields := map[string]field{
		"file": field{Present: existed},
		"env":  field{Present: doc["env"] != nil},
	}
	if env != nil {
		for _, key := range claudeEnvKeys {
			fields["env."+key] = snapString(env, key)
		}
	} else {
		for _, key := range claudeEnvKeys {
			fields["env."+key] = field{}
		}
	}
	fields["model"] = snapString(doc, "model")
	return fields
}

func claudeValues(doc map[string]any) map[string]string {
	got := map[string]string{}
	env, _ := childObject(doc, "env")
	for _, key := range claudeEnvKeys {
		if env != nil {
			got[key] = asString(env[key])
		}
	}
	got["model"] = asString(doc["model"])
	return got
}
