package controlapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testAdminPassword = "correct horse battery staple"

func TestAdminStorePersistsArgon2idRecordWithRestrictedPermissions(t *testing.T) {
	store := NewFileAdminStore(t.TempDir())
	if initialized, err := store.Initialized(); err != nil || initialized {
		t.Fatalf("initial state = %t, err=%v", initialized, err)
	}
	if err := store.Set("admin", testAdminPassword); err != nil {
		t.Fatal(err)
	}
	initialized, err := store.Initialized()
	if err != nil || !initialized {
		t.Fatalf("initialized = %t, err=%v", initialized, err)
	}
	if err := store.Authenticate("admin", testAdminPassword); err != nil {
		t.Fatalf("Authenticate() = %v", err)
	}
	if err := store.Authenticate("admin", "wrong password value"); err == nil {
		t.Fatal("wrong password authenticated")
	}
	if err := store.Set("second", testAdminPassword); err == nil {
		t.Fatal("second administrator was accepted")
	}
	info, err := os.Stat(filepath.Join(store.Dir(), adminRecordFilename))
	if err != nil {
		t.Fatal(err)
	}
	// 0640, not 0600: root writes this record (installer and opensurge-setup) but
	// the unprivileged control service has to read it back on every login.
	if info.Mode().Perm() != adminRecordMode {
		t.Fatalf("admin record mode=%o, want %o", info.Mode().Perm(), adminRecordMode)
	}
}

func TestResetPasswordKeepsTheRecordReadableForTheControlService(t *testing.T) {
	store := NewFileAdminStore(t.TempDir())
	if err := store.Set("admin", "initial-password"); err != nil {
		t.Fatal(err)
	}
	username, err := store.ResetPassword("short")
	if err != nil {
		t.Fatal(err)
	}
	if username != "admin" {
		t.Fatalf("reset username = %q, want admin", username)
	}
	if err := store.Authenticate("admin", "short"); err != nil {
		t.Fatalf("reset password does not authenticate: %v", err)
	}
	info, err := os.Stat(filepath.Join(store.Dir(), adminRecordFilename))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != adminRecordMode {
		t.Fatalf("record mode after reset = %o, want %o", info.Mode().Perm(), adminRecordMode)
	}
}

func TestAuthenticateReportsAnUnreadableRecordDistinctly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permission checks")
	}
	store := NewFileAdminStore(t.TempDir())
	if err := store.Set("admin", "initial-password"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Dir(), adminRecordFilename)
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, adminRecordMode) })
	err := store.Authenticate("admin", "initial-password")
	if !errors.Is(err, ErrAdminRecordUnreadable) {
		t.Fatalf("error = %v, want ErrAdminRecordUnreadable", err)
	}
}

