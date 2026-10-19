package sysops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ServiceUnit is one systemd service unit as shown in the services table.
type ServiceUnit struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Load         string `json:"load"`
	Active       string `json:"active"`
	Sub          string `json:"sub"`
	UnitFile     string `json:"unitFile"`
	FragmentPath string `json:"fragmentPath,omitempty"`
	MainPID      int    `json:"mainPid,omitempty"`
	MemoryBytes  uint64 `json:"memoryBytes,omitempty"`
	Since        string `json:"since,omitempty"`
}

// ServiceList is the payload of the services endpoint.
type ServiceList struct {
	Units   []ServiceUnit `json:"units"`
	Total   int           `json:"total"`
	Running int           `json:"running"`
	Failed  int           `json:"failed"`
	// CollectedAt lets the UI show how fresh the snapshot is.
	CollectedAt time.Time `json:"collectedAt"`
}

// serviceActions are the only verbs accepted by Action.
var serviceActions = map[string]bool{
	"start":       true,
	"stop":        true,
	"restart":     true,
	"reload":      true,
	"try-restart": true,
	"enable":      true,
	"disable":     true,
	"mask":        true,
	"unmask":      true,
}

// ValidServiceAction reports whether action is an allowed systemctl verb.
func ValidServiceAction(action string) bool { return serviceActions[action] }

// ListServices returns every installed service with its runtime and unit-file
// state. Two commands are used because `list-units` only knows loaded units
// while `list-unit-files` knows everything installed (including disabled ones).
func (s *Service) ListServices(ctx context.Context) (ServiceList, error) {
	if !s.Enabled() {
		return ServiceList{}, ErrDisabled
	}
	if !s.hasSystemd() {
		return ServiceList{}, ErrUnsupported
	}

	units := map[string]*ServiceUnit{}
	order := []string{}

	add := func(name string) *ServiceUnit {
		if u, ok := units[name]; ok {
			return u
		}
		u := &ServiceUnit{Name: name}
		units[name] = u
		order = append(order, name)
		return u
	}

	// Runtime state (in-memory units, including inactive/failed ones).
	res := s.run(ctx, s.cfg.SystemctlBin,
		"list-units", "--type=service", "--all", "--no-pager", "--plain", "--no-legend")
	if res.Err != nil && strings.TrimSpace(res.Stdout) == "" {
		return ServiceList{}, fmt.Errorf("systemctl list-units: %w", res.Err)
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 4 || !strings.HasSuffix(fields[0], ".service") {
			continue
		}
		u := add(fields[0])
		u.Load = fields[1]
		u.Active = fields[2]
		u.Sub = fields[3]
		if len(fields) > 4 {
			u.Description = strings.Join(fields[4:], " ")
		}
	}

	// Unit-file state (enabled/disabled/static/masked) for all installed units.
	res = s.run(ctx, s.cfg.SystemctlBin,
		"list-unit-files", "--type=service", "--no-pager", "--plain", "--no-legend")
	if res.Err == nil {
		for _, line := range strings.Split(res.Stdout, "\n") {
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) < 2 || !strings.HasSuffix(fields[0], ".service") {
				continue
			}
			u := add(fields[0])
			u.UnitFile = fields[1]
		}
	}

	out := ServiceList{Units: make([]ServiceUnit, 0, len(order)), CollectedAt: time.Now()}
	for _, name := range order {
		u := units[name]
		if u.Active == "" {
			u.Active = "inactive"
			u.Sub = "dead"
		}
		if u.Active == "active" {
			out.Running++
		}
		if u.Active == "failed" || u.Sub == "failed" {
			out.Failed++
		}
		out.Units = append(out.Units, *u)
	}
	sort.SliceStable(out.Units, func(i, j int) bool {
		return out.Units[i].Name < out.Units[j].Name
	})
	out.Total = len(out.Units)
	return out, nil
}

// ServiceDetail is the expanded view of one unit.
type ServiceDetail struct {
	Unit       ServiceUnit       `json:"unit"`
	Properties map[string]string `json:"properties"`
	// Journal holds the most recent log lines for this unit.
	Journal []LogEntry `json:"journal"`
}

// serviceDetailProps are the `systemctl show` properties the UI displays.
var serviceDetailProps = []string{
	"Id", "Description", "LoadState", "ActiveState", "SubState", "UnitFileState",
	"FragmentPath", "MainPID", "ExecMainStartTimestamp", "ExecMainExitTimestamp",
	"MemoryCurrent", "TasksCurrent", "User", "Group", "Restart", "WantedBy",
	"NRestarts", "ExecStart", "Documentation",
}

// ServiceDetail returns the properties and recent logs of one unit.
func (s *Service) ServiceDetail(ctx context.Context, name string) (ServiceDetail, error) {
	if !s.Enabled() {
		return ServiceDetail{}, ErrDisabled
	}
	if !ValidUnitName(name) {
		return ServiceDetail{}, fmt.Errorf("%w: nama unit", ErrInvalidInput)
	}
	if !s.hasSystemd() {
		return ServiceDetail{}, ErrUnsupported
	}

	args := []string{"show", name, "--no-pager"}
	for _, p := range serviceDetailProps {
		args = append(args, "--property="+p)
	}
	res := s.run(ctx, s.cfg.SystemctlBin, args...)
	if res.Err != nil {
		return ServiceDetail{}, fmt.Errorf("systemctl show: %w (%s)", res.Err, firstLine(res.Stderr))
	}

	props := map[string]string{}
	for _, line := range strings.Split(res.Stdout, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		props[key] = strings.TrimSpace(val)
	}

	unit := ServiceUnit{
		Name:         name,
		Description:  props["Description"],
		Load:         props["LoadState"],
		Active:       props["ActiveState"],
		Sub:          props["SubState"],
		UnitFile:     props["UnitFileState"],
		FragmentPath: props["FragmentPath"],
		Since:        props["ExecMainStartTimestamp"],
	}
	unit.MainPID, _ = strconv.Atoi(props["MainPID"])
	// MemoryCurrent is "n/a" (or a huge sentinel) for units without a cgroup.
	if v := props["MemoryCurrent"]; v != "" && v != "[not set]" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil && n < 1<<62 {
			unit.MemoryBytes = n
		}
	}

	entries, _ := s.Journal(ctx, JournalQuery{Unit: name, Lines: 100})
	return ServiceDetail{Unit: unit, Properties: props, Journal: entries}, nil
}

// ControlService runs a systemctl verb against one unit.
func (s *Service) ControlService(ctx context.Context, name, action string) (string, error) {
	if !s.Enabled() {
		return "", ErrDisabled
	}
	if !ValidUnitName(name) {
		return "", fmt.Errorf("%w: nama unit", ErrInvalidInput)
	}
	if !ValidServiceAction(action) {
		return "", fmt.Errorf("%w: aksi %q", ErrInvalidInput, action)
	}
	if !s.hasSystemd() {
		return "", ErrUnsupported
	}
	res := s.run(ctx, s.cfg.SystemctlBin, action, name)
	out := strings.TrimSpace(res.combined())
	if res.Err != nil {
		if out == "" {
			out = res.Err.Error()
		}
		return out, fmt.Errorf("systemctl %s %s: %s", action, name, firstLine(out))
	}
	if out == "" {
		out = fmt.Sprintf("%s %s: selesai", action, name)
	}
	return out, nil
}

// firstLine returns the first non-empty line of s, for short error messages.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// ErrNotFound marks a missing unit or file.
var ErrNotFound = errors.New("tidak ditemukan")
