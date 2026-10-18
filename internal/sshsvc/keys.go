package sshsvc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Key is a private key found in ~/.ssh.
type Key struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Type        string `json:"type,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	HasPublic   bool   `json:"hasPublic"`
}

// Keys lists the private keys in ~/.ssh together with their public fingerprint.
// Only .pub files are handed to ssh-keygen, so an encrypted private key can
// never trigger an interactive passphrase prompt.
func (s *Service) Keys(ctx context.Context) ([]Key, error) {
	dir := s.SSHDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var keys []Key
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".pub") || isNonKeyName(name) {
			continue
		}
		path := filepath.Join(dir, name)
		if !looksLikePrivateKey(path) {
			continue
		}
		k := Key{Name: name, Path: "~/.ssh/" + name}
		pubPath := path + ".pub"
		if _, err := os.Stat(pubPath); err == nil {
			k.HasPublic = true
			if typ, fp, err := s.keyInfo(ctx, pubPath); err == nil {
				k.Type, k.Fingerprint = typ, fp
			}
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Name < keys[j].Name })
	return keys, nil
}

// isNonKeyName filters out the files in ~/.ssh that are not private keys.
func isNonKeyName(name string) bool {
	switch {
	case name == "authorized_keys", strings.HasPrefix(name, "authorized_keys"),
		name == "known_hosts", strings.HasPrefix(name, "known_hosts"),
		name == "config", strings.HasPrefix(name, "config."),
		name == "environment", name == "rc", name == ".rnd":
		return true
	}
	return false
}

// looksLikePrivateKey checks the PEM header rather than parsing the key, which
// keeps encrypted keys readable without a passphrase.
func looksLikePrivateKey(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	return strings.Contains(string(buf[:n]), "PRIVATE KEY")
}

// keyInfo reads type and SHA256 fingerprint from a public key.
func (s *Service) keyInfo(ctx context.Context, path string) (typ, fingerprint string, err error) {
	out, err := s.run(ctx, 5*time.Second, s.cfg.KeygenBin, "-l", "-f", path)
	if err != nil {
		return "", "", err
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) < 2 {
		return "", "", errors.New("ssh-keygen: format keluaran tidak dikenal")
	}
	fingerprint = fields[1]
	if open := strings.LastIndex(out, "("); open >= 0 {
		if close := strings.Index(out[open:], ")"); close > 0 {
			typ = out[open+1 : open+close]
		}
	}
	return typ, fingerprint, nil
}

// HostKeyFP is one host key with its SHA256 fingerprint.
type HostKeyFP struct {
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
	// Key is the known_hosts line, host spec included.
	Key string `json:"key"`
}

// HostKeyInfo is the trust state of a host key.
type HostKeyInfo struct {
	Known        bool        `json:"known"`
	HostSpec     string      `json:"hostSpec"`
	Fingerprints []HostKeyFP `json:"fingerprints"`
}

// HostKeyStatus reports whether known_hosts already trusts the host, using
// ssh-keygen -F so hashed known_hosts entries are handled correctly. It makes
// no network calls.
func (s *Service) HostKeyStatus(ctx context.Context, alias string) (*HostKeyInfo, error) {
	if !s.cfg.Enabled {
		return nil, ErrDisabled
	}
	hs, err := s.resolveSpec(ctx, alias)
	if err != nil {
		return nil, err
	}
	info := &HostKeyInfo{HostSpec: hs.spec}
	knownHosts := s.knownHostsPath()
	if _, err := os.Stat(knownHosts); err != nil {
		return info, nil
	}
	out, err := s.run(ctx, 5*time.Second, s.cfg.KeygenBin, "-F", hs.spec, "-f", knownHosts)
	if err != nil || strings.TrimSpace(out) == "" {
		return info, nil
	}
	info.Known = true
	info.Fingerprints = parseKeyLines(out)
	return info, nil
}

// ScanHostKey asks the host which keys it offers. This is a network call and is
// only made when the user explicitly asks for it.
func (s *Service) ScanHostKey(ctx context.Context, alias string) ([]HostKeyFP, error) {
	if !s.cfg.Enabled {
		return nil, ErrDisabled
	}
	hs, err := s.resolveSpec(ctx, alias)
	if err != nil {
		return nil, err
	}
	args := []string{"-T", "5"}
	if hs.port != "" && hs.port != "22" {
		args = append(args, "-p", hs.port)
	}
	args = append(args, hs.hostname)
	out, err := s.run(ctx, 25*time.Second, s.cfg.KeyscanBin, args...)
	// A reachable host wins even if ssh-keyscan also complained about another
	// address, so parse first and only then decide it was a failure.
	keys := parseKeyLines(out)
	if len(keys) > 0 {
		return keys, nil
	}
	if err != nil {
		detail := firstLine(out)
		if detail == "" {
			detail = "tanpa keluaran"
		}
		return nil, fmt.Errorf("%w: ssh-keyscan: %v (%s)", ErrUpstream, err, detail)
	}
	return nil, fmt.Errorf("%w: host tidak memberikan kunci apa pun", ErrUpstream)
}

// TrustHostKey scans the host and writes its keys into known_hosts, replacing
// any previous entry for the same host so a rotated key stops failing. It
// returns how many keys were written.
func (s *Service) TrustHostKey(ctx context.Context, alias string) (int, error) {
	if !s.cfg.Enabled {
		return 0, ErrDisabled
	}
	hs, err := s.resolveSpec(ctx, alias)
	if err != nil {
		return 0, err
	}
	keys, err := s.ScanHostKey(ctx, alias)
	if err != nil {
		return 0, err
	}
	dir := s.SSHDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, fmt.Errorf("buat %s: %w", dir, err)
	}
	knownHosts := s.knownHostsPath()
	if _, err := os.Stat(knownHosts); err == nil {
		// ssh-keygen -R also handles hashed entries and keeps a .old backup.
		_, _ = s.run(ctx, 5*time.Second, s.cfg.KeygenBin, "-R", hs.spec, "-f", knownHosts)
	}
	f, err := os.OpenFile(knownHosts, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, fmt.Errorf("buka known_hosts: %w", err)
	}
	defer f.Close()

	written := 0
	for _, k := range keys {
		if _, err := fmt.Fprintln(f, k.Key); err != nil {
			return written, fmt.Errorf("tulis known_hosts: %w", err)
		}
		written++
	}
	return written, nil
}

// ForgotHostKey removes a host's entries from known_hosts.
func (s *Service) ForgotHostKey(ctx context.Context, alias string) (bool, error) {
	if !s.cfg.Enabled {
		return false, ErrDisabled
	}
	hs, err := s.resolveSpec(ctx, alias)
	if err != nil {
		return false, err
	}
	knownHosts := s.knownHostsPath()
	if _, err := os.Stat(knownHosts); err != nil {
		return false, nil
	}
	if _, err := s.run(ctx, 5*time.Second, s.cfg.KeygenBin, "-R", hs.spec, "-f", knownHosts); err != nil {
		return false, fmt.Errorf("%w: hapus entri known_hosts: %v", ErrUpstream, err)
	}
	return true, nil
}

// resolvedHost holds the effective destination of an alias.
type resolvedHost struct {
	hostname string
	port     string
	spec     string
}

// resolveSpec asks ssh for the effective hostname and port so known_hosts is
// addressed exactly the way ssh addresses it.
func (s *Service) resolveSpec(ctx context.Context, alias string) (resolvedHost, error) {
	if err := validateAlias(alias); err != nil {
		return resolvedHost{}, err
	}
	// Host-key operations only make sense for a host that is actually defined,
	// so a typo is a clear 404 instead of a pointless network attempt.
	cfg, err := s.load()
	if err != nil {
		return resolvedHost{}, err
	}
	if _, ok := cfg.Lookup(alias); !ok {
		return resolvedHost{}, fmt.Errorf("%w: %s", ErrUnknownHost, alias)
	}
	eff, err := s.Effective(ctx, alias)
	if err != nil {
		return resolvedHost{}, err
	}
	hs := resolvedHost{hostname: alias, port: "22"}
	for _, o := range eff.Options {
		switch o.Key {
		case "hostname":
			hs.hostname = o.Value
		case "port":
			hs.port = o.Value
		}
	}
	hs.spec = hs.hostname
	if hs.port != "" && hs.port != "22" {
		hs.spec = "[" + hs.hostname + "]:" + hs.port
	}
	return hs, nil
}

// parseKeyLines turns known_hosts / ssh-keyscan lines into fingerprints.
func parseKeyLines(out string) []HostKeyFP {
	var res []HostKeyFP
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.Join(fields[1:], " ")))
		if err != nil {
			continue
		}
		keyLine := strings.Join(fields[:3], " ")
		fp := ssh.FingerprintSHA256(pub)
		if seen[fp] {
			continue
		}
		seen[fp] = true
		res = append(res, HostKeyFP{Type: pub.Type(), Fingerprint: fp, Key: keyLine})
	}
	return res
}
