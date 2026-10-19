package sysops

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// PackageUpdate is one upgradable package.
type PackageUpdate struct {
	Name      string `json:"name"`
	Current   string `json:"current"`
	Candidate string `json:"candidate"`
	// Security marks updates coming from a *-security origin.
	Security bool   `json:"security"`
	Origin   string `json:"origin,omitempty"`
	// Action is the package manager verb: upgrade, install or remove.
	Action string `json:"action,omitempty"`
}

// PackageStatus is the payload of the packages endpoint.
type PackageStatus struct {
	Manager        string          `json:"manager"`
	Upgradable     []PackageUpdate `json:"upgradable"`
	Count          int             `json:"count"`
	SecurityCount  int             `json:"securityCount"`
	Removals       int             `json:"removals"`
	RebootRequired bool            `json:"rebootRequired"`
	RebootPackages []string        `json:"rebootPackages,omitempty"`
	CheckedAt      time.Time       `json:"checkedAt"`
	// Stale reports that the entry is older than packageCacheTTL.
	Stale bool `json:"stale"`
	// Error explains a failed probe without hiding the cached numbers.
	Error string `json:"error,omitempty"`
}

// packageCacheTTL is how long a simulated upgrade result is reused. The
// simulation shells out to the package manager, so it must not run per request.
const packageCacheTTL = 5 * time.Minute

// RebootRequired reports whether the host asks for a reboot after updates.
func (s *Service) RebootRequired() bool {
	for _, p := range []string{"/var/run/reboot-required", "/run/reboot-required"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// rebootPackages lists the packages that requested the reboot.
func rebootPackages() []string {
	data, err := os.ReadFile("/var/run/reboot-required.pkgs")
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		name := strings.TrimSpace(line)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// PackageStatus returns the current update picture, using a short-lived cache
// unless force is set.
func (s *Service) PackageStatus(ctx context.Context, force bool) (PackageStatus, error) {
	if !s.Enabled() {
		return PackageStatus{}, ErrDisabled
	}
	s.pkgMu.Lock()
	cached := s.pkgCache
	s.pkgMu.Unlock()

	if cached != nil && !force && time.Since(cached.CheckedAt) < packageCacheTTL {
		clone := *cached
		clone.Stale = false
		return clone, nil
	}

	manager := s.packageManager()
	if manager == "" {
		return PackageStatus{}, ErrUnsupported
	}

	status := PackageStatus{
		Manager:        manager,
		Upgradable:     []PackageUpdate{},
		RebootRequired: s.RebootRequired(),
		RebootPackages: rebootPackages(),
		CheckedAt:      time.Now(),
	}

	switch manager {
	case "apt":
		updates, removals, err := s.aptUpgradable(ctx)
		if err != nil {
			status.Error = err.Error()
			// Keeping the previous numbers beats showing nothing.
			if cached != nil {
				old := *cached
				old.Stale = true
				old.Error = status.Error
				return old, nil
			}
		}
		status.Upgradable = updates
		status.Removals = removals
	default: // dnf / yum
		updates, err := s.dnfUpgradable(ctx, manager)
		if err != nil {
			status.Error = err.Error()
			if cached != nil {
				old := *cached
				old.Stale = true
				old.Error = status.Error
				return old, nil
			}
		}
		status.Upgradable = updates
	}

	status.Count = len(status.Upgradable)
	if status.Upgradable == nil {
		status.Upgradable = []PackageUpdate{}
	}
	for _, u := range status.Upgradable {
		if u.Security {
			status.SecurityCount++
		}
	}

	if status.Error == "" {
		s.pkgMu.Lock()
		s.pkgCache = &status
		s.pkgMu.Unlock()
	}
	return status, nil
}

// aptInstRe matches the "Inst" lines of `apt-get -s dist-upgrade`:
//
//	Inst libc6 [2.36-9] (2.36-9+deb12u7 Debian-Security:12/stable [amd64])
var aptInstRe = regexp.MustCompile(`^Inst\s+(\S+)\s+(?:\[([^\]]+)\]\s+)?\(([^)]+)\)`)

// aptUpgradable simulates a dist-upgrade so the dashboard knows exactly what
// would happen, including security origins and removals.
func (s *Service) aptUpgradable(ctx context.Context) ([]PackageUpdate, int, error) {
	res := s.run(ctx, "apt-get", "-s", "-o", "Debug::NoLocking=1", "-o", "APT::Get::List-Cleanup=0", "dist-upgrade")
	if res.Err != nil && strings.TrimSpace(res.Stdout) == "" {
		// Fall back to the lighter, cached listing.
		return s.aptListUpgradable(ctx)
	}

	var updates []PackageUpdate
	removals := 0
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Remv ") {
			removals++
			continue
		}
		m := aptInstRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		candidateField := strings.TrimSpace(m[3])
		parts := strings.Fields(candidateField)
		candidate := ""
		if len(parts) > 0 {
			candidate = parts[0]
		}
		origin := strings.TrimSpace(strings.TrimPrefix(candidateField, candidate))
		updates = append(updates, PackageUpdate{
			Name:      m[1],
			Current:   m[2],
			Candidate: candidate,
			Origin:    origin,
			Security:  strings.Contains(strings.ToLower(origin), "security"),
			Action:    "upgrade",
		})
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].Name < updates[j].Name })
	return updates, removals, nil
}

