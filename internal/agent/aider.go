package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const aiderHint = "重新打开 Aider 后，OpenAI 接口会发到网关。仓库里的 .aider.conf.yml 若写了相同的键，会盖过用户目录里的这份。"

var aiderKeys = []string{"openai-api-base", "openai-api-key", "model"}

type aiderApp struct{}

func (aiderApp) id() string { return "aider" }

func aiderPath(m Machine) string {
	return filepath.Join(m.Home, ".aider.conf.yml")
}

func (aiderApp) located(m Machine) (bool, string) {
	path := aiderPath(m)
	if exists(path) || m.has("aider") {
		return true, path
	}
	return false, ""
}

func (a aiderApp) status(m Machine, gw Target) Status {
	st := Status{ID: a.id(), Name: "Aider", Support: "wire"}
	ok, where := a.located(m)
	if !ok {
		st.State = "absent"
		return st
	}
	st.Installed = true
	st.Where = where
	got, err := aiderRead(where)
	if err != nil {
		st.State = "unknown"
		return finishNote(st, aiderHint)
	}
	st.Model = got["model"]
	rec, have, err := loadRecord(m, a.id())
	if err != nil {
		st.State = "unknown"
		return finishNote(st, aiderHint)
	}
	if state := wiredState(have, rec, got, gw.Origin); state != "" {
		st.State = state
		return finishNote(st, aiderHint)
	}
	if gw.Origin != "" && sameOrigin(got["openai-api-base"], gw.Origin) && got["openai-api-key"] == caller(a.id()) {
		st.State = "hand"
	} else {
		st.State = "off"
	}
	return finishNote(st, aiderHint)
}

func (a aiderApp) connect(m Machine, gw Target, models []Model) error {
	if err := requireModels(gw, models); err != nil {
		return err
	}
	ok, _ := a.located(m)
	if !ok {
		return fail("absent")
	}
	path := aiderPath(m)
	text, orig, existed, err := readText(path)
	if err != nil {
		return err
	}
	current, err := aiderRead(path)
	if err != nil {
		return err
	}
	rec, have, err := loadRecord(m, a.id())
	if err != nil {
		return err
	}
	model := pickAider(models, current["model"])
	nextText, prev, err := writeYAML(text, map[string]string{
		"openai-api-base": gw.v1(),
		"openai-api-key":  caller(a.id()),
		"model":           model,
	})
	if err != nil {
		return err
	}
	fields := rec.Fields
	if !have {
		fields = prev
		fields["file"] = field{Present: existed}
	}
	next := record{Fields: fields, Written: map[string]string{
		"openai-api-base": gw.v1(),
		"openai-api-key":  caller(a.id()),
		"model":           model,
	}}
	return commit(m, a.id(), path, existed, orig, []byte(nextText), next)
}

func (a aiderApp) disconnect(m Machine) error {
	ok, _ := a.located(m)
	if !ok {
		return fail("absent")
	}
	rec, have, err := loadRecord(m, a.id())
	if err != nil {
		return err
	}
	if !have {
		return fail("not_connected")
	}
	path := aiderPath(m)
	text, _, existed, err := readText(path)
	if err != nil {
		return err
	}
	next, err := restoreYAML(text, aiderKeys, rec.Fields)
	if err != nil {
		return err
	}
	if !rec.Fields["file"].Present && strings.TrimSpace(next) == "" {
		if existed {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return removeRecord(m, a.id())
	}
	if err := writeAtomic(path, []byte(next)); err != nil {
		return err
	}
	return removeRecord(m, a.id())
}

func pickAider(models []Model, current string) string {
	for _, model := range models {
		if current == "openai/"+model.ID {
			return current
		}
	}
	return "openai/" + models[0].ID
}

func aiderRead(path string) (map[string]string, error) {
	text, _, existed, err := readText(path)
	if err != nil || !existed {
		if err != nil {
			return nil, err
		}
		return map[string]string{}, nil
	}
	return yamlValues(text, aiderKeys)
}

type cursorApp struct{}

func (cursorApp) id() string { return "cursor" }

func (cursorApp) located(m Machine) (bool, string) {
	if m.has("cursor") || m.has("Cursor") {
		return true, "cursor"
	}
	for _, dir := range m.AppDirs {
		for _, rel := range []string{filepath.Join("cursor", "Cursor.exe"), "Cursor.exe", "Cursor.app"} {
			path := filepath.Join(dir, rel)
			if _, err := os.Stat(path); err == nil {
				return true, path
			}
		}
	}
	return false, ""
}

func (c cursorApp) status(m Machine, gw Target) Status {
	st := Status{ID: c.id(), Name: "Cursor", Support: "manual"}
	ok, where := c.located(m)
	if !ok {
		st.State = "absent"
		return st
	}
	st.Installed = true
	st.Where = where
	st.State = "manual"
	if gw.Origin == "" {
		st.Note = "Cursor 已安装。这一版不改 Cursor 的设置。等网关开始监听后，在 Cursor 的 Models → API Keys 里打开 Override OpenAI Base URL。"
		return st
	}
	st.Note = "这一版不改 Cursor 的设置。在 Models → API Keys 里打开 Override OpenAI Base URL，基地址填 " + gw.v1() + "，密钥填 " + caller(c.id()) + "。这只覆盖走 OpenAI 接口的模型。"
	return st
}

func (c cursorApp) connect(m Machine, gw Target, models []Model) error {
	ok, _ := c.located(m)
	if !ok {
		return fail("absent")
	}
	return fail("unsupported")
}

func (c cursorApp) disconnect(m Machine) error {
	ok, _ := c.located(m)
	if !ok {
		return fail("absent")
	}
	return fail("unsupported")
}
