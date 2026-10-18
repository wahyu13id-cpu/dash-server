package sshsvc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"dashboard/internal/sshconfig"
	"dashboard/internal/sshsecret"
)

// HostInput is the payload used to create or update a host.
type HostInput struct {
	Alias        string             `json:"alias"`
	HostName     string             `json:"hostName"`
	User         string             `json:"user"`
	Port         string             `json:"port"`
	IdentityFile []string           `json:"identityFile"`
	ProxyJump    string             `json:"proxyJump"`
	Comment      string             `json:"comment"`
	Options      []sshconfig.Option `json:"options"`
}

// spec converts the input into the config writer's representation, dropping
// blank entries so an empty form field never writes a dangling directive.
func (in HostInput) spec() sshconfig.HostSpec {
	var ids []string
	for _, f := range in.IdentityFile {
		if f = strings.TrimSpace(f); f != "" {
			ids = append(ids, f)
		}
	}
	var opts []sshconfig.Option
	for _, o := range in.Options {
		key := strings.TrimSpace(o.Key)
		if key == "" {
			continue
		}
		opts = append(opts, sshconfig.Option{Key: key, Value: strings.TrimSpace(o.Value)})
	}
	return sshconfig.HostSpec{
		Alias:        strings.TrimSpace(in.Alias),
		HostName:     strings.TrimSpace(in.HostName),
		User:         strings.TrimSpace(in.User),
		Port:         strings.TrimSpace(in.Port),
		IdentityFile: ids,
		ProxyJump:    strings.TrimSpace(in.ProxyJump),
		Comment:      strings.TrimSpace(in.Comment),
		Options:      opts,
	}
}

// HostView is a host together with everything the UI shows next to it.
type HostView struct {
	sshconfig.Host
	Favorite        bool   `json:"favorite"`
	Note            string `json:"note,omitempty"`
	LastConnectedAt string `json:"lastConnectedAt,omitempty"`
	ConnectCount    int    `json:"connectCount"`
	HasPassword     bool   `json:"hasPassword"`
	HasPassphrase   bool   `json:"hasPassphrase"`
	// SecretBroken means a credential is stored but cannot be decrypted, which
	// happens when DASH_SESSION_SECRET changed.
	SecretBroken bool `json:"secretBroken"`
	// SharedBlock means the Host line defines several aliases.
	SharedBlock bool `json:"sharedBlock"`
}

// DefaultView is a block that is not a connectable host: wildcard patterns,
// Match sections and implied global directives.
type DefaultView struct {
	Kind      string             `json:"kind"`
	Patterns  []string           `json:"patterns"`
	Options   []sshconfig.Option `json:"options"`
	File      string             `json:"file"`
	StartLine int                `json:"startLine"`
	EndLine   int                `json:"endLine"`
	Comment   string             `json:"comment,omitempty"`
	Writable  bool               `json:"writable"`
	Implied   bool               `json:"implied"`
}

// View is the whole picture the SSH page renders.
type View struct {
	Enabled    bool                 `json:"enabled"`
	ConfigPath string               `json:"configPath"`
	SSHDir     string               `json:"sshDir"`
	Exists     bool                 `json:"exists"`
	Mode       string               `json:"mode,omitempty"`
	Hosts      []HostView           `json:"hosts"`
	Defaults   []DefaultView        `json:"defaults"`
	Files      []sshconfig.FileInfo `json:"files"`
	Warnings   []string             `json:"warnings"`
	Keys       []Key                `json:"keys"`
}

