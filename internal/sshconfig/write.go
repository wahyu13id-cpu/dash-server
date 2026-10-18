package sshconfig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Errors returned by the write operations.
var (
	// ErrNotFound means the alias is not defined as a concrete host.
	ErrNotFound = errors.New("host tidak ditemukan")
	// ErrReadOnly means the host lives in an included file, which the UI never
	// rewrites.
	ErrReadOnly = errors.New("host berada di file include (hanya baca)")
	// ErrDuplicate means another block already defines the alias.
	ErrDuplicate = errors.New("alias sudah dipakai")
	// ErrConflict means the file changed between reading and writing, so the
	// caller must reload before retrying.
	ErrConflict = errors.New("file konfigurasi berubah; muat ulang lalu coba lagi")
)

// ValidationError describes a rejected field.
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return e.Field + ": " + e.Message
}

// HostSpec is the desired content of a Host block as edited in the UI.
type HostSpec struct {
	Alias        string
	HostName     string
	User         string
	Port         string
	IdentityFile []string
	ProxyJump    string
	Comment      string
	Options      []Option
}

// Validate rejects anything that could produce a malformed or ambiguous block,
// or smuggle extra directives through a value.
func (s *HostSpec) Validate() error {
	if !aliasRe.MatchString(s.Alias) {
		return &ValidationError{
			Field:   "alias",
			Message: "hanya huruf, angka, titik, garis bawah dan strip (maks 128), tidak boleh diawali strip",
		}
	}
	if s.Port != "" {
		n, err := strconv.Atoi(s.Port)
		if err != nil || n < 1 || n > 65535 {
			return &ValidationError{Field: "port", Message: "harus angka 1-65535"}
		}
	}
	fields := []struct{ name, value string }{
		{"hostName", s.HostName},
		{"user", s.User},
		{"proxyJump", s.ProxyJump},
		{"comment", s.Comment},
	}
	for _, f := range fields {
		if err := checkValue(f.value); err != nil {
			return &ValidationError{Field: f.name, Message: err.Error()}
		}
	}
	for _, id := range s.IdentityFile {
		if err := checkValue(id); err != nil {
			return &ValidationError{Field: "identityFile", Message: err.Error()}
		}
	}
	if len(s.Options) > maxOptions {
		return &ValidationError{Field: "options", Message: fmt.Sprintf("maksimal %d opsi tambahan", maxOptions)}
	}
	for _, o := range s.Options {
		if !keyRe.MatchString(o.Key) {
			return &ValidationError{Field: "options", Message: fmt.Sprintf("nama opsi %q tidak valid", o.Key)}
		}
		if err := checkValue(o.Value); err != nil {
			return &ValidationError{Field: "options", Message: fmt.Sprintf("%s: %v", o.Key, err)}
		}
	}
	return nil
}

// checkValue rejects control characters, which are the only way a value could
// break out of its line or corrupt the file.
func checkValue(v string) error {
	if len(v) > maxValueLen {
		return fmt.Errorf("terlalu panjang (maks %d)", maxValueLen)
	}
	for _, r := range v {
		if r == '\n' || r == '\r' || r == 0 {
			return errors.New("mengandung karakter baris baru atau NUL")
		}
		if r < 0x20 && r != '\t' {
			return errors.New("mengandung karakter kontrol")
		}
	}
	return nil
}

// blockForAlias returns the first Host block defining alias as a literal
// pattern.
func (c *Config) blockForAlias(alias string) *Block {
	for _, b := range c.Blocks {
		if b.Kind != KindHost || b.Implied {
			continue
		}
		for _, p := range b.Patterns {
			if p == alias {
				return b
			}
		}
	}
	return nil
}

// Append adds a new Host block to the primary config, creating the file (and
// ~/.ssh) when needed.
func (c *Config) Append(spec HostSpec) (*Host, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if c.blockForAlias(spec.Alias) != nil {
		return nil, ErrDuplicate
	}
	lines, err := c.readPrimary()
	if err != nil {
		return nil, err
	}
	if n := len(lines); n > 0 && strings.TrimSpace(lines[n-1]) != "" {
		lines = append(lines, "")
	}
	lines = append(lines, formatBlock(spec, nil)...)
	if err := c.commit(lines); err != nil {
		return nil, err
	}
	if err := c.reload(); err != nil {
		return nil, err
	}
	return c.lookup(spec.Alias), nil
}

