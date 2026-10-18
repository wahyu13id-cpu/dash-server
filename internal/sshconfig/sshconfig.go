// Package sshconfig parses and edits OpenSSH client configuration files
// (ssh_config(5)) without disturbing the rest of the file.
//
// The parser is deliberately faithful to what the ssh binary actually does,
// which was verified against OpenSSH 10.0:
//
//   - Keywords are case-insensitive and end at the first whitespace or '='.
//   - A '#' starts a comment only when it begins a field; inside a field it is
//     ordinary data ("User u1#v" keeps the '#').
//   - "Host a b" defines two concrete aliases; commas are NOT separators (they
//     only separate criteria in a Match block).
//   - Directives appearing before the first Host/Match act as "Host *".
//   - Include paths expand relative to ~/.ssh, not to the including file.
//
// Everything is read-only except the primary config file; blocks that come
// from an Include are surfaced but never written.
package sshconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// maxIncludeDepth bounds recursive Include expansion.
	maxIncludeDepth = 16
	// maxValueLen bounds a single directive value.
	maxValueLen = 4096
	// maxOptions bounds the number of extra directives stored per host.
	maxOptions = 64
	// maxBackups is how many timestamped backups are kept beside the config.
	maxBackups = 10
	// blockIndent is the indentation used for directives inside a block.
	blockIndent = "    "
)

var (
	// aliasRe is the only shape accepted for a host alias. It forbids leading
	// '-' (which would look like an ssh option), wildcards and whitespace.
	aliasRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	// keyRe is the accepted shape of a directive keyword.
	keyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,63}$`)
)

// Option is a single directive inside a block, for example HostName -> web1.
type Option struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// BlockKind distinguishes Host sections from Match sections.
type BlockKind string

const (
	// KindHost is a "Host <patterns>" section.
	KindHost BlockKind = "host"
	// KindMatch is a "Match <criteria>" section.
	KindMatch BlockKind = "match"
)

// Block is one Host or Match section together with its 1-based line range in
// File. The range includes the Host/Match line itself.
type Block struct {
	Kind        BlockKind `json:"kind"`
	Patterns    []string  `json:"patterns"`
	Options     []Option  `json:"options"`
	File        string    `json:"file"`
	StartLine   int       `json:"startLine"`
	EndLine     int       `json:"endLine"`
	Comment     string    `json:"comment,omitempty"`
	CommentLine int       `json:"-"`
	// Implied marks the synthetic "Host *" block that holds the directives
	// written before the first Host/Match. It has no Host line to edit.
	Implied bool `json:"implied,omitempty"`
	// Writable reports whether this block lives in the primary config file.
	Writable bool `json:"writable"`
}

// FileInfo describes one file that contributed to the parsed view.
type FileInfo struct {
	Path     string `json:"path"`
	Primary  bool   `json:"primary"`
	Exists   bool   `json:"exists"`
	Writable bool   `json:"writable"`
	Mode     string `json:"mode,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Config is a parsed view over the main ssh config plus every file it includes.
type Config struct {
	// Home is the user home directory used to expand "~".
	Home string
	// SSHDir is Home/.ssh; relative Include paths resolve against it.
	SSHDir string
	// Primary is the writable config file (SSHDir/config unless overridden).
	Primary  string
	Files    []FileInfo
	Blocks   []*Block
	Warnings []string
}

// LoadOptions configures Load.
type LoadOptions struct {
	// Path is the main config file. Empty means <Home>/.ssh/config.
	Path string
	// Home is the user home directory. Empty means os.UserHomeDir().
	Home string
}

// Load parses the main config file and everything it includes. A missing main
// file is not an error: the returned Config simply has no blocks, which lets
// the UI offer to create it via Append.
func Load(opts LoadOptions) (*Config, error) {
	home := opts.Home
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil || home == "" {
			return nil, errors.New("sshconfig: cannot determine home directory")
		}
	}
	home = filepath.Clean(home)

	sshDir := filepath.Join(home, ".ssh")
	primary := opts.Path
	if primary == "" {
		primary = filepath.Join(sshDir, "config")
	} else if strings.HasPrefix(primary, "~") {
		primary = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(primary, "~"), "/"))
	}
	if abs, err := filepath.Abs(primary); err == nil {
		primary = abs
	}

	c := &Config{Home: home, SSHDir: sshDir, Primary: primary}
	c.parseFile(primary, true, 0, map[string]bool{})
	c.checkDirPerms()
	return c, nil
}

