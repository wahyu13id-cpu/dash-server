package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"dashboard/internal/audit"
	"dashboard/internal/sysops"
)

// This file wires the host-operations endpoints (systemd services, processes,
// logs, package updates) onto the sysops package. Every destructive action is
// audited; every read stays cheap because the UI polls some of them.

// mapOpsError converts a sysops error into an HTTP status and a stable code.
func mapOpsError(err error) (int, string, string) {
	switch {
	case err == nil:
		return http.StatusOK, "", ""
	case errors.Is(err, sysops.ErrDisabled):
		return http.StatusServiceUnavailable, CodeUnavailable, "fitur operasi sistem dinonaktifkan"
	case errors.Is(err, sysops.ErrUnsupported):
		return http.StatusServiceUnavailable, CodeUnavailable, "tidak didukung oleh sistem ini"
	case errors.Is(err, sysops.ErrNotFound):
		return http.StatusNotFound, CodeNotFound, "tidak ditemukan"
	case errors.Is(err, sysops.ErrInvalidInput):
		return http.StatusBadRequest, CodeBadRequest, err.Error()
	default:
		return http.StatusInternalServerError, CodeInternal, err.Error()
	}
}

// writeOpsError maps err and, for rejected input, records the denial so a
// probe of the API is visible in the audit trail.
func (s *Server) writeOpsError(w http.ResponseWriter, r *http.Request, err error, action string) {
	status, code, msg := mapOpsError(err)
	if status == http.StatusBadRequest || status == http.StatusForbidden {
		entry := audit.Entry{
			Action:  audit.ActionSecurityDenied,
			Target:  r.URL.Path,
			Detail:  action + ": " + err.Error(),
			IP:      s.clientIP(r),
			Success: false,
		}
		if u := userFrom(r.Context()); u != nil {
			entry.UserID, entry.Username = u.ID, u.Username
		}
		s.audit.Log(r.Context(), entry)
	}
	writeError(w, status, code, msg)
}

// opsOrNil returns the operations service, guarding against a nil dependency.
func (s *Server) opsOrNil() *sysops.Service {
	if s.ops == nil || !s.ops.Enabled() {
		return nil
	}
	return s.ops
}

// handleOpsAvailability reports which operations the host supports.
func (s *Server) handleOpsAvailability(w http.ResponseWriter, r *http.Request) {
	if s.ops == nil {
		writeJSON(w, http.StatusOK, sysops.Availability{})
		return
	}
	writeJSON(w, http.StatusOK, s.ops.Availability(r.Context()))
}

// --- systemd services -------------------------------------------------------

// handleOpsServices lists every installed service unit.
func (s *Server) handleOpsServices(w http.ResponseWriter, r *http.Request) {
	svc := s.opsOrNil()
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUnavailable, "fitur operasi sistem dinonaktifkan")
		return
	}
	list, err := svc.ListServices(r.Context())
	if err != nil {
		s.writeOpsError(w, r, err, "list services")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleOpsServiceDetail returns properties plus recent journal lines for one
// unit.
func (s *Server) handleOpsServiceDetail(w http.ResponseWriter, r *http.Request) {
	svc := s.opsOrNil()
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUnavailable, "fitur operasi sistem dinonaktifkan")
		return
	}
	detail, err := svc.ServiceDetail(r.Context(), r.PathValue("name"))
	if err != nil {
		s.writeOpsError(w, r, err, "service detail")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

type serviceActionRequest struct {
	Action string `json:"action"`
}

// handleOpsServiceAction starts, stops, restarts or enables a unit.
func (s *Server) handleOpsServiceAction(w http.ResponseWriter, r *http.Request) {
	svc := s.opsOrNil()
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUnavailable, "fitur operasi sistem dinonaktifkan")
		return
	}
	name := r.PathValue("name")
	var req serviceActionRequest
	if err := decodeJSON(w, r, &req, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	out, err := svc.ControlService(r.Context(), name, strings.TrimSpace(req.Action))
	if err != nil {
		s.audit.Log(r.Context(), audit.Entry{
			Action: audit.ActionServiceControl, Target: name,
			Detail: req.Action + ": " + err.Error(), IP: s.clientIP(r), Success: false,
		})
		s.writeOpsError(w, r, err, "service action")
		return
	}
	s.audit.Log(r.Context(), audit.Entry{
		Action: audit.ActionServiceControl, Target: name, Detail: req.Action,
		IP: s.clientIP(r), Success: true,
	})
	writeJSON(w, http.StatusOK, map[string]string{"action": req.Action, "unit": name, "output": out})
}