// Update replaces the block that defines alias. Renaming is supported: the
// alias token is rewritten in place so sibling patterns in a shared Host line
// are preserved.
func (c *Config) Update(alias string, spec HostSpec) (*Host, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	b := c.blockForAlias(alias)
	if b == nil {
		return nil, ErrNotFound
	}
	if !b.Writable {
		return nil, ErrReadOnly
	}
	if spec.Alias != alias && c.blockForAlias(spec.Alias) != nil {
		return nil, ErrDuplicate
	}
	lines, err := c.readPrimary()
	if err != nil {
		return nil, err
	}
	if err := verifyBlock(lines, b); err != nil {
		return nil, err
	}

	patterns := make([]string, 0, len(b.Patterns))
	for _, p := range b.Patterns {
		if p == alias {
			patterns = append(patterns, spec.Alias)
			continue
		}
		patterns = append(patterns, p)
	}

	start, end := blockRange(lines, b)
	out := make([]string, 0, len(lines)+len(patterns))
	out = append(out, lines[:start-1]...)
	out = append(out, formatBlock(spec, patterns)...)
	out = append(out, lines[end:]...)

	if err := c.commit(out); err != nil {
		return nil, err
	}
	if err := c.reload(); err != nil {
		return nil, err
	}
	return c.lookup(spec.Alias), nil
}

// Delete removes alias. When the Host line defines several aliases only the
// requested token is dropped; the block disappears once it is the last one.
func (c *Config) Delete(alias string) error {
	b := c.blockForAlias(alias)
	if b == nil {
		return ErrNotFound
	}
	if !b.Writable {
		return ErrReadOnly
	}
	lines, err := c.readPrimary()
	if err != nil {
		return err
	}
	if err := verifyBlock(lines, b); err != nil {
		return err
	}

	if len(b.Patterns) > 1 {
		remaining := make([]string, 0, len(b.Patterns)-1)
		for _, p := range b.Patterns {
			if p != alias {
				remaining = append(remaining, p)
			}
		}
		if len(remaining) > 0 {
			lines[b.StartLine-1] = "Host " + strings.Join(remaining, " ")
			if err := c.commit(lines); err != nil {
				return err
			}
			return c.reload()
		}
	}

	start, end := blockRange(lines, b)
	// Swallow one surrounding blank line so deleting does not leave a growing
	// gap of empty lines behind.
	if end < len(lines) && strings.TrimSpace(lines[end]) == "" {
		end++
	}
	if start > 1 && strings.TrimSpace(lines[start-2]) == "" {
		start--
	}

	out := make([]string, 0, len(lines))
	out = append(out, lines[:start-1]...)
	out = append(out, lines[end:]...)
	if err := c.commit(out); err != nil {
		return err
	}
	return c.reload()
}

// blockRange returns the 1-based inclusive line span to replace, including the
// comment line directly above the Host line when it belongs to the block.
func blockRange(lines []string, b *Block) (start, end int) {
	start, end = b.StartLine, b.EndLine
	if b.CommentLine > 0 && b.CommentLine == start-1 {
		start = b.CommentLine
	}
	// Guard against an out-of-range span after an external edit.
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start > end {
		start = end
	}
	return start, end
}

// verifyBlock re-checks that the recorded span still parses as the same Host
// line, which detects a concurrent external edit before we overwrite it.
func verifyBlock(lines []string, b *Block) error {
	if b.StartLine < 1 || b.StartLine > len(lines) {
		return ErrConflict
	}
	keyword, rest, ok := parseLine(lines[b.StartLine-1])
	if !ok || !strings.EqualFold(keyword, string(b.Kind)) {
		return ErrConflict
	}
	got := splitFields(rest)
	if len(got) != len(b.Patterns) {
		return ErrConflict
	}
	for i := range got {
		if got[i] != b.Patterns[i] {
			return ErrConflict
		}
	}
	return nil
}

// readPrimary reads the primary file as lines; a missing file yields nil.
func (c *Config) readPrimary() ([]string, error) {
	data, err := os.ReadFile(c.Primary)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return splitLines(data), nil
}

// commit backs up and atomically writes the primary file with mode 0600. The
// existing line-ending style is preserved so a CRLF file stays CRLF.
func (c *Config) commit(lines []string) error {
	dir := filepath.Dir(c.Primary)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("buat direktori %s: %w", dir, err)
	}
	eol := c.lineEnding()
	if _, err := os.Stat(c.Primary); err == nil {
		if err := backupFile(c.Primary); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
	}
	content := strings.Join(lines, eol)
	if len(lines) > 0 {
		content += eol
	}
	if err := writeFileAtomic(c.Primary, []byte(content), 0o600); err != nil {
		return fmt.Errorf("tulis %s: %w", c.Primary, err)
	}
	return nil
}