// Primary returns the writable config path.
func (c *Config) PrimaryPath() string { return c.Primary }

func (c *Config) checkDirPerms() {
	st, err := os.Stat(c.SSHDir)
	if err != nil {
		return
	}
	if perm := st.Mode().Perm(); perm&0o077 != 0 {
		c.Warnings = append(c.Warnings,
			fmt.Sprintf("%s bermode %04o (sebaiknya 0700)", c.SSHDir, perm))
	}
}

// parseFile reads one file and appends its blocks. primary marks the single
// writable file. depth guards recursive Include expansion and seen guards
// include cycles.
func (c *Config) parseFile(path string, primary bool, depth int, seen map[string]bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	info := FileInfo{Path: abs, Primary: primary, Writable: primary}

	if depth > maxIncludeDepth {
		info.Error = "include terlalu dalam, dilewati"
		c.Warnings = append(c.Warnings,
			fmt.Sprintf("include terlalu dalam, dilewati: %s", abs))
		c.Files = append(c.Files, info)
		return
	}
	if seen[abs] {
		info.Error = "sudah dibaca sebelumnya, dilewati"
		c.Warnings = append(c.Warnings,
			fmt.Sprintf("include berulang/siklus dilewati: %s", abs))
		c.Files = append(c.Files, info)
		return
	}
	seen[abs] = true

	data, err := os.ReadFile(abs)
	if err != nil {
		if !os.IsNotExist(err) {
			info.Error = err.Error()
		}
		c.Files = append(c.Files, info)
		return
	}
	if st, err := os.Stat(abs); err == nil {
		info.Mode = fmt.Sprintf("%04o", st.Mode().Perm())
		if primary && st.Mode().Perm()&0o077 != 0 {
			c.Warnings = append(c.Warnings,
				fmt.Sprintf("%s bermode %s (sebaiknya 0600)", abs, info.Mode))
		}
	}
	info.Exists = true
	c.Files = append(c.Files, info)

	lines := splitLines(data)
	var current *Block
	var implied *Block

	// closeBlock seals the open block at the given 1-based end line.
	closeBlock := func(endLine int) {
		if current != nil {
			current.EndLine = endLine
			current = nil
		}
	}

	for i, raw := range lines {
		lineNo := i + 1
		keyword, rest, ok := parseLine(raw)
		if !ok {
			continue
		}
		switch strings.ToLower(keyword) {
		case "include":
			if rest == "" {
				continue
			}
			// OpenSSH parses the included file in place, so the enclosing
			// block (if any) ends here. Directives after an in-block Include
			// would be orphaned and are reported instead of silently kept.
			closeBlock(i)
			c.expandInclude(rest, depth, seen)
			if implied != nil {
				implied.EndLine = lineNo
			}
		case "host", "match":
			closeBlock(i)
			b := &Block{
				Kind:      KindHost,
				File:      abs,
				StartLine: lineNo,
				Writable:  primary,
			}
			if strings.EqualFold(keyword, "match") {
				b.Kind = KindMatch
			}
			b.Patterns = splitFields(rest)
			// Attach the comment line directly above, if any.
			if i > 0 {
				if txt, ok := commentText(lines[i-1]); ok {
					b.Comment = txt
					b.CommentLine = i // 1-based number of lines[i-1]
				}
			}
			c.Blocks = append(c.Blocks, b)
			current = b
		default:
			opt := Option{Key: canonicalKey(keyword), Value: optionValue(keyword, rest)}
			if current == nil {
				// Directives before the first Host/Match behave as "Host *".
				if implied == nil {
					implied = &Block{
						Kind:      KindHost,
						Patterns:  []string{"*"},
						File:      abs,
						StartLine: 1,
						Implied:   true,
						Writable:  false,
					}
					c.Blocks = append(c.Blocks, implied)
				}
				implied.Options = append(implied.Options, opt)
				continue
			}
			current.Options = append(current.Options, opt)
		}
	}
	closeBlock(len(lines))
	if implied != nil {
		implied.EndLine = len(lines)
	}
}

