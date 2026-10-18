package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"dashboard/internal/auth"
)

type profileBody struct {
	User              auth.User `json:"user"`
	ActiveSessions    int       `json:"activeSessions"`
	TOTPEnabled       bool      `json:"totpEnabled"`
	SessionExpiresAt  string    `json:"sessionExpiresAt"`
	SessionTTLSeconds int64     `json:"sessionTtlSeconds"`
	UsernameMinLength int       `json:"usernameMinLength"`
	UsernameMaxLength int       `json:"usernameMaxLength"`
	PasswordMinLength int       `json:"passwordMinLength"`
}

type updateProfileBody struct {
	OK              bool      `json:"ok"`
	User            auth.User `json:"user"`
	UsernameChanged bool      `json:"usernameChanged"`
	PasswordChanged bool      `json:"passwordChanged"`
}

func decodeJSONBody(t *testing.T, data []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("decode body %s: %v", data, err)
	}
}

func TestProfileRequiresAuth(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()

	client := env.client()
	status, _ := doJSON(t, env, client, http.MethodGet, "/api/auth/profile", "", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("GET /api/auth/profile without a session = %d, want 401", status)
	}
	status, _ = doJSON(t, env, client, http.MethodPatch, "/api/auth/profile", "", `{"currentPassword":"x"}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("PATCH /api/auth/profile without a session = %d, want 401", status)
	}
}

func TestProfileGetReportsAccountState(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)

	status, data := doJSON(t, env, client, http.MethodGet, "/api/auth/profile", csrf, "")
	mustStatus(t, status, http.StatusOK, data)

	var body profileBody
	decodeJSONBody(t, data, &body)
	if body.User.Username != testUser {
		t.Errorf("username = %q, want %q", body.User.Username, testUser)
	}
	if body.ActiveSessions < 1 {
		t.Errorf("activeSessions = %d, want >= 1", body.ActiveSessions)
	}
	if body.SessionExpiresAt == "" {
		t.Error("sessionExpiresAt is empty")
	}
	if body.UsernameMinLength != auth.UsernameMinLen || body.PasswordMinLength != auth.PasswordMinLen {
		t.Errorf("policy hints = %d/%d, want %d/%d",
			body.UsernameMinLength, body.PasswordMinLength, auth.UsernameMinLen, auth.PasswordMinLen)
	}
	if body.TOTPEnabled {
		t.Error("two-factor should be off in a fresh environment")
	}
}

func TestProfileUpdateRenamesUser(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)

	body := `{"currentPassword":"` + testPass + `","username":"  Wahyu.Hidayat  ","displayName":"Wahyu"}`
	status, data := doJSON(t, env, client, http.MethodPatch, "/api/auth/profile", csrf, body)
	mustStatus(t, status, http.StatusOK, data)

	var out updateProfileBody
	decodeJSONBody(t, data, &out)
	if !out.OK || !out.UsernameChanged {
		t.Fatalf("unexpected response: %s", data)
	}
	if out.User.Username != "wahyu.hidayat" || out.User.DisplayName != "Wahyu" {
		t.Fatalf("stored values = %+v", out.User)
	}
	if out.PasswordChanged {
		t.Error("passwordChanged = true, want false")
	}

	// The session still works and /me reports the new login name.
	status, data = doJSON(t, env, client, http.MethodGet, "/api/auth/me", csrf, "")
	mustStatus(t, status, http.StatusOK, data)
	if !strings.Contains(string(data), `"username":"wahyu.hidayat"`) {
		t.Errorf("me after rename = %s", data)
	}

	// And the old name no longer signs in.
	other := env.client()
	status, _ = doJSON(t, env, other, http.MethodPost, "/api/auth/login", "",
		`{"username":"admin","password":"`+testPass+`"}`)
	if status != http.StatusUnauthorized {
		t.Errorf("login with the old username = %d, want 401", status)
	}
	status, _ = doJSON(t, env, other, http.MethodPost, "/api/auth/login", "",
		`{"username":"wahyu.hidayat","password":"`+testPass+`"}`)
	if status != http.StatusOK {
		t.Errorf("login with the new username = %d, want 200", status)
	}
}

func TestProfileUpdateRejectsWrongPassword(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)

	status, data := doJSON(t, env, client, http.MethodPatch, "/api/auth/profile", csrf,
		`{"currentPassword":"wrongpassword1","username":"someone"}`)
	mustStatus(t, status, http.StatusUnauthorized, data)

	status, data = doJSON(t, env, client, http.MethodGet, "/api/auth/me", csrf, "")
	mustStatus(t, status, http.StatusOK, data)
	if !strings.Contains(string(data), `"username":"admin"`) {
		t.Errorf("the account changed despite a failed re-authentication: %s", data)
	}
}

func TestProfileUpdateConflictAndPolicyErrors(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)

	// A second account for the conflict case.
	hash, err := auth.HashPassword("operatorpass1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.CreateUser(context.Background(), "operator", "Operator", hash, "admin"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	cases := []struct {
		name string
		body string
		want int
		code string
	}{
		{"taken", `{"currentPassword":"` + testPass + `","username":"Operator"}`, http.StatusConflict, CodeConflict},
		{"invalid username", `{"currentPassword":"` + testPass + `","username":"no spaces!"}`, http.StatusBadRequest, CodeInvalidUsername},
		{"weak password", `{"currentPassword":"` + testPass + `","newPassword":"short"}`, http.StatusBadRequest, CodeWeakPassword},
		{"missing current password", `{"username":"whatever"}`, http.StatusBadRequest, CodeBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, data := doJSON(t, env, client, http.MethodPatch, "/api/auth/profile", csrf, tc.body)
			mustStatus(t, status, tc.want, data)
			var apiErr APIError
			decodeJSONBody(t, data, &apiErr)
			if apiErr.Error.Code != tc.code {
				t.Errorf("code = %q, want %q (%s)", apiErr.Error.Code, tc.code, data)
			}
		})
	}
}

// TestProfilePasswordChangeKeepsCurrentSessionAndEndsOthers is the end-to-end
// guarantee: after changing the password this browser stays signed in while
// every other session is revoked.
func TestProfilePasswordChangeKeepsCurrentSessionAndEndsOthers(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)

	second, _ := login(t, env)

	status, data := doJSON(t, env, client, http.MethodPatch, "/api/auth/profile", csrf,
		`{"currentPassword":"`+testPass+`","newPassword":"brandnewpass2"}`)
	mustStatus(t, status, http.StatusOK, data)

	var out updateProfileBody
	decodeJSONBody(t, data, &out)
	if !out.PasswordChanged {
		t.Fatalf("passwordChanged = false (%s)", data)
	}

	// The current browser survives, and its rotated CSRF cookie is usable.
	csrf = cookieValue(client, env.ts.URL, csrfCookieName)
	if csrf == "" {
		t.Fatal("no CSRF cookie after the password change")
	}
	status, data = doJSON(t, env, client, http.MethodGet, "/api/auth/profile", csrf, "")
	mustStatus(t, status, http.StatusOK, data)

	// The other browser must be signed out.
	status, _ = doJSON(t, env, second, http.MethodGet, "/api/auth/me", "", "")
	if status != http.StatusUnauthorized {
		t.Errorf("the other session = %d, want 401", status)
	}

	// The old password is gone, the new one works.
	fresh := env.client()
	status, _ = doJSON(t, env, fresh, http.MethodPost, "/api/auth/login", "",
		`{"username":"admin","password":"`+testPass+`"}`)
	if status != http.StatusUnauthorized {
		t.Errorf("login with the old password = %d, want 401", status)
	}
	status, _ = doJSON(t, env, fresh, http.MethodPost, "/api/auth/login", "",
		`{"username":"admin","password":"brandnewpass2"}`)
	if status != http.StatusOK {
		t.Errorf("login with the new password = %d, want 200", status)
	}
}

// TestProfileUpdateNoopIsHarmless pins that sending unchanged values reports
// no change and does not rotate the session.
func TestProfileUpdateNoopIsHarmless(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)

	session := cookieValue(client, env.ts.URL, sessionCookieName)
	status, data := doJSON(t, env, client, http.MethodPatch, "/api/auth/profile", csrf,
		`{"currentPassword":"`+testPass+`","username":"admin","displayName":"Admin"}`)
	mustStatus(t, status, http.StatusOK, data)

	var out updateProfileBody
	decodeJSONBody(t, data, &out)
	if out.UsernameChanged || out.PasswordChanged {
		t.Errorf("expected no changes, got %s", data)
	}
	if got := cookieValue(client, env.ts.URL, sessionCookieName); got != session {
		t.Error("a no-op rotated the session cookie")
	}
}
