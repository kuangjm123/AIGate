package reasoning

import (
	"bytes"
	"encoding/json"
)

func readObject(body []byte) (map[string]json.RawMessage, bool) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || body[0] != '{' {
		return nil, false
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(body, &doc) != nil {
		return nil, false
	}
	return doc, true
}

func objectField(doc map[string]json.RawMessage, key string) (map[string]json.RawMessage, bool) {
	raw, ok := doc[key]
	if !ok {
		return nil, false
	}
	return readObject(raw)
}

func stringField(doc map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := doc[key]
	if !ok {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func intField(doc map[string]json.RawMessage, key string) (int, bool) {
	raw, ok := doc[key]
	if !ok {
		return 0, false
	}
	var value int
	if json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	return value, true
}

func boolField(doc map[string]json.RawMessage, key string) (bool, bool) {
	raw, ok := doc[key]
	if !ok {
		return false, false
	}
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		return false, false
	}
	return value, true
}

func setString(doc map[string]json.RawMessage, key, value string) {
	raw, err := json.Marshal(value)
	if err == nil {
		doc[key] = raw
	}
}

func setInt(doc map[string]json.RawMessage, key string, value int) {
	raw, err := json.Marshal(value)
	if err == nil {
		doc[key] = raw
	}
}

func setObject(doc map[string]json.RawMessage, key string, value map[string]json.RawMessage) {
	raw, err := json.Marshal(value)
	if err == nil {
		doc[key] = raw
	}
}

func deleteField(doc map[string]json.RawMessage, key string) {
	delete(doc, key)
}

func cloneObject(in map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