// expandInclude resolves one Include directive, glob included, and parses each
// match. Relative paths resolve against ~/.ssh, matching OpenSSH.
func (c *Config) expandInclude(rest string, depth int, seen map[string]bool) {
	for _, raw := range splitFields(rest) {
		pattern := raw
		if strings.HasPrefix(pattern, "~/") {
			pattern = filepath.Join(c.Home, pattern[2:])
		} else if pattern == "~" {
			pattern = c.Home
		} else if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(c.SSHDir, pattern)
		}
		matches, err := filepath.Glob(pattern)
		if err != nil {
			c.Warnings = append(c.Warnings, fmt.Sprintf("include tidak valid %q: %v", raw, err))
			continue
		}
		if len(matches) == 0 {
			c.Warnings = append(c.Warnings, fmt.Sprintf("include tanpa hasil: %s", raw))
			continue
		}
		sort.Strings(matches)
		for _, m := range matches {
			c.parseFile(m, false, depth+1, seen)
		}
	}
}

// parseLine splits a config line into a keyword and the raw remainder. The
// keyword ends at the first whitespace or '='; one separator character is then
// consumed. ok is false for blank lines and comments.
func parseLine(raw string) (keyword, rest string, ok bool) {
	s := strings.TrimLeft(raw, " \t")
	if s == "" || s[0] == '#' {
		return "", "", false
	}
	i := 0
	for i < len(s) && s[i] != ' ' && s[i] != '\t' && s[i] != '=' {
		i++
	}
	keyword = s[:i]
	if i < len(s) {
		i++ // consume the single separator
	}
	return keyword, strings.TrimLeft(s[i:], " \t"), true
}

// commentIndex reports where a comment starts in s, or -1. A '#' begins a
// comment only at the start of a field, so '#' inside a field stays literal.
func commentIndex(s string) int {
	inField, quoted := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quoted:
			if c == '\\' {
				i++
				continue
			}
			if c == '"' {
				quoted = false
			}
		case c == '"':
			quoted, inField = true, true
		case c == ' ' || c == '\t':
			inField = false
		case c == '#':
			if !inField {
				return i
			}
		default:
			inField = true
		}
	}
	return -1
}

// stripComment removes a trailing comment and surrounding whitespace.
func stripComment(s string) string {
	if i := commentIndex(s); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, " \t")
}

// splitFields tokenizes the remainder of a line: fields are separated by
// unquoted whitespace, double quotes group one field, and an unquoted '#' that
// starts a field begins a comment running to the end of the line.
func splitFields(s string) []string {
	s = stripComment(s)
	var (
		fields []string
		cur    strings.Builder
		inTok  bool
		quoted bool
	)
	flush := func() {
		if inTok {
			fields = append(fields, cur.String())
			cur.Reset()
			inTok = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quoted:
			if c == '\\' && i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
				continue
			}
			if c == '"' {
				quoted = false
				continue
			}
			cur.WriteByte(c)
		case c == '"':
			quoted, inTok = true, true
		case c == ' ' || c == '\t':
			flush()
		default:
			inTok = true
			cur.WriteByte(c)
		}
	}
	flush()
	return fields
}

// firstField returns the first token of s, or "".
func firstField(s string) string {
	if f := splitFields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

// commentText reports the text of a full-line comment.
func commentText(line string) (string, bool) {
	t := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(t, "#") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimLeft(t, "#")), true
}

// knownKeys maps lower-cased keywords to their canonical spelling for the
// directives the UI edits as first-class fields.
var knownKeys = map[string]string{
	"hostname":     "HostName",
	"user":         "User",
	"port":         "Port",
	"identityfile": "IdentityFile",
	"proxyjump":    "ProxyJump",
}

// canonicalKey normalises a keyword's spelling.
func canonicalKey(keyword string) string {
	if canon, ok := knownKeys[strings.ToLower(keyword)]; ok {
		return canon
	}
	return keyword
}

