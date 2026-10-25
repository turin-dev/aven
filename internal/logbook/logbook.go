// Package logbook은 배포 로그를 파일에 남기면서 실시간 구독자에게 뿌린다.
package logbook

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Event struct {
	Kind string // "log" | "status" | "done"
	Data string
}

type Book struct {
	mu     sync.Mutex
	f      *os.File
	lines  []string
	subs   map[chan Event]struct{}
	closed bool
	final  string
}

type Registry struct {
	dir   string
	mu    sync.Mutex
	books map[string]*Book
}

func NewRegistry(dir string) (*Registry, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &Registry{dir: dir, books: map[string]*Book{}}, nil
}

func (r *Registry) path(id string) string { return filepath.Join(r.dir, id+".log") }

// Open은 진행 중인 배포의 로그북을 만든다.
func (r *Registry) Open(id string) (*Book, error) {
	f, err := os.OpenFile(r.path(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, err
	}
	b := &Book{f: f, subs: map[chan Event]struct{}{}}
	r.mu.Lock()
	r.books[id] = b
	r.mu.Unlock()
	return b, nil
}

func (b *Book) Printf(format string, args ...any) {
	b.Line(time.Now().UTC().Format("15:04:05") + " " + fmt.Sprintf(format, args...))
}

func (b *Book) Line(s string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.f.WriteString(s + "\n")
	b.lines = append(b.lines, s)
	b.broadcast(Event{"log", s})
}

func (b *Book) Status(s string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.broadcast(Event{"status", s})
}

func (b *Book) broadcast(e Event) {
	for ch := range b.subs {
		select {
		case ch <- e:
		default: // 느린 구독자는 버린다
			close(ch)
			delete(b.subs, ch)
		}
	}
}

// Close는 최종 상태를 알리고 구독자를 정리한다.
func (b *Book) Close(final string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed, b.final = true, final
	b.broadcast(Event{"done", final})
	for ch := range b.subs {
		close(ch)
	}
	b.subs = nil
	b.f.Close()
}

// Subscribe는 지금까지의 로그와, 진행 중이면 이후 이벤트 채널을 돌려준다.
// 이미 끝난 배포(메모리에 없음)는 파일에서 읽고 ch는 nil이다.
func (r *Registry) Subscribe(id string) (past []string, ch chan Event, cancel func()) {
	r.mu.Lock()
	b := r.books[id]
	r.mu.Unlock()
	if b == nil {
		return r.readFile(id), nil, func() {}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	past = append([]string(nil), b.lines...)
	if b.closed {
		return past, nil, func() {}
	}
	ch = make(chan Event, 256)
	b.subs[ch] = struct{}{}
	return past, ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
	}
}

// Release는 끝난 로그북을 메모리에서 내린다. 이후 조회는 파일로 처리한다.
func (r *Registry) Release(id string) {
	r.mu.Lock()
	delete(r.books, id)
	r.mu.Unlock()
}

func (r *Registry) Remove(id string) {
	r.Release(id)
	os.Remove(r.path(id))
}

func (r *Registry) readFile(id string) []string {
	f, err := os.Open(r.path(id))
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

// Discard는 아무 데도 쓰지 않는 로그북이다.
func Discard() *Book { return &Book{closed: true} }
