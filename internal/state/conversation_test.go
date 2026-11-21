package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConversationArchiveGroupsBySession(t *testing.T) {
	dir := t.TempDir()
	store := Open(Paths{ConfigFile: filepath.Join(dir, "config.json"), UsageDir: filepath.Join(dir, "usage")})
	if err := store.AppendTurn(TurnRecord{Session: "", Output: "skip"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendTurn(TurnRecord{Session: "s/1", At: time.Unix(1, 0).UTC(), Input: "hi", Output: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendTurn(TurnRecord{Session: "s/1", At: time.Unix(2, 0).UTC(), Output: "again"}); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListConversations()
	if err != nil || len(list) != 1 || list[0].Session != "s/1" || list[0].Turns != 2 {
		t.Fatalf("list %v %+v", err, list)
	}
	turns, err := store.ReadConversation("s/1")
	if err != nil || len(turns) != 2 || turns[0].Output != "hello" || turns[1].Output != "again" || turns[0].Input != "hi" {
		t.Fatalf("turns %v %+v", err, turns)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "usage", "conversations"))
	if err != nil || len(entries) != 1 || entries[0].Name() == "s/1.jsonl" {
		t.Fatalf("file name must not be the raw session: %v", entries)
	}
	path := filepath.Join(dir, "usage", "conversations", entries[0].Name())
	if err := os.WriteFile(path, []byte("{not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadConversation("s/1"); err != ErrConversationUnknown {
		t.Fatalf("corrupt read %v", err)
	}
	if _, err := store.ListConversations(); err != ErrConversationUnknown {
		t.Fatalf("corrupt list %v", err)
	}
}