// View assembles the full state of the SSH page.
func (s *Service) View(ctx context.Context) (*View, error) {
	out := &View{Enabled: s.cfg.Enabled, ConfigPath: s.cfg.ConfigPath, SSHDir: s.SSHDir()}
	if !s.cfg.Enabled {
		return out, nil
	}
	sc, err := s.load()
	if err != nil {
		return nil, err
	}
	metas, err := s.store.All(ctx)
	if err != nil {
		return nil, err
	}
	secretBroken := false
	if s.secrets != nil && s.secrets.VerifyKey(ctx) != nil {
		secretBroken = true
	}

	hosts := make([]HostView, 0, len(sc.Hosts()))
	for _, h := range sc.Hosts() {
		hv := HostView{Host: *h, SharedBlock: len(h.Patterns) > 1}
		if m, ok := metas[h.Alias]; ok {
			hv.Favorite = m.Favorite
			hv.Note = m.Note
			hv.LastConnectedAt = m.LastConnectedAt
			hv.ConnectCount = m.ConnectCount
		}
		if s.secrets != nil {
			if kinds, err := s.secrets.Status(ctx, h.Alias); err == nil {
				hv.HasPassword = kinds[sshsecret.KindPassword]
				hv.HasPassphrase = kinds[sshsecret.KindPassphrase]
				hv.SecretBroken = secretBroken && (hv.HasPassword || hv.HasPassphrase)
			}
		}
		hosts = append(hosts, hv)
	}
	sortHosts(hosts)

	defaults := make([]DefaultView, 0)
	for _, b := range sc.Defaults() {
		defaults = append(defaults, DefaultView{
			Kind:      string(b.Kind),
			Patterns:  b.Patterns,
			Options:   b.Options,
			File:      b.File,
			StartLine: b.StartLine,
			EndLine:   b.EndLine,
			Comment:   b.Comment,
			Writable:  b.Writable,
			Implied:   b.Implied,
		})
	}

	for _, f := range sc.Files {
		if f.Primary {
			out.Exists = f.Exists
			out.Mode = f.Mode
		}
	}
	keys, err := s.Keys(ctx)
	if err != nil {
		// A missing ssh-keygen must not break the whole page.
		s.log.Warn("ssh: gagal membaca daftar kunci", "error", err)
	}

	out.Hosts = hosts
	out.Defaults = defaults
	out.Files = sc.Files
	out.Warnings = sc.Warnings
	out.Keys = keys
	return out, nil
}

// sortHosts orders favourites first, then the most recently used, then by alias.
func sortHosts(hosts []HostView) {
	sort.SliceStable(hosts, func(i, j int) bool {
		a, b := hosts[i], hosts[j]
		if a.Favorite != b.Favorite {
			return a.Favorite
		}
		if (a.LastConnectedAt != "") != (b.LastConnectedAt != "") {
			return a.LastConnectedAt != ""
		}
		if a.LastConnectedAt != b.LastConnectedAt {
			return a.LastConnectedAt > b.LastConnectedAt
		}
		return a.Alias < b.Alias
	})
}

// Lookup returns one host.
func (s *Service) Lookup(ctx context.Context, alias string) (*sshconfig.Host, error) {
	if !s.cfg.Enabled {
		return nil, ErrDisabled
	}
	sc, err := s.load()
	if err != nil {
		return nil, err
	}
	h, ok := sc.Lookup(alias)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownHost, alias)
	}
	return h, nil
}