// lineEnding detects the newline style of the current file, defaulting to LF.
func (c *Config) lineEnding() string {
	data, err := os.ReadFile(c.Primary)
	if err != nil {
		return "\n"
	}
	if bytes.Contains(data, []byte("\r\n")) {
		return "\r\n"
	}
	return "\n"
}

// reload re-parses the config in place so the caller sees the new state.
func (c *Config) reload() error {
	fresh, err := Load(LoadOptions{Path: c.Primary, Home: c.Home})
	if err != nil {
		return err
	}
	*c = *fresh
	return nil
}

func (c *Config) lookup(alias string) *Host {
	h, _ := c.Lookup(alias)
	return h
}

// backupFile copies the current config to a timestamped sibling and prunes old
// backups, keeping the most recent maxBackups.
func backupFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	stamp := time.Now().Format("20060102-150405")
	dest := path + ".bak." + stamp
	for i := 1; i <= 5; i++ {
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			break
		}
		dest = fmt.Sprintf("%s.bak.%s-%d", path, stamp, i)
	}
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		return err
	}
	pruneBackups(path)
	return nil
}

func pruneBackups(path string) {
	matches, err := filepath.Glob(path + ".bak.*")
	if err != nil || len(matches) <= maxBackups {
		return
	}
	sort.Strings(matches)
	for _, m := range matches[:len(matches)-maxBackups] {
		_ = os.Remove(m)
	}
}

// writeFileAtomic writes data to a temporary file in the same directory and
// renames it into place, so a crash can never leave a truncated config.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".sshconfig-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() {
		if name != "" {
			_ = os.Remove(name)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	name = ""
	return nil
}

// formatBlock renders a Host block as config lines. patterns overrides the
// alias list, which preserves sibling patterns on update; when nil the spec's
// alias is used alone.
func formatBlock(spec HostSpec, patterns []string) []string {
	var out []string
	if comment := sanitizeComment(spec.Comment); comment != "" {
		out = append(out, "# "+comment)
	}
	pat := patterns
	if len(pat) == 0 {
		pat = []string{spec.Alias}
	}
	out = append(out, "Host "+strings.Join(pat, " "))
	if spec.HostName != "" {
		out = append(out, blockIndent+"HostName "+quoteValue(spec.HostName))
	}
	if spec.User != "" {
		out = append(out, blockIndent+"User "+quoteValue(spec.User))
	}
	if spec.Port != "" {
		out = append(out, blockIndent+"Port "+spec.Port)
	}
	for _, id := range spec.IdentityFile {
		if strings.TrimSpace(id) == "" {
			continue
		}
		out = append(out, blockIndent+"IdentityFile "+quoteValue(id))
	}
	if spec.ProxyJump != "" {
		out = append(out, blockIndent+"ProxyJump "+quoteValue(spec.ProxyJump))
	}
	for _, o := range spec.Options {
		if strings.TrimSpace(o.Key) == "" {
			continue
		}
		line := blockIndent + canonicalKey(o.Key)
		if v := strings.TrimSpace(o.Value); v != "" {
			// Extra directives are emitted verbatim: several of them
			// (ProxyCommand, LocalCommand, Match exec) take a whole command
			// line, and re-quoting such a value would change what ssh reports
			// through "ssh -G" even though it still runs.
			line += " " + quoteVerbatim(o.Value)
		}
		out = append(out, line)
	}
	return out
}

// quoteVerbatim keeps a multi-token directive value as written, quoting it only
// when leaving it bare would turn it into a comment.
func quoteVerbatim(v string) string {
	if strings.HasPrefix(v, "#") {
		return `"` + escapeQuoted(v) + `"`
	}
	return v
}

// escapeQuoted escapes a value for use inside double quotes.
func escapeQuoted(v string) string {
	escaped := strings.ReplaceAll(v, `\`, `\\`)
	return strings.ReplaceAll(escaped, `"`, `\"`)
}

// sanitizeComment collapses a comment to a single safe line.
func sanitizeComment(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.TrimSpace(strings.TrimLeft(s, "#"))
	return strings.TrimSpace(s)
}

// quoteValue quotes a value only when it would otherwise be misparsed: empty
// values, values starting with '#' (which would read as a comment) and values
// containing whitespace or quotes.
func quoteValue(v string) string {
	if v == "" {
		return `""`
	}
	if !strings.ContainsAny(v, " \t\"'\\") && !strings.HasPrefix(v, "#") {
		return v
	}
	return `"` + escapeQuoted(v) + `"`
}