// --- processes --------------------------------------------------------------

// handleOpsProcesses returns the process table.
func (s *Server) handleOpsProcesses(w http.ResponseWriter, r *http.Request) {
	svc := s.opsOrNil()
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUnavailable, "fitur operasi sistem dinonaktifkan")
		return
	}
	q := r.URL.Query()
	limit := 0
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 5000 {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "limit tidak valid")
			return
		}
		limit = n
	}
	list, err := svc.ListProcesses(r.Context(), q.Get("sort"), q.Get("q"), q.Get("user"), limit)
	if err != nil {
		s.writeOpsError(w, r, err, "list processes")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type signalProcessRequest struct {
	Signal string `json:"signal"`
}

// handleOpsProcessSignal sends an allow-listed signal to a pid.
func (s *Server) handleOpsProcessSignal(w http.ResponseWriter, r *http.Request) {
	svc := s.opsOrNil()
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUnavailable, "fitur operasi sistem dinonaktifkan")
		return
	}
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil || pid <= 0 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "pid tidak valid")
		return
	}
	var req signalProcessRequest
	if err := decodeJSON(w, r, &req, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	if err := svc.SignalProcess(pid, req.Signal); err != nil {
		s.audit.Log(r.Context(), audit.Entry{
			Action: audit.ActionProcessSignal, Target: strconv.Itoa(pid),
			Detail: req.Signal + ": " + err.Error(), IP: s.clientIP(r), Success: false,
		})
		s.writeOpsError(w, r, err, "process signal")
		return
	}
	s.audit.Log(r.Context(), audit.Entry{
		Action: audit.ActionProcessSignal, Target: strconv.Itoa(pid), Detail: req.Signal,
		IP: s.clientIP(r), Success: true,
	})
	writeJSON(w, http.StatusOK, map[string]any{"pid": pid, "signal": strings.ToUpper(strings.TrimSpace(req.Signal))})
}

type niceProcessRequest struct {
	Nice int `json:"nice"`
}

// handleOpsProcessNice changes a process scheduling priority.
func (s *Server) handleOpsProcessNice(w http.ResponseWriter, r *http.Request) {
	svc := s.opsOrNil()
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUnavailable, "fitur operasi sistem dinonaktifkan")
		return
	}
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil || pid <= 0 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "pid tidak valid")
		return
	}
	var req niceProcessRequest
	if err := decodeJSON(w, r, &req, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	if err := svc.ReniceProcess(pid, req.Nice); err != nil {
		s.audit.Log(r.Context(), audit.Entry{
			Action: audit.ActionProcessNice, Target: strconv.Itoa(pid),
			Detail: strconv.Itoa(req.Nice) + ": " + err.Error(), IP: s.clientIP(r), Success: false,
		})
		s.writeOpsError(w, r, err, "process nice")
		return
	}
	s.audit.Log(r.Context(), audit.Entry{
		Action: audit.ActionProcessNice, Target: strconv.Itoa(pid), Detail: strconv.Itoa(req.Nice),
		IP: s.clientIP(r), Success: true,
	})
	writeJSON(w, http.StatusOK, map[string]any{"pid": pid, "nice": req.Nice})
}

// --- logs -------------------------------------------------------------------

// handleOpsLogSources lists the journal and the readable files under the log
// directory.
func (s *Server) handleOpsLogSources(w http.ResponseWriter, r *http.Request) {
	svc := s.opsOrNil()
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUnavailable, "fitur operasi sistem dinonaktifkan")
		return
	}
	sources, err := svc.LogSources(r.Context())
	if err != nil {
		s.writeOpsError(w, r, err, "log sources")
		return
	}
	writeJSON(w, http.StatusOK, sources)
}

