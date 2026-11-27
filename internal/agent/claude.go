package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	claudeHint              = "重新打开 Claude Code 后，主模型、opus、sonnet、haiku、fable 档位和 /model 列表都会走网关。基地址与凭据写在 settings.json，settings 里的云厂商开关和 ANTHROPIC_MODEL 会清掉，断开时按接入前的值恢复。"
	claudeOneMillionContext = 1_000_000
	claudePickerField       = "modelPicker"
	claudePickerDescription = "via AIGate"
)

var claudeTiers = []string{"opus", "sonnet", "haiku", "fable"}

var claudeEnvKeys = []string{
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"ANTHROPIC_DEFAULT_FABLE_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL",
	"CLAUDE_CODE_SUBAGENT_MODEL",
	"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY",
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_VERTEX",
	"CLAUDE_CODE_USE_FOUNDRY",
	"CLAUDE_CODE_USE_ANTHROPIC_AWS",
	"CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD",
	"CLAUDE_CODE_USE_MANTLE",
}

// Claude Code treats ANTHROPIC_MODEL as a higher-priority setting than the
// settings model and /model. Connect removes it so the gateway model can be
// selected; disconnect puts the user's key back when it is still untouched.
var claudeRemovedEnvKeys = []string{"ANTHROPIC_MODEL"}

var claudeCloudEnvKeys = []string{
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_VERTEX",
	"CLAUDE_CODE_USE_FOUNDRY",
	"CLAUDE_CODE_USE_ANTHROPIC_AWS",
	"CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD",
	"CLAUDE_CODE_USE_MANTLE",
}

type claudeApp struct{}

func (claudeApp) id() string { return "claude" }

func claudeDir(m Machine) string {
	if dir := strings.TrimSpace(m.ClaudeConfigDir); dir != "" {
		return dir
	}
	return filepath.Join(m.Home, ".claude")
}

