package codexcat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func decode(t *testing.T, b []byte) ([]map[string]any, map[string]any) {
	t.Helper()
	var doc struct {
		Models []map[string]any `json:"models"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("catalog %s: %v", b, err)
	}
	if doc.Models == nil {
		t.Fatalf("models is null: %s", b)
	}
	return doc.Models, nil
}

func TestCatalogUsesCodexModelShape(t *testing.T) {
	no := false
	yes := true
	b := Catalog([]Model{
		{ID: "p/text", Name: "Text", Context: 32000, Reasoning: true, Vision: &no},
		{ID: "group/vision", Name: "Vision", Context: 128000, Vision: &yes},
	}, "high", nil)
	models, _ := decode(t, b)
	if len(models) != 2 {
		t.Fatalf("models %#v", models)
	}
	first := models[0]
	if first["slug"] != "p/text" || first["base_instructions"] == "" || first["shell_type"] != "unified_exec" {
		t.Fatalf("first %#v", first)
	}
	if first["default_reasoning_level"] != "high" {
		t.Fatalf("default effort %#v", first["default_reasoning_level"])
	}
	levels, _ := first["supported_reasoning_levels"].([]any)
	if len(levels) == 0 {
		t.Fatalf("levels %#v", first["supported_reasoning_levels"])
	}
	mods, _ := first["input_modalities"].([]any)
	if len(mods) != 1 || mods[0] != "text" {
		t.Fatalf("modalities %#v", mods)
	}
	if first["supports_reasoning_summaries"] != true {
		t.Fatalf("summaries %#v", first["supports_reasoning_summaries"])
	}
	if first["supports_search_tool"] != false {
		t.Fatalf("search tool %#v", first["supports_search_tool"])
	}
	second := models[1]
	mods, _ = second["input_modalities"].([]any)
	if len(mods) != 2 || mods[1] != "image" {
		t.Fatalf("vision modalities %#v", mods)
	}
	if second["supported_reasoning_levels"] == nil || len(second["supported_reasoning_levels"].([]any)) != 0 {
		t.Fatalf("non-reasoning levels %#v", second["supported_reasoning_levels"])
	}
}

func TestCatalogUsesKnownLevelsAndUltra(t *testing.T) {
	models, _ := decode(t, Catalog([]Model{
		{
			ID: "p/reasoner", Reasoning: true,
			ReasoningEfforts: []string{"low", "high", "max"}, ReasoningEffortsKnown: true,
		},
		{
			ID: "gpt-5", Reasoning: true,
			ReasoningEfforts: []string{"low", "high", "max", "ultra"}, ReasoningEffortsKnown: true,
		},
		{
			ID: "p/no-selector", Reasoning: true,
			ReasoningEfforts: []string{}, ReasoningEffortsKnown: true,
		},
	}, "ultra", nil))
	first := models[0]
	if first["default_reasoning_level"] != "ultra" {
		t.Fatalf("reasoner default %#v", first)
	}
	if got := effortNames(first); strings.Join(got, ",") != "low,high,max,ultra" {
		t.Fatalf("reasoner levels %v", got)
	}
	second := models[1]
	if second["default_reasoning_level"] != "ultra" {
		t.Fatalf("OpenAI default %#v", second)
	}
	if got := effortNames(second); strings.Join(got, ",") != "low,high,max,ultra" {
		t.Fatalf("OpenAI levels %v", got)
	}
	third := models[2]
	if third["default_reasoning_level"] != nil || len(effortNames(third)) != 0 {
		t.Fatalf("known empty levels %#v", third)
	}
}

func effortNames(entry map[string]any) []string {
	rows, _ := entry["supported_reasoning_levels"].([]any)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		item, _ := row.(map[string]any)
		name, _ := item["effort"].(string)
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func TestKeepCarriesUserKeysAndTrueOverrides(t *testing.T) {
	current := []byte(`{
	  "models": [
	    {
	      "slug": "p/demo",
	      "x_user": {"keep": true},
	      "max_output_tokens": 9007199254740993,
	      "supports_reasoning_summaries": true
	    }
	  ]
	}`)
	generated := Catalog([]Model{{ID: "p/demo", Name: "Demo"}}, "", nil)
	merged := Keep(current, generated)
	models, _ := decode(t, merged)
	demo := models[0]
	if demo["x_user"] == nil || demo["max_output_tokens"] != json.Number("9007199254740993") {
		t.Fatalf("user fields lost %#v", demo)
	}
	if demo["supports_reasoning_summaries"] != true {
		t.Fatalf("true override lost %#v", demo["supports_reasoning_summaries"])
	}
	if len(models) != 1 || demo["slug"] != "p/demo" {
		t.Fatalf("models %#v", models)
	}
}

func TestKeepRemovesAModelNoLongerServed(t *testing.T) {
	current := []byte(`{"models":[{"slug":"gone","x_user":1}]}`)
	generated := Catalog([]Model{{ID: "p/live", Name: "Live"}}, "", nil)
	models, _ := decode(t, Keep(current, generated))
	if len(models) != 1 || models[0]["slug"] != "p/live" {
		t.Fatalf("models %#v", models)
	}
}

func TestKeepCarriesTopLevelUserKeys(t *testing.T) {
	current := []byte(`{
	  "x_owner": {"name": "user"},
	  "x_big": 9007199254740993,
	  "models": [{"slug": "p/demo", "x_user": true}]
	}`)
	generated := Catalog([]Model{{ID: "p/demo", Name: "Demo"}}, "", nil)
	var doc map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(Keep(current, generated)))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	var owner map[string]string
	if err := json.Unmarshal(doc["x_owner"], &owner); err != nil || owner["name"] != "user" {
		t.Fatalf("owner %s: %v", doc["x_owner"], err)
	}
	if string(doc["x_big"]) != "9007199254740993" {
		t.Fatalf("big number changed: %s", doc["x_big"])
	}
	models, err := decodeModels(doc["models"])
	if err != nil || len(models) != 1 || models[0]["x_user"] != true {
		t.Fatalf("models %#v: %v", models, err)
	}
}

func TestUsedModelsEnterTheAppsFirstHundred(t *testing.T) {
	var models []Model
	for i := 0; i < 150; i++ {
		models = append(models, Model{ID: fmt.Sprintf("p/m%03d", i), Name: fmt.Sprintf("m%03d", i)})
	}
	got, _ := decode(t, Catalog(models, "", []string{"p/m149"}))
	if len(got) != 150 {
		t.Fatalf("len %d", len(got))
	}
	found := false
	for _, model := range got[:AppList] {
		if model["slug"] == "p/m149" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("used model outside the app list: %#v", got[0]["slug"])
	}
}

func TestInvalidCurrentIsNotKept(t *testing.T) {
	generated := Catalog([]Model{{ID: "p/demo", Name: "Demo"}}, "", nil)
	merged := Keep([]byte(`{"models":`), generated)
	if string(merged) != string(generated) {
		t.Fatalf("invalid file was partly kept\n%s", merged)
	}
	if Valid([]byte(`{"models":`)) {
		t.Fatal("invalid catalog reported valid")
	}
}