func TestSetupAndAuthStatusAreSingleAdministratorFlow(t *testing.T) {
	server := newTestServer(t)

	status := getAuthStatus(t, server, nil)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"initialized":false`) || !strings.Contains(status.Body.String(), `"authenticated":false`) {
		t.Fatalf("initial status=%d body=%s", status.Code, status.Body.String())
	}
	setup := postAuthJSON(t, server, "/api/v1/auth/setup", `{"username":"admin","password":"`+testAdminPassword+`"}`, "192.168.50.9", nil)
	if setup.Code != http.StatusNoContent {
		t.Fatalf("setup status=%d body=%s", setup.Code, setup.Body.String())
	}
	status = getAuthStatus(t, server, nil)
	if !strings.Contains(status.Body.String(), `"initialized":true`) || !strings.Contains(status.Body.String(), `"authenticated":false`) {
		t.Fatalf("post-setup status=%d body=%s", status.Code, status.Body.String())
	}
	second := postAuthJSON(t, server, "/api/v1/auth/setup", `{"username":"other","password":"`+testAdminPassword+`"}`, "192.168.50.9", nil)
	if second.Code != http.StatusConflict {
		t.Fatalf("second setup status=%d body=%s", second.Code, second.Body.String())
	}
}

func TestLoginSetsSecureSessionCookie(t *testing.T) {
	server := newAuthTestServer(t)
	response := postAuthJSON(t, server, "/api/v1/auth/login", `{"username":"admin","password":"`+testAdminPassword+`"}`, "192.168.50.9", nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies=%v", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != "opensurge_session" || cookie.Value == "" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie=%#v", cookie)
	}
	status := getAuthStatus(t, server, cookie)
	if !strings.Contains(status.Body.String(), `"authenticated":true`) {
		t.Fatalf("authenticated status=%d body=%s", status.Code, status.Body.String())
	}
}

func TestFailedLoginLocksSourceIPAfterFiveAttempts(t *testing.T) {
	server := newAuthTestServer(t)
	for attempt := 0; attempt < 5; attempt++ {
		response := postAuthJSON(t, server, "/api/v1/auth/login", `{"username":"admin","password":"wrong password value"}`, "192.168.50.9", nil)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d body=%s", attempt, response.Code, response.Body.String())
		}
	}
	response := postAuthJSON(t, server, "/api/v1/auth/login", `{"username":"admin","password":"wrong password value"}`, "192.168.50.9", nil)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("locked login status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAuthenticatedMutationsRequireSameOrigin(t *testing.T) {
	server := newAuthTestServer(t)
	login := postAuthJSON(t, server, "/api/v1/auth/login", `{"username":"admin","password":"`+testAdminPassword+`"}`, "192.168.50.9", nil)
	cookie := login.Result().Cookies()[0]
	request := httptest.NewRequest(http.MethodPost, testManagementURL+"/api/v1/recovery", strings.NewReader(`{"stage":"idle"}`))
	request.Host = testManagementAddr
	request.RemoteAddr = "192.168.50.9:1234"
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "origin_rejected") {
		t.Fatalf("mutation without same origin status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAuthRoutesReplaceBootstrapAndProtectAPI(t *testing.T) {
	server := newAuthTestServer(t)
	unauthorized := getAuthStatus(t, server, nil)
	if unauthorized.Code != http.StatusOK {
		t.Fatalf("auth status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, testManagementURL+"/api/v1/overview", nil)
	request.Host = testManagementAddr
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized API status=%d body=%s", response.Code, response.Body.String())
	}
	for _, path := range []string{"/bootstrap", "/api/v1/session/bootstrap"} {
		request := httptest.NewRequest(http.MethodGet, testManagementURL+path, nil)
		request.Host = testManagementAddr
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("retired endpoint %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestBearerAuthenticationIsRetired(t *testing.T) {
	server := newAuthTestServer(t)
	request := httptest.NewRequest(http.MethodGet, testManagementURL+"/api/v1/overview", nil)
	request.Host = testManagementAddr
	request.Header.Set("Authorization", "Bearer legacy-token")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "authentication_required") {
		t.Fatalf("legacy bearer status=%d body=%s", response.Code, response.Body.String())
	}
}

func newAuthTestServer(t *testing.T) *Server {
	t.Helper()
	server := newTestServer(t)
	if err := server.adminStore.Set("admin", testAdminPassword); err != nil {
		t.Fatal(err)
	}
	return server
}

func postAuthJSON(t *testing.T, server *Server, path, body, remoteIP string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, testManagementURL+path, strings.NewReader(body))
	request.Host = testManagementAddr
	request.RemoteAddr = remoteIP + ":1234"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", testManagementURL)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func getAuthStatus(t *testing.T, server *Server, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, testManagementURL+"/api/v1/auth/status", nil)
	request.Host = testManagementAddr
	request.RemoteAddr = "192.168.50.9:1234"
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func TestAuthStatusJSONHasNoCredentialMaterial(t *testing.T) {
	server := newAuthTestServer(t)
	response := getAuthStatus(t, server, nil)
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["password"]; ok {
		t.Fatal("auth status exposed password")
	}
}
