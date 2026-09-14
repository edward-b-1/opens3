package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// errQueueFull is returned when the persistent queue hit its limit.
var errQueueFull = errors.New("notify: queue full")

// diskQueue is a directory of JSON files, one per message, named so that
// lexical order is arrival order. It survives restarts: files that were
// not delivered are replayed when the dispatcher starts.
type diskQueue struct {
	dir   string
	limit int

	mu    sync.Mutex
	count int
	n     uint64
}

func openDiskQueue(dir string, limit int) (*diskQueue, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	q := &diskQueue{dir: dir, limit: limit}
	q.count = len(q.list())
	return q, nil
}

// push persists m and returns its path.
func (q *diskQueue) push(m *Message) (string, error) {
	q.mu.Lock()
	if q.limit > 0 && q.count >= q.limit {
		q.mu.Unlock()
		return "", errQueueFull
	}
	q.count++
	q.n++
	name := fmt.Sprintf("%020d-%08d.json", time.Now().UnixNano(), q.n)
	q.mu.Unlock()
	data, err := json.Marshal(m)
	if err != nil {
		q.dec()
		return "", err
	}
	path := filepath.Join(q.dir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		q.dec()
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		q.dec()
		return "", err
	}
	return path, nil
}

func (q *diskQueue) dec() {
	q.mu.Lock()
	q.count--
	q.mu.Unlock()
}

// remove deletes a delivered (or corrupt) entry.
func (q *diskQueue) remove(path string) {
	if err := os.Remove(path); err == nil {
		q.dec()
	}
}

// list returns queued entry paths in arrival order.
func (q *diskQueue) list() []string {
	ents, err := os.ReadDir(q.dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		out = append(out, filepath.Join(q.dir, e.Name()))
	}
	sort.Strings(out)
	return out
}

// load reads one entry.
func (q *diskQueue) load(path string) (*Message, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Message
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// length is the number of queued entries.
func (q *diskQueue) length() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.count
}
