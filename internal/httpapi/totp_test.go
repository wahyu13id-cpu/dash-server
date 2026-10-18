package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"dashboard/internal/totp"
)

// enableTOTPFor activates two-factor authentication for the shared test user
// directly through the service and returns its secret and recovery codes.
func enableTOTPFor(t *testing.T, env *testEnv) (string, []string) {
	t.Helper()
	ctx := context.Background()

	user, _, err := env.store.GetUserByUsername(ctx, testUser)
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	setup, err := env.auth.BeginTOTPSetup(ctx, user)
	if err != nil {
		t.Fatalf("BeginTOTPSetup: %v", err)
	}
	code, err := totp.Code(setup.Secret, time.Now())
	if err != nil {
		t.Fatalf("totp.Code: %v", err)
	}
	codes, err := env.auth.ConfirmTOTPSetup(ctx, user, code)
	if err != nil {
		t.Fatalf("ConfirmTOTPSetup: %v", err)
	}
	return setup.Secret, codes
}

// postJSON issues an authenticated JSON POST with the CSRF header and origin.
func postJSON(t *testing.T, client *http.Client, url, csrf, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testOrig)
	if csrf != "" {
		req.Header.Set(csrfHeaderName, csrf)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decodeLogin(t *testing.T, resp *http.Response) loginResponse {
	t.Helper()
	defer resp.Body.Close()
	var out loginResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	return out
}

// TestLoginWithoutTOTPReturnsOKStatus pins the single-step response shape.
func TestLoginWithoutTOTPReturnsOKStatus(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()

	client := env.client()
	resp := postJSON(t, client, env.ts.URL+"/api/auth/login", "",
		`{"username":"`+testUser+`","password":"`+testPass+`"}`)
	out := decodeLogin(t, resp)
	if resp.StatusCode != http.StatusOK || out.Status != "ok" {
		t.Fatalf("status=%d body=%+v", resp.StatusCode, out)
	}
	if out.User == nil || out.User.Username != testUser || out.Challenge != "" {
		t.Fatalf("unexpected body: %+v", out)
	}
}

// TestTOTPLoginHTTPFlow covers the two-step login: password, then code.
func TestTOTPLoginHTTPFlow(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	secret, _ := enableTOTPFor(t, env)

	client := env.client()
	first := decodeLogin(t, postJSON(t, client, env.ts.URL+"/api/auth/login", "",
		`{"username":"`+testUser+`","password":"`+testPass+`"}`))
	if first.Status != "totp_required" || first.Challenge == "" || first.User != nil {
		t.Fatalf("unexpected first step: %+v", first)
	}
	// Crucially, no session cookie may be issued before the second factor.
	if c := cookieValue(client, env.ts.URL, sessionCookieName); c != "" {
		t.Fatal("a session cookie was issued before the second factor")
	}

	// A wrong code is rejected and still leaves the user unauthenticated.
	bad := postJSON(t, client, env.ts.URL+"/api/auth/login/totp", "",
		`{"challenge":"`+first.Challenge+`","code":"000000"}`)
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong code status = %d, want 401", bad.StatusCode)
	}
	me, err := client.Get(env.ts.URL + "/api/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	me.Body.Close()
	if me.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me before second factor = %d, want 401", me.StatusCode)
	}

	code, err := totp.Code(secret, time.Now().Add(totp.Period))
	if err != nil {
		t.Fatal(err)
	}
	second := decodeLogin(t, postJSON(t, client, env.ts.URL+"/api/auth/login/totp", "",
		`{"challenge":"`+first.Challenge+`","code":"`+code+`"}`))
	if second.Status != "ok" || second.User == nil || second.User.Username != testUser {
		t.Fatalf("unexpected second step: %+v", second)
	}
	if cookieValue(client, env.ts.URL, csrfCookieName) == "" {
		t.Fatal("csrf cookie missing after second factor")
	}

	ok, err := client.Get(env.ts.URL + "/api/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("me after second factor = %d, want 200", ok.StatusCode)
	}
}

