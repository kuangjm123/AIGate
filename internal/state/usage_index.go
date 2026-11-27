package state

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
)

// usageBlockRows is the number of parsed lines in one frozen block. A full
// block is never rewritten; new lines go into the last block.
const usageBlockRows = 512

// usageIndex keeps the parsed prefix of attempts.jsonl. It is guarded by
// Store.usageMu. A missing file clears it. A file that cannot be read or a
// line that does not parse leaves the index unchanged and is reported as
// unknown; it is never treated as an empty ledger.
type usageIndex struct {
	ready     bool
	fileInfo  os.FileInfo
	offset    int64
	size      int64
	tailClean bool
	blocks    [][]AttemptRecord
}

func (idx *usageIndex) reset() {
	*idx = usageIndex{}
}

func (idx *usageIndex) appended(rec AttemptRecord, n int64) {
	if !idx.ready || idx.size != idx.offset {
		idx.reset()
		return
	}
	idx.appendRecord(rec)
	idx.offset += n
	idx.size = idx.offset
	idx.tailClean = true
}

func (idx *usageIndex) appendRecord(rec AttemptRecord) {
	if len(idx.blocks) == 0 || len(idx.blocks[len(idx.blocks)-1]) >= usageBlockRows {
		idx.blocks = append(idx.blocks, nil)
	}
	last := len(idx.blocks) - 1
	idx.blocks[last] = append(idx.blocks[last], rec)
}

func (idx *usageIndex) records() []AttemptRecord {
	n := 0
	for _, block := range idx.blocks {
		n += len(block)
	}
	out := make([]AttemptRecord, 0, n)
	for _, block := range idx.blocks {
		out = append(out, block...)
	}
	return out
}

func (s *Store) readUsageLocked() ([]AttemptRecord, error) {
	if err := s.refreshUsageLocked(); err != nil {
		return nil, err
	}
	return s.usageIndex.records(), nil
}

func (s *Store) refreshUsageLocked() error {
	path := s.usageFile()
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s.missingUsageLocked()
		}
		return ErrUsageUnknown
	}
	idx := &s.usageIndex
	if idx.ready && idx.fileInfo != nil && !os.SameFile(idx.fileInfo, info) {
		idx.reset()
	}
	if idx.ready && info.Size() == idx.size {
		return nil
	}
	if !idx.ready || info.Size() < idx.offset {
		return s.loadUsageLocked(path, info)
	}
	return s.extendUsageLocked(path, info)
}

func (s *Store) missingUsageLocked() error {
	info, err := os.Stat(s.paths.UsageDir)
	if err != nil {
		if os.IsNotExist(err) {
			s.usageIndex.reset()
			return nil
		}
		return ErrUsageUnknown
	}
	if !info.IsDir() {
		return ErrUsageUnknown
	}
	s.usageIndex.reset()
	return nil
}

func (s *Store) loadUsageLocked(path string, info os.FileInfo) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return ErrUsageUnknown
	}
	complete := completeUsageLines(data)
	recs, err := parseUsageLines(complete)
	if err != nil {
		return err
	}
	blocks := make([][]AttemptRecord, 0, (len(recs)+usageBlockRows-1)/usageBlockRows)
	for start := 0; start < len(recs); start += usageBlockRows {
		end := start + usageBlockRows
		if end > len(recs) {
			end = len(recs)
		}
		blocks = append(blocks, recs[start:end])
	}
	size := info.Size()
	if int64(len(data)) > size {
		size = int64(len(data))
	}
	s.usageIndex = usageIndex{
		ready:     true,
		fileInfo:  info,
		offset:    int64(len(complete)),
		size:      size,
		tailClean: int64(len(complete)) == size,
		blocks:    blocks,
	}
	return nil
}

func (s *Store) extendUsageLocked(path string, info os.FileInfo) error {
	f, err := os.Open(path)
	if err != nil {
		return ErrUsageUnknown
	}
	defer f.Close()
	if _, err := f.Seek(s.usageIndex.offset, io.SeekStart); err != nil {
		s.usageIndex.reset()
		return ErrUsageUnknown
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ErrUsageUnknown
	}
	complete := completeUsageLines(data)
	if len(complete) == 0 {
		s.usageIndex.size = info.Size()
		s.usageIndex.tailClean = s.usageIndex.size == s.usageIndex.offset
		return nil
	}
	recs, err := parseUsageLines(complete)
	if err != nil {
		return err
	}
	for _, rec := range recs {
		s.usageIndex.appendRecord(rec)
	}
	s.usageIndex.offset += int64(len(complete))
	s.usageIndex.size = info.Size()
	if s.usageIndex.size < s.usageIndex.offset {
		s.usageIndex.size = s.usageIndex.offset
	}
	s.usageIndex.tailClean = s.usageIndex.size == s.usageIndex.offset
	return nil
}

func parseUsageLines(data []byte) ([]AttemptRecord, error) {
	var out []AttemptRecord
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec AttemptRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, ErrUsageUnknown
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, ErrUsageUnknown
	}
	return out, nil
}

// trimUsageTail removes a trailing fragment that was not finished with a
// newline, such as a write cut short by a crash or a full disk. It returns
// true when it removed bytes. Complete lines are never touched. The next
// append then starts a fresh line instead of completing the fragment.
func trimUsageTail(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return false, err
	}
	size := info.Size()
	if size == 0 {
		return false, f.Close()
	}
	last := []byte{0}
	if _, err := f.ReadAt(last, size-1); err != nil {
		_ = f.Close()
		return false, err
	}
	if last[0] == '\n' {
		return false, f.Close()
	}
	cut := int64(-1)
	const chunk = 4096
	for end := size; end > 0 && cut < 0; {
		start := end - chunk
		if start < 0 {
			start = 0
		}
		buf := make([]byte, end-start)
		if _, err := f.ReadAt(buf, start); err != nil {
			_ = f.Close()
			return false, err
		}
		if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
			cut = start + int64(i) + 1
		}
		end = start
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	if cut < 0 {
		cut = 0
	}
	if err := os.Truncate(path, cut); err != nil {
		return false, err
	}
	return true, nil
}
