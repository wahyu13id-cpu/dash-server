package sysops

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LogEntry is one journal line, normalized for the UI.
type LogEntry struct {
	// Seq keeps the journal's own ordering (newest last).
	Seq        uint64    `json:"seq"`
	Time       time.Time `json:"time"`
	Priority   int       `json:"priority"`
	Unit       string    `json:"unit,omitempty"`
	Identifier string    `json:"identifier,omitempty"`
	PID        int       `json:"pid,omitempty"`
	Message    string    `json:"message"`
}

// JournalQuery selects journal entries.
type JournalQuery struct {
	// Unit restricts the query to one service unit ("" = whole journal).
	Unit string
	// Priority is a journalctl level: emerg, alert, crit, err, warning,
	// notice, info, debug ("" = all).
	Priority string
	// Lines is the maximum number of entries returned (newest last).
	Lines int
	// Since is a relative window: "1h", "30m", "7d", or boot/today/yesterday.
	Since string
	// Search is a literal, case-insensitive needle.
	Search string
}

// journalPriorities are the accepted --priority values.
var journalPriorities = map[string]bool{
	"":        true,
	"emerg":   true,
	"alert":   true,
	"crit":    true,
	"err":     true,
	"warning": true,
	"notice":  true,
	"info":    true,
	"debug":   true,
	// numeric aliases accepted by journalctl
	"0": true, "1": true, "2": true, "3": true, "4": true, "5": true, "6": true, "7": true,
}

var sinceRe = regexp.MustCompile(`^[0-9]{1,4}(s|m|h|d)$`)

// normalizeSince turns a compact window into a journalctl --since value.
func normalizeSince(v string) (string, bool) {
	v = strings.TrimSpace(v)
	switch v {
	case "":
		return "", true
	case "boot", "today", "yesterday":
		return v, true
	}
	if sinceRe.MatchString(v) {
		return "-" + v, true
	}
	return "", false
}

// Journal reads the systemd journal. It returns entries in chronological order
// so the UI can append and follow without re-sorting.
func (s *Service) Journal(ctx context.Context, q JournalQuery) ([]LogEntry, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	if !q.validUnit() {
		return nil, fmt.Errorf("%w: nama unit", ErrInvalidInput)
	}
	if !journalPriorities[q.Priority] {
		return nil, fmt.Errorf("%w: prioritas %q", ErrInvalidInput, q.Priority)
	}
	since, ok := normalizeSince(q.Since)
	if !ok {
		return nil, fmt.Errorf("%w: rentang waktu %q", ErrInvalidInput, q.Since)
	}

	lines := q.Lines
	if lines <= 0 {
		lines = 200
	}
	if lines > 2000 {
		lines = 2000
	}

	if _, err := exec.LookPath(s.cfg.JournalctlBin); err != nil {
		return nil, ErrUnsupported
	}

	args := []string{
		"--no-pager",
		"--output=json",
		"--lines=" + strconv.Itoa(lines),
	}
	if q.Unit != "" {
		args = append(args, "--unit="+q.Unit)
	}
	if q.Priority != "" {
		args = append(args, "--priority="+q.Priority)
	}
	if since != "" {
		args = append(args, "--since="+since)
	}
	if needle := strings.TrimSpace(q.Search); needle != "" {
		// QuoteMeta keeps the search literal: the user searches for text, not
		// for a regular expression, and a bad pattern can never error out.
		args = append(args, "--grep="+regexp.QuoteMeta(needle), "--case-sensitive=false")
	}

	res := s.run(ctx, s.cfg.JournalctlBin, args...)
	if res.Err != nil && strings.TrimSpace(res.Stdout) == "" {
		return nil, fmt.Errorf("journalctl: %s", firstLine(res.Stderr))
	}

	out := make([]LogEntry, 0, lines)
	sc := bufio.NewScanner(strings.NewReader(res.Stdout))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		entry, ok := parseJournalLine(line)
		if ok {
			out = append(out, entry)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.Before(out[j].Time)
		}
		return out[i].Seq < out[j].Seq
	})
	return out, nil
}

func (q JournalQuery) validUnit() bool {
	return q.Unit == "" || ValidUnitName(q.Unit)
}

