package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"dashboard/internal/audit"
	"dashboard/internal/sshconfig"
	"dashboard/internal/sshsecret"
	"dashboard/internal/sshsvc"
)

// sshReady returns the SSH service, writing an error response when the feature
// is unavailable so every handler can bail out with one line.
func (s *Server) sshReady(w http.ResponseWriter) *sshsvc.Service {
	if s.ssh == nil || !s.ssh.Enabled() {
		writeError(w, http.StatusServiceUnavailable, CodeUnavailable, "fitur SSH tidak aktif")
		return nil
	}
	return s.ssh
}

// sshError maps domain errors onto HTTP statuses. Validation messages are
// surfaced verbatim because they tell the user exactly which field is wrong;
// anything unexpected is logged and replaced with a generic message.
func sshError(w http.ResponseWriter, err error, internal string) {
	var verr *sshconfig.ValidationError
	switch {
	case errors.Is(err, sshsvc.ErrDisabled):
		writeError(w, http.StatusServiceUnavailable, CodeUnavailable, err.Error())
	case errors.Is(err, sshsvc.ErrUnknownHost):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
	case errors.Is(err, sshsvc.ErrReadOnly), errors.Is(err, sshsvc.ErrNoAlias),
		errors.Is(err, sshsvc.ErrInvalidAlias):
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
	case errors.Is(err, sshsvc.ErrUpstream):
		// The remote host or an ssh tool failed; the detail is the whole point
		// of the message, so it is surfaced instead of a generic 500.
		writeError(w, http.StatusBadGateway, CodeUnavailable, err.Error())
	case errors.As(err, &verr):
		writeError(w, http.StatusUnprocessableEntity, CodeBadRequest, verr.Error())
	case errors.Is(err, sshconfig.ErrDuplicate), errors.Is(err, sshconfig.ErrConflict):
		writeError(w, http.StatusConflict, CodeConflict, err.Error())
	case errors.Is(err, sshsecret.ErrUnknownKind):
		writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error())
	default:
		slog.Error(internal, "error", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, internal)
	}
}

// --- state ----------------------------------------------------------------