// aptListUpgradable reads `apt list --upgradable`, which uses only the cached
// package lists.
func (s *Service) aptListUpgradable(ctx context.Context) ([]PackageUpdate, int, error) {
	res := s.run(ctx, "apt-get", "-s", "-o", "Debug::NoLocking=1", "upgrade")
	if strings.TrimSpace(res.Stdout) == "" {
		if res.Err != nil {
			return nil, 0, fmt.Errorf("apt-get -s upgrade: %s", firstLine(res.Stderr))
		}
		return nil, 0, nil
	}
	var updates []PackageUpdate
	for _, line := range strings.Split(res.Stdout, "\n") {
		m := aptInstRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		candidateField := strings.TrimSpace(m[3])
		parts := strings.Fields(candidateField)
		candidate, origin := "", ""
		if len(parts) > 0 {
			candidate = parts[0]
			origin = strings.TrimSpace(strings.TrimPrefix(candidateField, candidate))
		}
		updates = append(updates, PackageUpdate{
			Name:      m[1],
			Current:   m[2],
			Candidate: candidate,
			Origin:    origin,
			Security:  strings.Contains(strings.ToLower(origin), "security"),
			Action:    "upgrade",
		})
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].Name < updates[j].Name })
	return updates, 0, nil
}

// dnfUpgradable lists pending updates for dnf/yum.
func (s *Service) dnfUpgradable(ctx context.Context, manager string) ([]PackageUpdate, error) {
	args := []string{"-q", "check-update"}
	if manager == "yum" {
		args = []string{"-q", "check-update"}
	}
	res := s.run(ctx, manager, args...)
	// dnf exits 100 when updates are available, which is not an error here.
	var updates []PackageUpdate
	security := s.dnfSecuritySet(ctx, manager)
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || strings.Contains(fields[0], "=") {
			continue
		}
		name := fields[0]
		arch := ""
		if idx := strings.LastIndex(name, "."); idx > 0 {
			arch = name[idx+1:]
			name = name[:idx]
		}
		_ = arch
		updates = append(updates, PackageUpdate{
			Name:      name,
			Candidate: fields[1],
			Origin:    fields[2],
			Security:  security[name],
			Action:    "upgrade",
		})
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].Name < updates[j].Name })
	return updates, nil
}

// dnfSecuritySet returns the names of packages with pending security errata.
func (s *Service) dnfSecuritySet(ctx context.Context, manager string) map[string]bool {
	out := map[string]bool{}
	if manager != "dnf" {
		return out
	}
	res := s.run(ctx, "dnf", "-q", "updateinfo", "list", "security")
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		// The package name is the last column, e.g. "kernel-6.1.0.x86_64".
		name := fields[len(fields)-1]
		if idx := strings.LastIndex(name, "."); idx > 0 {
			name = name[:idx]
		}
		out[name] = true
	}
	return out
}

// --- package jobs ------------------------------------------------------------

// RefreshPackages updates the package manager metadata in the background.
func (s *Service) RefreshPackages(ctx context.Context) (*Job, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	switch s.packageManager() {
	case "apt":
		return s.startJob("packages.refresh", "apt-get update", 5*time.Minute, "apt-get", "update"), nil
	case "dnf":
		return s.startJob("packages.refresh", "dnf makecache", 5*time.Minute, "dnf", "makecache"), nil
	case "yum":
		return s.startJob("packages.refresh", "yum makecache", 5*time.Minute, "yum", "makecache"), nil
	default:
		return nil, ErrUnsupported
	}
}

// UpgradePackages installs pending updates in the background. dist selects
// dist-upgrade (may install and remove packages), otherwise a plain upgrade is
// used, which never removes installed packages.
func (s *Service) UpgradePackages(ctx context.Context, dist bool) (*Job, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	manager := s.packageManager()
	if manager == "" {
		return nil, ErrUnsupported
	}

	// Any upgrade invalidates the cached picture.
	s.pkgMu.Lock()
	s.pkgCache = nil
	s.pkgMu.Unlock()

	switch manager {
	case "apt":
		verb := "upgrade"
		label := "apt-get upgrade"
		if dist {
			verb = "dist-upgrade"
			label = "apt-get dist-upgrade"
		}
		return s.startJob("packages.upgrade", label, 30*time.Minute,
			"apt-get", "-y", "-o", "Dpkg::Options::=--force-confold", "-o", "Dpkg::Options::=--force-confdef", verb), nil
	case "dnf", "yum":
		return s.startJob("packages.upgrade", manager+" -y upgrade", 30*time.Minute, manager, "-y", "upgrade"), nil
	default:
		return nil, ErrUnsupported
	}
}
