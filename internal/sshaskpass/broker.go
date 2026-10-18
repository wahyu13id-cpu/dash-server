// Package sshaskpass brokers stored SSH credentials to the ssh client.
//
// OpenSSH never accepts a password as an argument, and passing one on the
// command line would expose it to every local user through /proc. The supported
// mechanism is the SSH_ASKPASS program, which ssh runs with
// SSH_ASKPASS_REQUIRE=force whenever it needs a password or key passphrase.
//
// This package implements the server side: a unix socket, mode 0600, inside a
// mode 0700 directory, so only the account running the dashboard can connect.
// The ssh child receives a socket path and a single-use-scoped token through
// its environment. The plaintext therefore never touches the disk, never
// appears in argv, and never reaches the logs.
//
// The broker answers only prompts that ask for a password or a passphrase; a
// host-key confirmation prompt is refused, so nothing is ever auto-accepted.
package sshaskpass

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// EnvSocket and EnvToken are set on the ssh child process so the askpass
	// helper can reach the broker and prove which session it belongs to.
	EnvSocket = "DASH_ASKPASS_SOCK"
	EnvToken  = "DASH_ASKPASS_TOKEN"

	// RequireValue forces ssh to always use SSH_ASKPASS, even with a tty.
	RequireValue = "force"
	// RequireEnv configures that behaviour.
	RequireEnv = "SSH_ASKPASS_REQUIRE"
	// AskpassEnv points ssh at the helper program.
	AskpassEnv = "SSH_ASKPASS"
	// HelperEnv marks the ssh child process so the binary knows it is being
	// run as the askpass helper. OpenSSH exec()s SSH_ASKPASS directly (it does
	// not go through a shell and cannot be given extra arguments), so the only
	// reliable way to tell "dashboard <prompt>" apart from "dashboard <command>"
	// is this marker, which exists solely in ssh session environments.
	HelperEnv = "DASH_ASKPASS_HELPER"
	// HelperValue is the value of HelperEnv.
	HelperValue = "1"

	// defaultMaxUses allows ssh to re-prompt a few times (bad password, retry)
	// without letting a token be harvested indefinitely.
	defaultMaxUses = 6
	// defaultTTL bounds how long a grant stays usable.
	defaultTTL = 15 * time.Minute
	// connDeadline bounds one helper conversation.
	connDeadline = 30 * time.Second
)

// Request is what the helper sends.
type Request struct {
	Token  string `json:"token"`
	Prompt string `json:"prompt"`
}

// Response is what the broker answers. Secret is only set on success.
type Response struct {
	Status  string `json:"status"`
	Secret  string `json:"secret,omitempty"`
	Message string `json:"message,omitempty"`
}

// Broker serves credential requests.
type Broker struct {
	dir  string
	sock string
	ln   net.Listener
	log  *slog.Logger

	mu     sync.Mutex
	grants map[string]*grant
	closed bool
	wg     sync.WaitGroup
}

type grant struct {
	alias      string
	password   string
	passphrase string
	uses       int
	maxUses    int
	expires    time.Time
}

// New starts a broker listening on a private unix socket.
func New(log *slog.Logger) (*Broker, error) {
	if log == nil {
		log = slog.Default()
	}
	dir, err := os.MkdirTemp("", "dash-askpass-")
	if err != nil {
		return nil, fmt.Errorf("askpass: buat direktori: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("askpass: mode direktori: %w", err)
	}
	sock := filepath.Join(dir, "sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("askpass: listen: %w", err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		_ = ln.Close()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("askpass: mode socket: %w", err)
	}
	b := &Broker{dir: dir, sock: sock, ln: ln, log: log, grants: map[string]*grant{}}
	b.wg.Add(1)
	go b.serve()
	return b, nil
}

// Socket returns the socket path to hand to the ssh child.
func (b *Broker) Socket() string { return b.sock }

// Close stops the broker and removes its private directory.
func (b *Broker) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.grants = map[string]*grant{}
	b.mu.Unlock()

	err := b.ln.Close()
	b.wg.Wait()
	_ = os.RemoveAll(b.dir)
	return err
}

// Grant registers credentials for one alias and returns the token the ssh child
// must present. An empty password and passphrase yield an empty token, which
// means "no stored credentials" and leaves ssh to prompt in the terminal.
func (b *Broker) Grant(alias, password, passphrase string) (string, error) {
	if password == "" && passphrase == "" {
		return "", nil
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return "", errors.New("askpass: broker sudah ditutup")
	}
	b.pruneLocked()
	b.grants[token] = &grant{
		alias:      alias,
		password:   password,
		passphrase: passphrase,
		maxUses:    defaultMaxUses,
		expires:    time.Now().Add(defaultTTL),
	}
	return token, nil
}

// Revoke drops a grant once its session is gone.
func (b *Broker) Revoke(token string) {
	if token == "" {
		return
	}
	b.mu.Lock()
	delete(b.grants, token)
	b.mu.Unlock()
}

func (b *Broker) serve() {
	defer b.wg.Done()
	for {
		conn, err := b.ln.Accept()
		if err != nil {
			b.mu.Lock()
			closed := b.closed
			b.mu.Unlock()
			if closed {
				return
			}
			// A transient accept error must not spin the CPU.
			time.Sleep(20 * time.Millisecond)
			continue
		}
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			defer conn.Close()
			b.handle(conn)
		}()
	}
}

func (b *Broker) handle(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(connDeadline))
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	for {
		var req Request
		if err := dec.Decode(&req); err != nil {
			return
		}
		if err := enc.Encode(b.answer(req)); err != nil {
			return
		}
	}
}

func (b *Broker) answer(req Request) Response {
	b.mu.Lock()
	g := b.grants[req.Token]
	if g != nil && (time.Now().After(g.expires) || g.uses >= g.maxUses) {
		delete(b.grants, req.Token)
		g = nil
	}
	if g == nil {
		b.mu.Unlock()
		b.log.Warn("askpass: token tidak dikenal atau kedaluwarsa")
		return Response{Status: "denied", Message: "token tidak dikenal atau kedaluwarsa"}
	}
	g.uses++
	alias, uses := g.alias, g.uses
	kind := classifyPrompt(req.Prompt)
	secret := ""
	switch kind {
	case kindPassword:
		secret = g.password
	case kindPassphrase:
		secret = g.passphrase
	}
	b.mu.Unlock()

	if secret == "" {
		b.log.Info("askpass: permintaan ditolak",
			"alias", alias, "jenis", string(kind), "pemakaian", uses)
		return Response{Status: "denied", Message: "tidak ada kredensial untuk prompt ini"}
	}
	b.log.Info("askpass: kredensial diberikan",
		"alias", alias, "jenis", string(kind), "pemakaian", uses)
	return Response{Status: "ok", Secret: secret}
}

// pruneLocked drops expired grants so the map cannot grow without bound.
func (b *Broker) pruneLocked() {
	now := time.Now()
	for token, g := range b.grants {
		if now.After(g.expires) || g.uses >= g.maxUses {
			delete(b.grants, token)
		}
	}
}

type promptKind string

const (
	kindOther      promptKind = "other"
	kindPassword   promptKind = "password"
	kindPassphrase promptKind = "passphrase"
)

// classifyPrompt decides what ssh is asking for. Anything that is not a
// password or passphrase (notably the host-key confirmation) is refused.
func classifyPrompt(prompt string) promptKind {
	p := strings.ToLower(prompt)
	switch {
	case strings.Contains(p, "passphrase"):
		return kindPassphrase
	case strings.Contains(p, "password"):
		return kindPassword
	default:
		return kindOther
	}
}

func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("askpass: token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