// handleOpsJournal reads the systemd journal.
func (s *Server) handleOpsJournal(w http.ResponseWriter, r *http.Request) {
	svc := s.opsOrNil()
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUnavailable, "fitur operasi sistem dinonaktifkan")
		return
	}
	q := r.URL.Query()
	lines, err := queryInt(q.Get("lines"), 0, 2000, 200)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "lines tidak valid")
		return
	}
	entries, err := svc.Journal(r.Context(), sysops.JournalQuery{
		Unit:     strings.TrimSpace(q.Get("unit")),
		Priority: strings.TrimSpace(q.Get("priority")),
		Lines:    lines,
		Since:    strings.TrimSpace(q.Get("since")),
		Search:   q.Get("search"),
	})
	if err != nil {
		s.writeOpsError(w, r, err, "journal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// handleOpsLogFile returns the tail of one log file.
func (s *Server) handleOpsLogFile(w http.ResponseWriter, r *http.Request) {
	svc := s.opsOrNil()
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUnavailable, "fitur operasi sistem dinonaktifkan")
		return
	}
	q := r.URL.Query()
	lines, err := queryInt(q.Get("lines"), 0, 2000, 200)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "lines tidak valid")
		return
	}
	file, err := svc.ReadLogFile(r.Context(), strings.TrimSpace(q.Get("path")), lines, q.Get("search"))
	if err != nil {
		s.writeOpsError(w, r, err, "read log file")
		return
	}
	writeJSON(w, http.StatusOK, file)
}

// --- package updates --------------------------------------------------------

// handleOpsPackages returns the pending-update picture.
func (s *Server) handleOpsPackages(w http.ResponseWriter, r *http.Request) {
	svc := s.opsOrNil()
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUnavailable, "fitur operasi sistem dinonaktifkan")
		return
	}
	force := r.URL.Query().Get("refresh") == "1"
	status, err := svc.PackageStatus(r.Context(), force)
	if err != nil {
		s.writeOpsError(w, r, err, "package status")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// handleOpsPackagesRefresh updates the package metadata in the background.
func (s *Server) handleOpsPackagesRefresh(w http.ResponseWriter, r *http.Request) {
	svc := s.opsOrNil()
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUnavailable, "fitur operasi sistem dinonaktifkan")
		return
	}
	job, err := svc.RefreshPackages(r.Context())
	if err != nil {
		s.audit.Log(r.Context(), audit.Entry{
			Action: audit.ActionPackageRefresh, Detail: err.Error(), IP: s.clientIP(r), Success: false,
		})
		s.writeOpsError(w, r, err, "package refresh")
		return
	}
	s.audit.Log(r.Context(), audit.Entry{
		Action: audit.ActionPackageRefresh, Target: job.Command, Detail: job.ID,
		IP: s.clientIP(r), Success: true,
	})
	writeJSON(w, http.StatusAccepted, job)
}

type upgradeRequest struct {
	// Dist selects dist-upgrade, which may install and remove packages.
	Dist bool `json:"dist"`
}

// handleOpsPackagesUpgrade installs pending updates in the background.
func (s *Server) handleOpsPackagesUpgrade(w http.ResponseWriter, r *http.Request) {
	svc := s.opsOrNil()
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUnavailable, "fitur operasi sistem dinonaktifkan")
		return
	}
	var req upgradeRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(w, r, &req, 8<<10); err != nil {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
			return
		}
	}
	job, err := svc.UpgradePackages(r.Context(), req.Dist)
	if err != nil {
		s.audit.Log(r.Context(), audit.Entry{
			Action: audit.ActionPackageUpgrade, Detail: err.Error(), IP: s.clientIP(r), Success: false,
		})
		s.writeOpsError(w, r, err, "package upgrade")
		return
	}
	s.audit.Log(r.Context(), audit.Entry{
		Action: audit.ActionPackageUpgrade, Target: job.Command, Detail: job.ID,
		IP: s.clientIP(r), Success: true,
	})
	writeJSON(w, http.StatusAccepted, job)
}

// handleOpsJob returns the state and output of a background job.
func (s *Server) handleOpsJob(w http.ResponseWriter, r *http.Request) {
	svc := s.opsOrNil()
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUnavailable, "fitur operasi sistem dinonaktifkan")
		return
	}
	job, ok := svc.Job(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "pekerjaan tidak ditemukan")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// queryInt parses an optional integer query parameter within bounds.
func queryInt(raw string, min, max, fallback int) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < min || n > max {
		return 0, errors.New("out of range")
	}
	return n, nil
}
