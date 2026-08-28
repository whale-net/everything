package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
)

// historyLimit bounds how many raw JSONL lines a session keeps in memory for
// replay to newly (re)connecting subscribers, e.g. after a browser reload.
const historyLimit = 5000

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Session wraps one `pi --mode rpc` subprocess and fans its stdout out to any
// number of live SSE subscribers, replaying recent history to new ones.
type Session struct {
	id    string
	cmd   *exec.Cmd
	stdin io.WriteCloser

	mu      sync.Mutex
	subs    map[chan []byte]struct{}
	history [][]byte
	closed  bool
}

func (s *Session) broadcast(line []byte) {
	cp := append([]byte(nil), line...)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, cp)
	if len(s.history) > historyLimit {
		s.history = s.history[len(s.history)-historyLimit:]
	}
	for ch := range s.subs {
		select {
		case ch <- cp:
		default:
			// Slow subscriber; drop the line rather than block the reader loop.
		}
	}
}

func (s *Session) subscribe() (chan []byte, [][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan []byte, 64)
	s.subs[ch] = struct{}{}
	hist := append([][]byte(nil), s.history...)
	return ch, hist
}

func (s *Session) unsubscribe(ch chan []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[ch]; ok {
		delete(s.subs, ch)
		close(ch)
	}
}

func (s *Session) send(v any) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return errors.New("session is closed")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = s.stdin.Write(b)
	return err
}

func (s *Session) kill() error {
	if s.cmd.Process == nil {
		return nil
	}
	return s.cmd.Process.Kill()
}

// Manager owns every live Session on this host, keyed by id.
type Manager struct {
	piBin string
	args  []string

	mu       sync.Mutex
	sessions map[string]*Session
}

func newManager(piBin string, extraArgs []string) *Manager {
	return &Manager{piBin: piBin, args: extraArgs, sessions: map[string]*Session{}}
}

func (m *Manager) create(provider, model string) (*Session, error) {
	args := append([]string{"--mode", "rpc", "--no-session"}, m.args...)
	if provider != "" {
		args = append(args, "--provider", provider)
	}
	if model != "" {
		args = append(args, "--model", model)
	}

	cmd := exec.Command(m.piBin, args...)
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	sess := &Session{
		id:    newID(),
		cmd:   cmd,
		stdin: stdin,
		subs:  map[chan []byte]struct{}{},
	}

	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			sess.broadcast(line)
		}
		sess.mu.Lock()
		sess.closed = true
		subs := sess.subs
		sess.subs = map[chan []byte]struct{}{}
		sess.mu.Unlock()
		for ch := range subs {
			close(ch)
		}
		_ = cmd.Wait()
	}()

	m.mu.Lock()
	m.sessions[sess.id] = sess
	m.mu.Unlock()
	return sess, nil
}

func (m *Manager) get(id string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

func (m *Manager) delete(id string) {
	m.mu.Lock()
	sess := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if sess != nil {
		_ = sess.kill()
	}
}