// parseJournalLine decodes one `journalctl --output=json` object.
func parseJournalLine(line string) (LogEntry, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return LogEntry{}, false
	}
	e := LogEntry{}
	if v := jsonString(raw["__REALTIME_TIMESTAMP"]); v != "" {
		if micros, err := strconv.ParseInt(v, 10, 64); err == nil {
			e.Time = time.UnixMicro(micros)
		}
	}
	if v := jsonString(raw["PRIORITY"]); v != "" {
		e.Priority, _ = strconv.Atoi(v)
	}
	e.Unit = jsonString(raw["_SYSTEMD_UNIT"])
	if e.Unit == "" {
		e.Unit = jsonString(raw["_SYSTEMD_USER_UNIT"])
	}
	e.Identifier = jsonString(raw["SYSLOG_IDENTIFIER"])
	if e.Identifier == "" {
		e.Identifier = jsonString(raw["_COMM"])
	}
	if v := jsonString(raw["_PID"]); v != "" {
		e.PID, _ = strconv.Atoi(v)
	}
	if v := jsonString(raw["_SOURCE_REALTIME_TIMESTAMP"]); v != "" && e.Time.IsZero() {
		if micros, err := strconv.ParseInt(v, 10, 64); err == nil {
			e.Time = time.UnixMicro(micros)
		}
	}
	e.Message = jsonMessage(raw["MESSAGE"])
	if cursor := jsonString(raw["__CURSOR"]); cursor != "" {
		// The cursor carries the journal's own monotonic counter (the i= field),
		// which orders entries even when two share a timestamp.
		for _, part := range strings.Split(cursor, ";") {
			if !strings.HasPrefix(part, "i=") {
				continue
			}
			if n, err := strconv.ParseUint(part[2:], 16, 64); err == nil {
				e.Seq = n
			}
		}
	}
	if e.Time.IsZero() && e.Seq == 0 {
		return LogEntry{}, false
	}
	return e, true
}

// jsonString decodes a JSON scalar as a string, ignoring non-scalar values.
func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}

// jsonMessage handles both scalar and byte-array MESSAGE values. Journald
// stores binary payloads as an array of byte values.
func jsonMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if s := jsonString(raw); s != "" || string(raw) == `""` {
		return s
	}
	var bytes []int
	if err := json.Unmarshal(raw, &bytes); err == nil {
		buf := make([]byte, 0, len(bytes))
		for _, b := range bytes {
			if b >= 0 && b < 256 {
				buf = append(buf, byte(b))
			}
		}
		return string(buf)
	}
	return ""
}

// --- log files --------------------------------------------------------------

// LogSource is a selectable log origin: the system journal, one unit's journal
// or a file under the log directory.
type LogSource struct {
	Kind    string    `json:"kind"` // "journal" | "unit" | "file"
	Name    string    `json:"name"`
	Path    string    `json:"path,omitempty"`
	Unit    string    `json:"unit,omitempty"`
	Size    int64     `json:"size,omitempty"`
	ModTime time.Time `json:"modTime,omitempty"`
}

// LogSourceList is the payload of the log sources endpoint.
type LogSourceList struct {
	Sources []LogSource `json:"sources"`
	LogDir  string      `json:"logDir"`
}

// logFileRe matches the files the log viewer offers. Binary files (wtmp,
// lastlog, the journal directory) are deliberately absent.
var logFileRe = regexp.MustCompile(`(?i)^(syslog|messages|dmesg|kern\.log|auth\.log|daemon\.log|user\.log|debug|boot\.log|bootstrap\.log|dpkg\.log|alternatives\.log|faillog|ufw\.log|[a-z0-9._-]+\.log(\.[0-9]+)?)$`)