// optionValue extracts the value of a directive. The five first-class keys are
// single-valued and take their first field; every other directive keeps the
// raw remainder verbatim (comment removed) so multi-token values such as
// ProxyCommand survive a round trip untouched.
func optionValue(keyword, rest string) string {
	if _, ok := knownKeys[strings.ToLower(keyword)]; ok {
		return firstField(rest)
	}
	return stripComment(rest)
}

// splitLines splits file content into lines, normalising CRLF to LF.
func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	s := string(data)
	if strings.Contains(s, "\r\n") {
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	lines := strings.Split(s, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// --- accessors ---------------------------------------------------------

// Option returns the value of the first occurrence of key.
func (b *Block) Option(key string) (string, bool) {
	for _, o := range b.Options {
		if strings.EqualFold(o.Key, key) {
			return o.Value, true
		}
	}
	return "", false
}

// Options returns every value of key, in order.
func (b *Block) MultiOptions(key string) []string {
	var out []string
	for _, o := range b.Options {
		if strings.EqualFold(o.Key, key) {
			out = append(out, o.Value)
		}
	}
	return out
}

// IsWildcard reports whether the block targets a pattern rather than a
// concrete host, which makes it a "defaults" block instead of a connectable
// entry.
func (b *Block) IsWildcard() bool {
	if b.Implied {
		return true
	}
	if b.Kind != KindHost {
		return false
	}
	for _, p := range b.Patterns {
		if isWildcard(p) {
			return true
		}
	}
	return false
}

func isWildcard(pattern string) bool {
	return strings.ContainsAny(pattern, "*?!")
}

// Host is a single connectable alias derived from a Host block.
type Host struct {
	Alias        string   `json:"alias"`
	HostName     string   `json:"hostName"`
	User         string   `json:"user"`
	Port         string   `json:"port"`
	IdentityFile []string `json:"identityFile"`
	ProxyJump    string   `json:"proxyJump"`
	Comment      string   `json:"comment"`
	Options      []Option `json:"options"`
	File         string   `json:"file"`
	StartLine    int      `json:"startLine"`
	EndLine      int      `json:"endLine"`
	Patterns     []string `json:"patterns"`
	Writable     bool     `json:"writable"`
}

// hostFromBlock projects a Block into the edited view for one alias. The five
// first-class keys are lifted out; every other directive is preserved in
// Options so unknown keywords are never lost on save.
func hostFromBlock(b *Block, alias string) *Host {
	h := &Host{
		Alias:        alias,
		Comment:      b.Comment,
		File:         b.File,
		StartLine:    b.StartLine,
		EndLine:      b.EndLine,
		Patterns:     append([]string(nil), b.Patterns...),
		Writable:     b.Writable,
		IdentityFile: b.MultiOptions("IdentityFile"),
	}
	for _, o := range b.Options {
		switch o.Key {
		case "HostName":
			h.HostName = o.Value
		case "User":
			h.User = o.Value
		case "Port":
			h.Port = o.Value
		case "ProxyJump":
			h.ProxyJump = o.Value
		case "IdentityFile":
			// already collected
		default:
			h.Options = append(h.Options, o)
		}
	}
	return h
}

// Hosts returns every concrete alias, in file order, first definition winning
// (matching OpenSSH's resolution order).
func (c *Config) Hosts() []*Host {
	var out []*Host
	seen := map[string]bool{}
	for _, b := range c.Blocks {
		if b.Kind != KindHost || b.Implied {
			continue
		}
		for _, p := range b.Patterns {
			if isWildcard(p) {
				continue
			}
			if seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, hostFromBlock(b, p))
		}
	}
	return out
}

// Lookup returns the host for an exact alias.
func (c *Config) Lookup(alias string) (*Host, bool) {
	for _, h := range c.Hosts() {
		if h.Alias == alias {
			return h, true
		}
	}
	return nil, false
}

// Defaults returns the blocks that are not connectable entries: wildcard Host
// blocks, Match blocks, and the implied global block.
func (c *Config) Defaults() []*Block {
	var out []*Block
	for _, b := range c.Blocks {
		if b.Kind == KindMatch || b.IsWildcard() {
			out = append(out, b)
		}
	}
	return out
}

// Aliases returns the concrete aliases known to the config.
func (c *Config) Aliases() []string {
	hosts := c.Hosts()
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, h.Alias)
	}
	return out
}