// handleSSHState returns everything the SSH page needs in one round trip.
func (s *Server) handleSSHState(w http.ResponseWriter, r *http.Request) {
	svc := s.sshReady(w)
	if svc == nil {
		return
	}
	view, err := svc.View(r.Context())
	if err != nil {
		sshError(w, err, "could not read ssh configuration")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleSSHKeys lists the private keys in ~/.ssh.
func (s *Server) handleSSHKeys(w http.ResponseWriter, r *http.Request) {
	svc := s.sshReady(w)
	if svc == nil {
		return
	}
	keys, err := svc.Keys(r.Context())
	if err != nil {
		sshError(w, err, "could not list ssh keys")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

type sshParseRequest struct {
	Command string `json:"command"`
}

// handleSSHParse turns "ssh user@host -p 2222" into host fields.
func (s *Server) handleSSHParse(w http.ResponseWriter, r *http.Request) {
	svc := s.sshReady(w)
	if svc == nil {
		return
	}
	var req sshParseRequest
	if err := decodeJSON(w, r, &req, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	writeJSON(w, http.StatusOK, svc.QuickAdd(req.Command))
}

// --- host CRUD ------------------------------------------------------------

func (s *Server) handleSSHHostCreate(w http.ResponseWriter, r *http.Request) {
	svc := s.sshReady(w)
	if svc == nil {
		return
	}
	var in sshsvc.HostInput
	if err := decodeJSON(w, r, &in, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	host, err := svc.Add(r.Context(), in)
	if err != nil {
		sshError(w, err, "could not add ssh host")
		return
	}
	s.audit.Log(r.Context(), audit.Entry{
		Action: audit.ActionSSHHostCreate, Target: host.Alias,
		Detail: svc.Config().ConfigPath, Success: true,
	})
	writeJSON(w, http.StatusCreated, host)
}

func (s *Server) handleSSHHostGet(w http.ResponseWriter, r *http.Request) {
	svc := s.sshReady(w)
	if svc == nil {
		return
	}
	alias := r.PathValue("alias")
	host, err := svc.Lookup(r.Context(), alias)
	if err != nil {
		sshError(w, err, "could not read ssh host")
		return
	}
	secrets, err := svc.SecretStatus(r.Context(), alias)
	if err != nil {
		secrets = map[sshsecret.Kind]bool{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"host":          host,
		"hasPassword":   secrets[sshsecret.KindPassword],
		"hasPassphrase": secrets[sshsecret.KindPassphrase],
	})
}

func (s *Server) handleSSHHostUpdate(w http.ResponseWriter, r *http.Request) {
	svc := s.sshReady(w)
	if svc == nil {
		return
	}
	alias := r.PathValue("alias")
	var in sshsvc.HostInput
	if err := decodeJSON(w, r, &in, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	host, err := svc.Update(r.Context(), alias, in)
	if err != nil {
		sshError(w, err, "could not update ssh host")
		return
	}
	detail := svc.Config().ConfigPath
	if host.Alias != alias {
		detail += " (rename dari " + alias + ")"
	}
	s.audit.Log(r.Context(), audit.Entry{
		Action: audit.ActionSSHHostUpdate, Target: host.Alias, Detail: detail, Success: true,
	})
	writeJSON(w, http.StatusOK, host)
}

func (s *Server) handleSSHHostDelete(w http.ResponseWriter, r *http.Request) {
	svc := s.sshReady(w)
	if svc == nil {
		return
	}
	alias := r.PathValue("alias")
	if err := svc.Delete(r.Context(), alias); err != nil {
		sshError(w, err, "could not delete ssh host")
		return
	}
	s.audit.Log(r.Context(), audit.Entry{
		Action: audit.ActionSSHHostDelete, Target: alias, Success: true,
	})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": alias})
}

type sshFavoriteRequest struct {
	Favorite bool `json:"favorite"`
}

func (s *Server) handleSSHFavorite(w http.ResponseWriter, r *http.Request) {
	svc := s.sshReady(w)
	if svc == nil {
		return
	}
	alias := r.PathValue("alias")
	var req sshFavoriteRequest
	if err := decodeJSON(w, r, &req, 4<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	if err := svc.SetFavorite(r.Context(), alias, req.Favorite); err != nil {
		sshError(w, err, "could not update favorite")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alias": alias, "favorite": req.Favorite})
}

type sshNoteRequest struct {
	Note string `json:"note"`
}

func (s *Server) handleSSHNote(w http.ResponseWriter, r *http.Request) {
	svc := s.sshReady(w)
	if svc == nil {
		return
	}
	alias := r.PathValue("alias")
	var req sshNoteRequest
	if err := decodeJSON(w, r, &req, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	if err := svc.SetNote(r.Context(), alias, req.Note); err != nil {
		sshError(w, err, "could not update note")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alias": alias})
}

// --- diagnostics ----------------------------------------------------------

// handleSSHEffective returns the resolved configuration for a host, which is
// what ssh -G computes and therefore authoritative.
func (s *Server) handleSSHEffective(w http.ResponseWriter, r *http.Request) {
	svc := s.sshReady(w)
	if svc == nil {
		return
	}
	alias := r.PathValue("alias")
	if _, err := svc.Lookup(r.Context(), alias); err != nil {
		sshError(w, err, "could not read ssh host")
		return
	}
	res, err := svc.Effective(r.Context(), alias)
	if err != nil {
		sshError(w, err, "could not resolve ssh configuration")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleSSHTest probes reachability without ever prompting.
func (s *Server) handleSSHTest(w http.ResponseWriter, r *http.Request) {
	svc := s.sshReady(w)
	if svc == nil {
		return
	}
	alias := r.PathValue("alias")
	res, err := svc.Test(r.Context(), alias)
	if err != nil {
		sshError(w, err, "could not test ssh host")
		return
	}
	s.audit.Log(r.Context(), audit.Entry{
		Action: audit.ActionSSHConnect, Target: alias,
		Detail: "uji koneksi: " + res.Status, Success: res.Status == "ok",
	})
	writeJSON(w, http.StatusOK, res)
}

// --- stored credentials ---------------------------------------------------

type sshSecretRequest struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// handleSSHSecretSet stores a password or key passphrase. The plaintext is
// write-only: no endpoint ever returns it.
func (s *Server) handleSSHSecretSet(w http.ResponseWriter, r *http.Request) {
	svc := s.sshReady(w)
	if svc == nil {
		return
	}
	alias := r.PathValue("alias")
	var req sshSecretRequest
	if err := decodeJSON(w, r, &req, 16<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	kind := sshsecret.Kind(strings.ToLower(strings.TrimSpace(req.Kind)))
	if !sshsecret.ValidKind(kind) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "kind harus password atau passphrase")
		return
	}
	if req.Value == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "nilai kredensial kosong")
		return
	}
	// ssh reads the answer from the helper's standard output one line at a
	// time, so a newline inside the secret would silently truncate it. Refuse
	// instead of storing something that can never work.
	if strings.ContainsAny(req.Value, "\r\n") {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "nilai kredensial tidak boleh berisi baris baru")
		return
	}
	if err := svc.SetSecret(r.Context(), alias, kind, req.Value); err != nil {
		sshError(w, err, "could not store credential")
		return
	}
	// The value itself is never logged or echoed.
	s.audit.Log(r.Context(), audit.Entry{
		Action: audit.ActionSSHSecretSet, Target: alias, Detail: string(kind), Success: true,
	})
	writeJSON(w, http.StatusOK, map[string]any{"alias": alias, "kind": kind, "stored": true})
}

// handleSSHSecretClear removes stored credentials. Omitting kind clears both.
func (s *Server) handleSSHSecretClear(w http.ResponseWriter, r *http.Request) {
	svc := s.sshReady(w)
	if svc == nil {
		return
	}
	alias := r.PathValue("alias")
	param := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))

	kinds := sshsecret.Kinds()
	if param != "" {
		kind := sshsecret.Kind(param)
		if !sshsecret.ValidKind(kind) {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "kind harus password atau passphrase")
			return
		}
		kinds = []sshsecret.Kind{kind}
	}
	for _, kind := range kinds {
		if err := svc.ClearSecret(r.Context(), alias, kind); err != nil {
			sshError(w, err, "could not clear credential")
			return
		}
	}
	s.audit.Log(r.Context(), audit.Entry{
		Action: audit.ActionSSHSecretClear, Target: alias, Detail: param, Success: true,
	})
	writeJSON(w, http.StatusOK, map[string]any{"alias": alias, "cleared": true})
}

// --- host keys ------------------------------------------------------------

// handleSSHHostKeyStatus reports whether the host key is already trusted.
func (s *Server) handleSSHHostKeyStatus(w http.ResponseWriter, r *http.Request) {
	svc := s.sshReady(w)
	if svc == nil {
		return
	}
	info, err := svc.HostKeyStatus(r.Context(), r.PathValue("alias"))
	if err != nil {
		sshError(w, err, "could not read host key status")
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleSSHHostKeyScan asks the host for its keys so the user can compare
// fingerprints before trusting them.
func (s *Server) handleSSHHostKeyScan(w http.ResponseWriter, r *http.Request) {
	svc := s.sshReady(w)
	if svc == nil {
		return
	}
	alias := r.PathValue("alias")
	keys, err := svc.ScanHostKey(r.Context(), alias)
	if err != nil {
		sshError(w, err, "could not scan host key")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alias": alias, "fingerprints": keys})
}

// handleSSHHostKeyTrust writes the scanned keys into known_hosts. This is the
// explicit trust-on-first-use step; nothing is ever trusted implicitly.
func (s *Server) handleSSHHostKeyTrust(w http.ResponseWriter, r *http.Request) {
	svc := s.sshReady(w)
	if svc == nil {
		return
	}
	alias := r.PathValue("alias")
	n, err := svc.TrustHostKey(r.Context(), alias)
	if err != nil {
		sshError(w, err, "could not trust host key")
		return
	}
	s.audit.Log(r.Context(), audit.Entry{
		Action: audit.ActionSSHHostKeyTrust, Target: alias,
		Detail: "kunci host dipercaya", Success: true,
	})
	writeJSON(w, http.StatusOK, map[string]any{"alias": alias, "trusted": n})
}

// handleSSHHostKeyForget removes the host from known_hosts.
func (s *Server) handleSSHHostKeyForget(w http.ResponseWriter, r *http.Request) {
	svc := s.sshReady(w)
	if svc == nil {
		return
	}
	alias := r.PathValue("alias")
	removed, err := svc.ForgotHostKey(r.Context(), alias)
	if err != nil {
		sshError(w, err, "could not forget host key")
		return
	}
	s.audit.Log(r.Context(), audit.Entry{
		Action: audit.ActionSSHHostKeyTrust, Target: alias,
		Detail: "kunci host dilupakan", Success: true,
	})
	writeJSON(w, http.StatusOK, map[string]any{"alias": alias, "removed": removed})
}