// Add appends a new host block.
func (s *Service) Add(ctx context.Context, in HostInput) (*sshconfig.Host, error) {
	if !s.cfg.Enabled {
		return nil, ErrDisabled
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	sc, err := s.load()
	if err != nil {
		return nil, err
	}
	host, err := sc.Append(in.spec())
	if err != nil {
		return nil, mapWriteError(err)
	}
	return host, nil
}

// Update replaces the block defining alias. Renaming keeps the stored
// credential and metadata attached to the host.
func (s *Service) Update(ctx context.Context, alias string, in HostInput) (*sshconfig.Host, error) {
	if !s.cfg.Enabled {
		return nil, ErrDisabled
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	sc, err := s.load()
	if err != nil {
		return nil, err
	}
	spec := in.spec()
	host, err := sc.Update(alias, spec)
	if err != nil {
		return nil, mapWriteError(err)
	}
	if spec.Alias != alias {
		s.migrateAlias(ctx, alias, spec.Alias)
	}
	return host, nil
}

// Delete removes a host and the metadata and credentials attached to it.
func (s *Service) Delete(ctx context.Context, alias string) error {
	if !s.cfg.Enabled {
		return ErrDisabled
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	sc, err := s.load()
	if err != nil {
		return err
	}
	if err := sc.Delete(alias); err != nil {
		return mapWriteError(err)
	}
	if s.secrets != nil {
		if err := s.secrets.ClearAlias(ctx, alias); err != nil {
			s.log.Warn("ssh: gagal menghapus kredensial", "alias", alias, "error", err)
		}
	}
	if err := s.store.Delete(ctx, alias); err != nil {
		s.log.Warn("ssh: gagal menghapus metadata", "alias", alias, "error", err)
	}
	return nil
}

// SetFavorite toggles the favourite flag.
func (s *Service) SetFavorite(ctx context.Context, alias string, favorite bool) error {
	if !s.cfg.Enabled {
		return ErrDisabled
	}
	if _, err := s.Lookup(ctx, alias); err != nil {
		return err
	}
	return s.store.SetFavorite(ctx, alias, favorite)
}

// SetNote stores a note for a host.
func (s *Service) SetNote(ctx context.Context, alias, note string) error {
	if !s.cfg.Enabled {
		return ErrDisabled
	}
	if _, err := s.Lookup(ctx, alias); err != nil {
		return err
	}
	return s.store.SetNote(ctx, alias, note)
}

// SecretStatus reports which credentials are stored for a host.
func (s *Service) SecretStatus(ctx context.Context, alias string) (map[sshsecret.Kind]bool, error) {
	if !s.cfg.Enabled {
		return nil, ErrDisabled
	}
	if s.secrets == nil {
		return map[sshsecret.Kind]bool{}, nil
	}
	return s.secrets.Status(ctx, alias)
}

// SetSecret stores one credential.
func (s *Service) SetSecret(ctx context.Context, alias string, kind sshsecret.Kind, value string) error {
	if !s.cfg.Enabled {
		return ErrDisabled
	}
	if s.secrets == nil {
		return errors.New("penyimpanan kredensial tidak aktif")
	}
	if _, err := s.Lookup(ctx, alias); err != nil {
		return err
	}
	if err := s.secrets.Set(ctx, alias, kind, value); err != nil {
		return err
	}
	return nil
}

// ClearSecret removes one credential.
func (s *Service) ClearSecret(ctx context.Context, alias string, kind sshsecret.Kind) error {
	if !s.cfg.Enabled {
		return ErrDisabled
	}
	if s.secrets == nil {
		return errors.New("penyimpanan kredensial tidak aktif")
	}
	return s.secrets.Clear(ctx, alias, kind)
}

// migrateAlias moves credentials and metadata when a host is renamed. Secrets
// are re-sealed because the alias is part of their associated data.
func (s *Service) migrateAlias(ctx context.Context, from, to string) {
	if s.secrets != nil {
		for _, kind := range sshsecret.Kinds() {
			value, ok, err := s.secrets.Get(ctx, from, kind)
			if err != nil || !ok {
				continue
			}
			if err := s.secrets.Set(ctx, to, kind, value); err != nil {
				s.log.Warn("ssh: gagal memindahkan kredensial", "dari", from, "ke", to, "error", err)
				continue
			}
			_ = s.secrets.Clear(ctx, from, kind)
		}
	}
	if m, ok, err := s.store.Get(ctx, from); err == nil && ok {
		m.Alias = to
		if err := s.store.Upsert(ctx, m); err != nil {
			s.log.Warn("ssh: gagal memindahkan metadata", "dari", from, "ke", to, "error", err)
		} else {
			_ = s.store.Delete(ctx, from)
		}
	}
}

// TestResult classifies a connection probe.
type TestResult struct {
	// Status is one of ok, auth_required, host_key_unknown, unreachable,
	// dns_error or error.
	Status     string `json:"status"`
	Message    string `json:"message"`
	DurationMs int64  `json:"durationMs"`
}

// Test probes whether a host is reachable. BatchMode makes ssh fail instead of
// prompting, so the probe can never hang on a password or a host key.
func (s *Service) Test(ctx context.Context, alias string) (*TestResult, error) {
	if !s.cfg.Enabled {
		return nil, ErrDisabled
	}
	if err := validateAlias(alias); err != nil {
		return nil, err
	}
	if _, err := s.Lookup(ctx, alias); err != nil {
		return nil, err
	}
	start := time.Now()
	out, err := s.run(ctx, 15*time.Second, s.cfg.SSHBin,
		"-F", s.cfg.ConfigPath,
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
		"-o", "NumberOfPasswordPrompts=0",
		alias, "true")
	result := &TestResult{DurationMs: time.Since(start).Milliseconds()}
	if err == nil {
		result.Status = "ok"
		result.Message = "terhubung dan terautentikasi"
		return result, nil
	}
	result.Status, result.Message = classifySSHError(out, err)
	return result, nil
}

// classifySSHError turns ssh output into a stable status the UI can act on.
func classifySSHError(out string, runErr error) (string, string) {
	text := strings.ToLower(out)
	switch {
	case strings.Contains(text, "host key verification failed"),
		strings.Contains(text, "remote host identification has changed"):
		return "host_key_unknown", "kunci host belum dipercaya atau berubah"
	case strings.Contains(text, "permission denied"),
		strings.Contains(text, "authentication failed"),
		strings.Contains(text, "no supported authentication methods"):
		return "auth_required", "host terjangkau tetapi autentikasi gagal (perlu password atau kunci)"
	case strings.Contains(text, "connection refused"):
		return "unreachable", "koneksi ditolak (port tertutup)"
	case strings.Contains(text, "connection timed out"), strings.Contains(text, "timed out"),
		strings.Contains(text, "no route to host"), strings.Contains(text, "network is unreachable"):
		return "unreachable", "host tidak menjawab (timeout/jaringan)"
	case strings.Contains(text, "could not resolve hostname"),
		strings.Contains(text, "name or service not known"),
		strings.Contains(text, "temporary failure in name resolution"):
		return "dns_error", "nama host tidak dapat diresolusi"
	case strings.Contains(text, "connection closed"), strings.Contains(text, "closed by remote host"):
		return "unreachable", "koneksi ditutup oleh host jarak jauh"
	}
	msg := firstLine(out)
	if msg == "" {
		msg = runErr.Error()
	}
	return "error", msg
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncate(strings.TrimSpace(s), 300)
}

// QuickAdd is the parsed form of an "ssh user@host -p 2222" style string.
type QuickAdd struct {
	Alias        string   `json:"alias"`
	HostName     string   `json:"hostName"`
	User         string   `json:"user"`
	Port         string   `json:"port"`
	IdentityFile string   `json:"identityFile"`
	ProxyJump    string   `json:"proxyJump"`
	Warnings     []string `json:"warnings"`
}

// QuickAdd parses a pasted ssh command line into host fields, mirroring the way
// editors let you add a host by typing the command you would run.
func (s *Service) QuickAdd(text string) QuickAdd {
	out := QuickAdd{}
	fields := splitCommandLine(text)
	if len(fields) > 0 && strings.EqualFold(fields[0], "ssh") {
		fields = fields[1:]
	}
	var target string
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		next := func() string {
			if i+1 < len(fields) {
				i++
				return fields[i]
			}
			return ""
		}
		switch {
		case f == "-p" || f == "--port":
			out.Port = next()
		case f == "-l":
			out.User = next()
		case f == "-i":
			out.IdentityFile = next()
		case f == "-J" || f == "--jump":
			out.ProxyJump = next()
		case strings.HasPrefix(f, "-p") && len(f) > 2:
			out.Port = strings.TrimPrefix(f, "-p")
		case strings.HasPrefix(f, "-l") && len(f) > 2:
			out.User = strings.TrimPrefix(f, "-l")
		case strings.HasPrefix(f, "-i") && len(f) > 2:
			out.IdentityFile = strings.TrimPrefix(f, "-i")
		case strings.HasPrefix(f, "-"):
			out.Warnings = append(out.Warnings,
				fmt.Sprintf("opsi %q diabaikan; tambahkan manual di bagian opsi lanjutan", f))
		default:
			if target == "" {
				target = f
			}
		}
	}
	if target == "" {
		out.Warnings = append(out.Warnings, "tidak ada host yang terdeteksi")
		return out
	}
	if user, host, ok := strings.Cut(target, "@"); ok {
		out.HostName = host
		if out.User == "" {
			out.User = user
		}
	} else {
		out.HostName = target
	}
	if host, port, ok := strings.Cut(out.HostName, ":"); ok {
		out.HostName = host
		if out.Port == "" {
			out.Port = port
		}
	}
	out.Alias = suggestAlias(out.HostName)
	return out
}

// suggestAlias derives a safe alias from a hostname.
func suggestAlias(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	return truncate(out, 128)
}

// splitCommandLine splits on whitespace while honouring single and double
// quotes, which is enough for the short command lines this parses.
func splitCommandLine(s string) []string {
	var (
		fields []string
		cur    strings.Builder
		quote  rune
	)
	flush := func() {
		if cur.Len() > 0 {
			fields = append(fields, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return fields
}

// mapWriteError translates config writer errors into service errors.
func mapWriteError(err error) error {
	switch {
	case errors.Is(err, sshconfig.ErrReadOnly):
		return fmt.Errorf("%w: %v", ErrReadOnly, err)
	case errors.Is(err, sshconfig.ErrNotFound):
		return fmt.Errorf("%w: %v", ErrUnknownHost, err)
	default:
		return err
	}
}
