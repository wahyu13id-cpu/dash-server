package httpapi

import (
	"net/http"

	"dashboard/internal/sysinfo"
)

// systemResponse wraps the host snapshot with a readiness flag so the UI can
// distinguish "still warming up" from an empty host.
type systemResponse struct {
	Ready    bool             `json:"ready"`
	Snapshot *sysinfo.Snapshot `json:"snapshot,omitempty"`
}

// handleSystem returns the latest host resource snapshot (CPU, memory, disks,
// network and top processes). The heavy lifting happens in the background
// sampler, so this handler only reads a cached value.
func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	if s.monitor == nil {
		writeJSON(w, http.StatusOK, systemResponse{Ready: false})
		return
	}
	snap, ok := s.monitor.Latest()
	if !ok {
		writeJSON(w, http.StatusOK, systemResponse{Ready: false})
		return
	}
	writeJSON(w, http.StatusOK, systemResponse{Ready: true, Snapshot: &snap})
}
