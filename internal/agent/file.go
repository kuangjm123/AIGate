package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

var errHost = errors.New("host")

func urlParse(raw string) (*url.URL, error) {
	return url.Parse(raw)
}

type field struct {
	Present bool   `json:"present"`
	Value   string `json:"value,omitempty"`
}

type record struct {
	Fields        map[string]field  `json:"fields"`
	Written       map[string]string `json:"written,omitempty"`
	Catalog       string            `json:"catalog,omitempty"`
	CatalogKnown  bool              `json:"catalog_known,omitempty"`
	CatalogBefore string            `json:"catalog_before,omitempty"`
	Capability    bool              `json:"capability,omitempty"`
}

func loadRecord(m Machine, id string) (record, bool, error) {
	b, err := os.ReadFile(stashPath(m, id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return record{}, false, nil
		}
		return record{}, false, fail("unknown")
	}
	var rec record
	if json.Unmarshal(b, &rec) != nil || rec.Fields == nil {
		return record{}, true, fail("unknown")
	}
	return rec, true, nil
}

func saveRecord(m Machine, id string, rec record) error {
	if m.StashDir == "" {
		return fail("unknown")
	}
	if rec.Fields == nil {
		rec.Fields = map[string]field{}
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return writeAtomic(stashPath(m, id), b)
}

func removeRecord(m Machine, id string) error {
	err := os.Remove(stashPath(m, id))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func stashPath(m Machine, id string) string {
	return filepath.Join(m.StashDir, id+".json")
}

func readJSONFile(path string) (map[string]any, []byte, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{}, nil, false, nil
		}
		return nil, nil, false, fail("unknown")
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return map[string]any{}, b, true, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil || doc == nil {
		return nil, b, true, fail("unknown")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, b, true, fail("unknown")
	}
	return doc, b, true, nil
}

func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func childObject(doc map[string]any, key string) (map[string]any, error) {
	v, ok := doc[key]
	if !ok || v == nil {
		return nil, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fail("unknown")
	}
	return m, nil
}

func stringField(doc map[string]any, key string) (string, bool, error) {
	v, ok := doc[key]
	if !ok || v == nil {
		return "", false, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", true, fail("unknown")
	}
	return s, true, nil
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func restoreString(doc map[string]any, key string, f field) {
	if !f.Present {
		delete(doc, key)
		return
	}
	doc[key] = f.Value
}

func snapString(doc map[string]any, key string) field {
	s, ok, err := stringField(doc, key)
	if err != nil || !ok {
		return field{}
	}
	return field{Present: true, Value: s}
}

func commit(m Machine, id, path string, existed bool, orig, next []byte, rec record) error {
	if err := writeAtomic(path, next); err != nil {
		return err
	}
	if err := saveRecord(m, id, rec); err != nil {
		restoreFile(path, existed, orig)
		return err
	}
	return nil
}

func restoreFile(path string, existed bool, orig []byte) {
	_ = restoreFileState(path, existed, orig)
}

func restoreFileState(path string, existed bool, orig []byte) error {
	if !existed {
		err := os.Remove(path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if orig != nil {
		return writeAtomic(path, orig)
	}
	return nil
}

func readOptionalFile(path string) ([]byte, bool, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		return b, true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return nil, false, err
}

func secretDigest(secret string) string {
	if secret == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".aigate-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmpName)
		if werr != nil {
			return werr
		}
		return cerr
	}
	_ = os.Chmod(tmpName, 0o600)
	if err := os.Rename(tmpName, path); err == nil {
		return nil
	}
	bak := path + ".aigate-bak"
	_ = os.Remove(bak)
	if err := os.Rename(path, bak); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Rename(bak, path)
		_ = os.Remove(tmpName)
		return err
	}
	_ = os.Remove(bak)
	return nil
}

func matches(got, written map[string]string) bool {
	if len(written) == 0 {
		return false
	}
	for k, v := range written {
		if got[k] != v {
			return false
		}
	}
	return true
}

func originMoved(written map[string]string, origin string) bool {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if origin == "" {
		return false
	}
	for _, v := range written {
		u, err := url.Parse(v)
		if err != nil || u.Scheme == "" || u.Host == "" {
			continue
		}
		if !sameOrigin(v, origin) {
			return true
		}
	}
	return false
}

func wiredState(have bool, rec record, got map[string]string, origin string) string {
	if !have {
		return ""
	}
	if originMoved(rec.Written, origin) || !matches(got, rec.Written) {
		return "drift"
	}
	return "on"
}

func finishNote(st Status, hint string) Status {
	switch st.State {
	case "unknown":
		st.Note = "配置读不出来，没有改它。"
	case "drift":
		st.Note = "配置和上次接入写下的不一致。重新接入会按当前网关改回去，断开会恢复接入前记下的值。"
	case "hand":
		st.Note = "配置已经指向这个网关，但没有接入记录。接入会把当前的值记下来。"
	case "on", "off":
		st.Note = hint
	}
	return st
}

func requireModels(gw Target, models []Model) error {
	if gw.Origin == "" {
		return fail("not_listening")
	}
	if len(models) == 0 {
		return fail("no_models")
	}
	return nil
}

func quoteDotenv(s string) string {
	if s == "" {
		return `""`
	}
	safe := true
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._:/@+-", r) {
			continue
		}
		safe = false
		break
	}
	if safe {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
