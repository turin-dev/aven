// Package logbook은 배포 로그를 파일에 남기면서 실시간 구독자에게 뿌린다.
package logbook

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Event struct {
	Kind string // "log" | "status" | "done"
	Data string
}

// maxMemLines는 진행 중인 로그북이 메모리에 들고 있는 줄 수 상한이다. 넘으면 메모리 사본을 버리고
// 새 구독자에게는 파일에서 읽어 준다(빌드 로그가 아주 길어도 서버 메모리가 로그 크기만큼 늘지 않는다).
const maxMemLines = 10000

// maxReadLine은 파일에서 읽을 때 한 줄의 최대 길이다(넘는 줄은 잘린다).
const maxReadLine = 1024 * 1024

type Book struct {
	mu    sync.Mutex
	f     *os.File
	path  string
	lines []string
	// mem이면 lines가 파일 전체와 같다. 이미 내용이 있던 파일을 다시 열었거나(재배포 없는 scale, self-host 단계)
	// maxMemLines를 넘으면 false가 되고 Subscribe는 파일을 읽는다.
	mem    bool
	subs   map[chan Event]struct{}
	closed bool
	final  string
}

type Registry struct {
	dir   string
	mu    sync.Mutex
	books map[string]*Book
}

// ErrInvalidID는 파일 이름으로 쓸 수 없는 로그북 id다.
var ErrInvalidID = errors.New("logbook: invalid id")

func NewRegistry(dir string) (*Registry, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &Registry{dir: dir, books: map[string]*Book{}}, nil
}

// validID는 id가 로그 디렉터리 안의 파일 이름 하나로만 쓰이는지다("../x", "a/b" 같은 경로를 막는다).
func validID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, "/\\\x00")
}

func (r *Registry) path(id string) string { return filepath.Join(r.dir, id+".log") }

// Open은 진행 중인 배포의 로그북을 만든다. 같은 id의 파일이 있으면 그 뒤에 이어 쓰고, 구독자는 예전 줄부터 받는다.
func (r *Registry) Open(id string) (*Book, error) {
	if !validID(id) {
		return nil, ErrInvalidID
	}
	p := r.path(id)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, err
	}
	b := &Book{f: f, path: p, mem: true, subs: map[chan Event]struct{}{}}
	if st, err := f.Stat(); err == nil && st.Size() > 0 {
		b.mem = false
	}
	r.mu.Lock()
	r.books[id] = b
	r.mu.Unlock()
	return b, nil
}

func (b *Book) Printf(format string, args ...any) {
	b.Line(time.Now().UTC().Format("15:04:05") + " " + fmt.Sprintf(format, args...))
}

// Line은 한 줄을 남긴다. 줄바꿈이 든 문자열은 여러 줄로 나눈다(파일과 메모리 사본, 구독자가 같은 줄을 본다).
func (b *Book) Line(s string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	for _, l := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		b.f.WriteString(l + "\n")
		if b.mem {
			if len(b.lines) < maxMemLines {
				b.lines = append(b.lines, l)
			} else {
				b.mem, b.lines = false, nil
			}
		}
		b.broadcast(Event{"log", l})
	}
}

func (b *Book) Status(s string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.broadcast(Event{"status", s})
}

func (b *Book) broadcast(e Event) {
	for ch := range b.subs {
		select {
		case ch <- e:
		default: // 느린 구독자는 버린다(채널이 닫히면 구독자는 다시 연결해 처음부터 받는다)
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
	b.lines = nil
	if b.f != nil {
		b.f.Close()
	}
}

// Subscribe는 지금까지의 로그와, 진행 중이면 이후 이벤트 채널을 돌려준다.
// 이미 끝난 배포(메모리에 없음)는 파일에서 읽고 ch는 nil이다.
func (r *Registry) Subscribe(id string) (past []string, ch chan Event, cancel func()) {
	if !validID(id) {
		return nil, nil, func() {}
	}
	r.mu.Lock()
	b := r.books[id]
	r.mu.Unlock()
	if b == nil {
		return r.readFile(id), nil, func() {}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return r.readFile(id), nil, func() {}
	}
	if b.mem {
		past = append([]string(nil), b.lines...)
	} else {
		// 쓰기는 b.mu 안에서만 하므로 지금 읽는 파일 내용과 이후 채널 이벤트 사이에 빠지거나 겹치는 줄이 없다.
		past = readLines(b.path)
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

// Release는 끝난(Close한) 로그북을 메모리에서 내린다. 이후 조회는 파일로 처리한다.
// 같은 id를 그 사이 다시 Open했으면(그 로그북은 아직 진행 중이다) 내리지 않는다: 먼저 끝난 쪽의 Release가 진행 중인
// 로그북을 지우면 실시간 구독자가 새 줄을 받지 못했다.
func (r *Registry) Release(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.books[id]
	if b == nil {
		return
	}
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		delete(r.books, id)
	}
}

// Remove는 로그북을 메모리와 파일에서 지운다(배포 삭제).
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	delete(r.books, id)
	r.mu.Unlock()
	if validID(id) {
		os.Remove(r.path(id))
	}
}

func (r *Registry) readFile(id string) []string { return readLines(r.path(id)) }

func readLines(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	rd := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := readLine(rd)
		if err != nil {
			return lines
		}
		lines = append(lines, line)
	}
}

// readLine은 한 줄을 읽는다. maxReadLine보다 긴 줄은 잘라서 돌려준다(bufio.Scanner는 긴 줄에서 나머지 로그를 모두 버렸다).
func readLine(rd *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		chunk, isPrefix, err := rd.ReadLine()
		if err != nil {
			if sb.Len() > 0 {
				return sb.String(), nil
			}
			return "", err
		}
		if room := maxReadLine - sb.Len(); room > 0 {
			sb.Write(chunk[:min(len(chunk), room)])
		}
		if !isPrefix {
			return sb.String(), nil
		}
	}
}

// Discard는 아무 데도 쓰지 않는 로그북이다.
func Discard() *Book { return &Book{closed: true} }