func claudePath(m Machine) string {
	return filepath.Join(claudeDir(m), "settings.json")
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
	if have {
		if claudeTierWired(rec.Written, got) || claudePickerWired(rec.Written, got) {
			got["model"] = rec.Written["model"]
		}
	}
	if state := wiredState(have, rec, got, gw.Origin); state != "" {
		if state == "on" && !claudeManagedEnvIntact(doc, rec.Written) {
			state = "drift"
		}
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
	snapshot := snapClaude(doc, env, existed)
	fields := rec.Fields
	if !have {
		fields = snapshot
	} else {
		for key, value := range snapshot {
			if _, ok := fields[key]; !ok {
				fields[key] = value
			}
		}
	}
	if env == nil {
		env = map[string]any{}
		doc["env"] = env
	}
	values, main := claudeGatewayEnv(gw, models, asString(doc["model"]))
	picker, pickerJSON, err := claudePickerBlock(doc, models, main)
	if err != nil {
		return err
	}
	for key, value := range values {
		env[key] = value
	}
	for _, key := range claudeRemovedEnvKeys {
		delete(env, key)
	}
	doc["model"] = claudeWireID(main)
	doc[claudePickerField] = picker
	body, err := marshalJSON(doc)
	if err != nil {
		return err
	}
	written := make(map[string]string, len(values)+len(claudeRemovedEnvKeys)+2)
	for key, value := range values {
		written[key] = value
	}
	for _, key := range claudeRemovedEnvKeys {
		written[key] = ""
	}
	written["model"] = claudeWireID(main)
	written[claudePickerField] = pickerJSON
	next := record{Fields: fields, Written: written}
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
	if env != nil {
		for _, key := range claudeEnvKeys {
			written, managed := rec.Written[key]
			if !managed {
				continue
			}
			current, present, err := stringField(env, key)
			if err != nil {
				return err
			}
			if written == "" {
				if present && current != "" {
					continue
				}
			} else if !present || current != written {
				continue
			}
			restoreString(env, key, rec.Fields["env."+key])
		}
		if len(env) == 0 && !rec.Fields["env"].Present {
			delete(doc, "env")
		} else {
			doc["env"] = env
		}
	}
	if written, managed := rec.Written["model"]; managed {
		current, present, err := stringField(doc, "model")
		if err != nil {
			return err
		}
		unchanged := present == (written != "") && (written == "" || current == written)
		if unchanged {
			restoreString(doc, "model", rec.Fields["model"])
		}
	}
	if written, managed := rec.Written[claudePickerField]; managed && claudePickerJSON(doc) == written {
		if original, ok := rec.Fields[claudePickerField]; ok && original.Present {
			decoder := json.NewDecoder(strings.NewReader(original.Value))
			decoder.UseNumber()
			var value any
			if decoder.Decode(&value) != nil {
				return fail("unknown")
			}
			doc[claudePickerField] = value
		} else {
			delete(doc, claudePickerField)
		}
	}
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
	if value, ok := doc[claudePickerField]; ok && value != nil {
		fields[claudePickerField] = field{Present: true, Value: claudePickerJSON(doc)}
	} else {
		fields[claudePickerField] = field{}
	}
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
	got[claudePickerField] = claudePickerJSON(doc)
	return got
}

// claudeTierWired reports whether the top-level model is a tier name whose
// recorded gateway model is still intact. Claude Code's /model fable saves
// "fable" even though the request uses ANTHROPIC_DEFAULT_FABLE_MODEL.
func claudeTierWired(written, got map[string]string) bool {
	if written["model"] == "" {
		return false
	}
	tier := strings.ToLower(strings.TrimSpace(stripClaudeOneMillion(got["model"])))
	switch tier {
	case "opus", "sonnet", "haiku", "fable":
	default:
		return false
	}
	key := "ANTHROPIC_DEFAULT_" + strings.ToUpper(tier) + "_MODEL"
	want := written[key]
	return want != "" && got[key] == want
}

// claudePickerWired reports whether /model saved another model from the
// picker AIGate wrote. Claude Code changes the top-level model but not the
// picker, so that choice is still this connection, not a broken config.
func claudePickerWired(written, got map[string]string) bool {
	current := strings.TrimSpace(got["model"])
	if current == "" {
		return false
	}
	var picker struct {
		Options []struct {
			Model       string `json:"model"`
			Description string `json:"description"`
		} `json:"options"`
	}
	if json.Unmarshal([]byte(written[claudePickerField]), &picker) != nil {
		return false
	}
	for _, option := range picker.Options {
		if option.Description != claudePickerDescription {
			continue
		}
		left := stripClaudeOneMillion(strings.TrimSpace(option.Model))
		right := stripClaudeOneMillion(current)
		if left != "" && left == right {
			return true
		}
	}
	return false
}

func claudeManagedEnvIntact(doc map[string]any, written map[string]string) bool {
	env, err := childObject(doc, "env")
	if err != nil {
		return false
	}
	for key, want := range written {
		if key == "model" || key == claudePickerField {
			continue
		}
		if claudeEnvRemoved(key) {
			if env != nil {
				if _, ok := env[key]; ok {
					return false
				}
			}
			continue
		}
		if env == nil {
			return false
		}
		got, present, err := stringField(env, key)
		if err != nil || !present || got != want {
			return false
		}
	}
	return true
}

func claudeEnvRemoved(key string) bool {
	for _, removed := range claudeRemovedEnvKeys {
		if key == removed {
			return true
		}
	}
	return false
}

func claudeGatewayEnv(gw Target, models []Model, current string) (map[string]string, Model) {
	main := claudePickModel(models, current)
	values := map[string]string{
		"ANTHROPIC_BASE_URL":                         gw.Origin,
		"ANTHROPIC_AUTH_TOKEN":                       caller("claude"),
		"ANTHROPIC_API_KEY":                          "",
		"ANTHROPIC_SMALL_FAST_MODEL":                 claudeTierValue(models, main, "haiku"),
		"CLAUDE_CODE_SUBAGENT_MODEL":                 claudeWireID(main),
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
	}
	for _, tier := range claudeTiers {
		values["ANTHROPIC_DEFAULT_"+strings.ToUpper(tier)+"_MODEL"] = claudeTierValue(models, main, tier)
	}
	for _, key := range claudeCloudEnvKeys {
		values[key] = ""
	}
	return values, main
}

func claudePickerJSON(doc map[string]any) string {
	value, ok := doc[claudePickerField]
	if !ok || value == nil {
		return ""
	}
	body, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(body)
}

// claudePickerBlock writes the gateway's route and group models into /model.
// Claude Code's own gateway discovery keeps only ids containing claude or
// anthropic, so without this list an id such as deepseek-v4-pro is callable but
// not selectable. User options stay, and an earlier list from this process is
// replaced.
func claudePickerBlock(doc map[string]any, models []Model, main Model) (map[string]any, string, error) {
	block := map[string]any{}
	if existing, ok := doc[claudePickerField]; ok && existing != nil {
		value, ok := existing.(map[string]any)
		if !ok {
			return nil, "", fail("unknown")
		}
		for key, item := range value {
			block[key] = item
		}
	}
	options, user := []any(nil), []any(nil)
	if existing, ok := block["options"]; ok && existing != nil {
		list, ok := existing.([]any)
		if !ok {
			return nil, "", fail("unknown")
		}
		for _, item := range list {
			option, _ := item.(map[string]any)
			if option != nil && asString(option["description"]) == claudePickerDescription {
				continue
			}
			user = append(user, item)
		}
	}
	seen := map[string]bool{}
	for _, item := range user {
		if option, ok := item.(map[string]any); ok {
			if id := strings.TrimSpace(asString(option["model"])); id != "" {
				seen[id] = true
			}
		}
	}
	add := func(model Model) {
		id := claudeWireID(model)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		label := strings.TrimSpace(model.Name)
		if label == "" {
			label = id
		}
		if label == model.Bare && id != model.Bare {
			label = id
		}
		options = append(options, map[string]any{
			"description": claudePickerDescription,
			"label":       label,
			"model":       id,
		})
	}
	add(main)
	for _, model := range models {
		add(model)
	}
	block["options"] = append(options, user...)
	body, err := json.Marshal(block)
	if err != nil {
		return nil, "", fail("unknown")
	}
	return block, string(body), nil
}

func claudePickModel(models []Model, current string) Model {
	current = stripClaudeOneMillion(strings.TrimSpace(current))
	id := pickID(models, current)
	for _, model := range models {
		if model.ID == id {
			return model
		}
	}
	return Model{ID: id, Bare: id}
}

func claudeTierValue(models []Model, main Model, tier string) string {
	for _, model := range models {
		if claudeHasTier(model, tier) {
			return claudeWireID(model)
		}
	}
	return claudeWireID(main)
}

func claudeHasTier(model Model, tier string) bool {
	tier = strings.ToLower(tier)
	for _, name := range []string{model.ID, model.Bare, model.Name} {
		if strings.Contains(strings.ToLower(name), tier) {
			return true
		}
	}
	return false
}

func claudeWireID(model Model) string {
	id := strings.TrimSpace(model.ID)
	if id == "" || model.Context < claudeOneMillionContext {
		return id
	}
	if strings.HasSuffix(strings.ToLower(id), "[1m]") {
		return id
	}
	return id + "[1m]"
}

func stripClaudeOneMillion(model string) string {
	if len(model) >= 4 && strings.EqualFold(model[len(model)-4:], "[1m]") {
		return strings.TrimSpace(model[:len(model)-4])
	}
	return model
}