// LogSources lists everything the log page can show.
func (s *Service) LogSources(ctx context.Context) (LogSourceList, error) {
	if !s.Enabled() {
		return LogSourceList{}, ErrDisabled
	}
	out := LogSourceList{LogDir: s.cfg.LogDir, Sources: []LogSource{}}

	if s.hasSystemd() {
		out.Sources = append(out.Sources, LogSource{Kind: "journal", Name: "Journal sistem (semua)"})
		// One entry per service unit, so an admin can jump straight to a unit's
		// log without leaving the page.
		if list, err := s.ListServices(ctx); err == nil {
			units := make([]ServiceUnit, 0, len(list.Units))
			for _, u := range list.Units {
				// Only units that can actually log something. Template units
				// (foo@.service) have no instance yet, so their journal is empty.
				if u.Load == "not-found" || u.Load == "masked" || strings.Contains(u.Name, "@.") {
					continue
				}
				units = append(units, u)
			}
			sort.Slice(units, func(i, j int) bool { return units[i].Name < units[j].Name })
			for _, u := range units {
				out.Sources = append(out.Sources, LogSource{Kind: "unit", Name: u.Name, Unit: u.Name})
			}
		}
	}

	path, err := cleanPath(s.cfg.LogDir)
	if err != nil {
		return out, nil // no readable log dir: journal sources are still useful
	}
	root := path

	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > 2 || len(out.Sources) > 800 {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			full := filepath.Join(dir, e.Name())
			if e.IsDir() {
				// The systemd journal store is binary and huge: never offered.
				if e.Name() == "journal" || e.Name() == "private" {
					continue
				}
				walk(full, depth+1)
				continue
			}
			if !logFileRe.MatchString(e.Name()) {
				continue
			}
			// Symlinks are followed only when they stay inside the log dir.
			real, err := filepath.EvalSymlinks(full)
			if err != nil || !within(root, real) {
				continue
			}
			info, err := os.Stat(real)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			rel, err := filepath.Rel(root, real)
			if err != nil {
				rel = e.Name()
			}
			out.Sources = append(out.Sources, LogSource{
				Kind:    "file",
				Name:    filepath.ToSlash(rel),
				Path:    real,
				Size:    info.Size(),
				ModTime: info.ModTime(),
			})
		}
	}
	walk(root, 1)

	sort.Slice(out.Sources, func(i, j int) bool {
		if out.Sources[i].Kind != out.Sources[j].Kind {
			return out.Sources[i].Kind < out.Sources[j].Kind
		}
		return out.Sources[i].Name < out.Sources[j].Name
	})
	return out, nil
}

// FileLog is a window into a log file.
type FileLog struct {
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	Lines     []string  `json:"lines"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"modTime"`
	Truncated bool      `json:"truncated"`
}

// maxLogRead caps how much of a log file is read per request.
const maxLogRead = 1 << 20 // 1 MiB

// ReadLogFile returns the tail of a log file, optionally filtered.
func (s *Service) ReadLogFile(ctx context.Context, path string, lines int, search string) (FileLog, error) {
	if !s.Enabled() {
		return FileLog{}, ErrDisabled
	}
	root, err := cleanPath(s.cfg.LogDir)
	if err != nil {
		return FileLog{}, ErrUnsupported
	}
	real, err := cleanPath(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return FileLog{}, ErrNotFound
		}
		return FileLog{}, err
	}
	if !within(root, real) {
		return FileLog{}, fmt.Errorf("%w: hanya berkas di dalam %s", ErrInvalidInput, root)
	}

	if lines <= 0 {
		lines = 200
	}
	if lines > 2000 {
		lines = 2000
	}

	f, err := os.Open(real)
	if err != nil {
		if os.IsNotExist(err) {
			return FileLog{}, ErrNotFound
		}
		return FileLog{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return FileLog{}, err
	}
	if !info.Mode().IsRegular() {
		return FileLog{}, fmt.Errorf("%w: bukan berkas biasa", ErrInvalidInput)
	}

	size := info.Size()
	readFrom := int64(0)
	if size > maxLogRead {
		readFrom = size - maxLogRead
	}
	if _, err := f.Seek(readFrom, io.SeekStart); err != nil {
		return FileLog{}, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return FileLog{}, err
	}

	text := string(data)
	if readFrom > 0 {
		// Drop the first, possibly partial, line.
		if idx := strings.IndexByte(text, '\n'); idx >= 0 {
			text = text[idx+1:]
		}
	}
	all := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(all) == 1 && all[0] == "" {
		all = nil
	}

	needle := strings.ToLower(strings.TrimSpace(search))
	if needle != "" {
		filtered := make([]string, 0, len(all))
		for _, line := range all {
			if strings.Contains(strings.ToLower(line), needle) {
				filtered = append(filtered, line)
			}
		}
		all = filtered
	}

	truncated := false
	if len(all) > lines {
		all = all[len(all)-lines:]
		truncated = true
	}

	rel, relErr := filepath.Rel(root, real)
	if relErr != nil {
		rel = filepath.Base(real)
	}
	return FileLog{
		Path:      real,
		Name:      filepath.ToSlash(rel),
		Lines:     all,
		Size:      size,
		ModTime:   info.ModTime(),
		Truncated: truncated,
	}, nil
}