// TestTOTPManageEndpointsHTTPFlow covers enroll, activate, status and disable.
func TestTOTPManageEndpointsHTTPFlow(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)

	statusResp, err := client.Get(env.ts.URL + "/api/auth/totp")
	if err != nil {
		t.Fatal(err)
	}
	var status totpStatusResponse
	if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	statusResp.Body.Close()
	if status.Enabled {
		t.Fatal("two-factor reported as enabled for a fresh account")
	}
	if status.Issuer != "Dashboard Test" || status.Account != testUser {
		t.Fatalf("unexpected status payload: %+v", status)
	}

	setupResp := postJSON(t, client, env.ts.URL+"/api/auth/totp/setup", csrf, `{}`)
	var setup totpSetupResponse
	if err := json.NewDecoder(setupResp.Body).Decode(&setup); err != nil {
		t.Fatal(err)
	}
	setupResp.Body.Close()
	if setupResp.StatusCode != http.StatusOK {
		t.Fatalf("setup status = %d", setupResp.StatusCode)
	}
	if setup.Secret == "" || !strings.HasPrefix(setup.URI, "otpauth://totp/") {
		t.Fatalf("unexpected setup payload: %+v", setup)
	}
	if !strings.HasPrefix(setup.QRPNG, "data:image/png;base64,") {
		t.Fatalf("qr is not an inline PNG: %.40q", setup.QRPNG)
	}

	// Confirming with a wrong code must not activate the secret.
	badActivate := postJSON(t, client, env.ts.URL+"/api/auth/totp/activate", csrf, `{"code":"000000"}`)
	badActivate.Body.Close()
	if badActivate.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad activate status = %d, want 401", badActivate.StatusCode)
	}

	code, err := totp.Code(setup.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	activateResp := postJSON(t, client, env.ts.URL+"/api/auth/totp/activate", csrf, `{"code":"`+code+`"}`)
	var activated recoveryCodesResponse
	if err := json.NewDecoder(activateResp.Body).Decode(&activated); err != nil {
		t.Fatal(err)
	}
	activateResp.Body.Close()
	if activateResp.StatusCode != http.StatusOK || len(activated.RecoveryCodes) != totp.RecoveryCodeCount {
		t.Fatalf("activate status=%d codes=%d", activateResp.StatusCode, len(activated.RecoveryCodes))
	}

	statusResp2, err := client.Get(env.ts.URL + "/api/auth/totp")
	if err != nil {
		t.Fatal(err)
	}
	var status2 totpStatusResponse
	if err := json.NewDecoder(statusResp2.Body).Decode(&status2); err != nil {
		t.Fatal(err)
	}
	statusResp2.Body.Close()
	if !status2.Enabled || status2.RecoveryRemaining != totp.RecoveryCodeCount {
		t.Fatalf("unexpected status after activation: %+v", status2)
	}

	// Disabling needs both the password and a current code.
	wrongPw := postJSON(t, client, env.ts.URL+"/api/auth/totp/disable", csrf,
		`{"password":"nope","code":"`+code+`"}`)
	wrongPw.Body.Close()
	if wrongPw.StatusCode != http.StatusUnauthorized {
		t.Fatalf("disable with wrong password = %d, want 401", wrongPw.StatusCode)
	}

	next, err := totp.Code(setup.Secret, time.Now().Add(totp.Period))
	if err != nil {
		t.Fatal(err)
	}
	disableResp := postJSON(t, client, env.ts.URL+"/api/auth/totp/disable", csrf,
		`{"password":"`+testPass+`","code":"`+next+`"}`)
	disableResp.Body.Close()
	if disableResp.StatusCode != http.StatusOK {
		t.Fatalf("disable status = %d", disableResp.StatusCode)
	}

	statusResp3, err := client.Get(env.ts.URL + "/api/auth/totp")
	if err != nil {
		t.Fatal(err)
	}
	var status3 totpStatusResponse
	if err := json.NewDecoder(statusResp3.Body).Decode(&status3); err != nil {
		t.Fatal(err)
	}
	statusResp3.Body.Close()
	if status3.Enabled {
		t.Fatal("two-factor still enabled after disable")
	}
}

// TestTOTPRecoveryCodeHTTP verifies a recovery code completes a login once.
func TestTOTPRecoveryCodeHTTP(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	_, codes := enableTOTPFor(t, env)

	signIn := func(code string) int {
		client := env.client()
		first := decodeLogin(t, postJSON(t, client, env.ts.URL+"/api/auth/login", "",
			`{"username":"`+testUser+`","password":"`+testPass+`"}`))
		resp := postJSON(t, client, env.ts.URL+"/api/auth/login/totp", "",
			`{"challenge":"`+first.Challenge+`","code":"`+code+`"}`)
		resp.Body.Close()
		return resp.StatusCode
	}

	if got := signIn(codes[0]); got != http.StatusOK {
		t.Fatalf("recovery login status = %d, want 200", got)
	}
	if got := signIn(codes[0]); got != http.StatusUnauthorized {
		t.Fatalf("reused recovery login status = %d, want 401", got)
	}
}
