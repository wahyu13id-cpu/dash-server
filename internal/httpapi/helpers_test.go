package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
)

func newCookieJar() (http.CookieJar, error) { return cookiejar.New(nil) }

func escape(s string) string { return url.QueryEscape(s) }

// login authenticates the test client and returns it together with the CSRF
// token that mutating requests must echo back.
func login(t *testing.T, env *testEnv) (*http.Client, string) {
	t.Helper()
	client := env.client()
	body := `{"username":"` + testUser + `","password":"` + testPass + `"}`
	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testOrig)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("login request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", resp.StatusCode)
	}
	var out struct {
		User struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	if out.User.Username != testUser {
		t.Fatalf("unexpected user %q", out.User.Username)
	}
	csrf := cookieValue(client, env.ts.URL, csrfCookieName)
	if csrf == "" {
		t.Fatal("csrf cookie not set")
	}
	return client, csrf
}

func cookieValue(client *http.Client, rawURL, name string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	for _, c := range client.Jar.Cookies(u) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

func cookieHeader(client *http.Client, rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	var parts []string
	for _, c := range client.Jar.Cookies(u) {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

// wsURL converts an httptest http URL into a websocket URL for a session.
func wsURL(base, id string) string {
	base = strings.Replace(base, "http://", "ws://", 1)
	base = strings.Replace(base, "https://", "wss://", 1)
	return base + "/api/terminal/sessions/" + id + "/stream"
}
