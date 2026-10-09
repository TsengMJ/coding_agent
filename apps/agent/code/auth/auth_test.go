package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tsengmingchun/quill/apps/agent/migrate"
)

const testKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // 32 bytes

// fakeGitHub answers the device flow: the first token poll is pending,
// later ones grant "gh-token" for login.
func fakeGitHub(t *testing.T, login string) *httptest.Server {
	t.Helper()
	var polls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/device/code", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("client_id") != "cid" || r.Form.Get("scope") != Scopes {
			http.Error(w, "bad form", 400)
			return
		}
		json.NewEncoder(w).Encode(DeviceCode{DeviceCode: "dev", UserCode: "ABCD-1234", VerificationURI: "https://github.com/login/device", ExpiresIn: 900, Interval: 5})
	})
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch {
		case r.Form.Get("device_code") == "expired":
			json.NewEncoder(w).Encode(map[string]string{"error": "expired_token"})
		case polls.Add(1) == 1:
			json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
		default:
			json.NewEncoder(w).Encode(map[string]string{"access_token": "gh-token"})
		}
	})
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gh-token" {
			http.Error(w, "bad token", 401)
			return
		}
		json.NewEncoder(w).Encode(GitHubUser{Login: login, Name: "Test User"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newService(t *testing.T, login string, allowed ...string) (*Service, *[]string) {
	t.Helper()
	gh := fakeGitHub(t, login)
	c, err := NewCipher(testKey)
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	s := &Service{
		GitHub: &GitHub{ClientID: "cid", BaseURL: gh.URL, APIURL: gh.URL},
		Store:  NewMemoryStore(), Cipher: c, Allowed: allowed,
		OnEvent: func(_ context.Context, user, action string) { events = append(events, user+":"+action) },
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	return s, &events
}

func call(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDeviceFlowLogin(t *testing.T) {
	s, events := newService(t, "Alice", "alice")
	mux := http.NewServeMux()
	s.Routes(mux)
	mux.Handle("GET /private", s.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := UserFrom(r.Context())
		w.Write([]byte(u.ID + " " + u.GitHubToken))
	})))

	rec := call(mux, "POST", "/auth/device", "", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ABCD-1234") {
		t.Fatalf("device = %d %s", rec.Code, rec.Body)
	}
	if rec := call(mux, "POST", "/auth/token", "", `{"device_code":"dev"}`); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("first poll = %d %s", rec.Code, rec.Body)
	}
	rec = call(mux, "POST", "/auth/token", "", `{"device_code":"dev"}`)
	if rec.Code != 200 {
		t.Fatalf("second poll = %d %s", rec.Code, rec.Body)
	}
	var sess Session
	json.Unmarshal(rec.Body.Bytes(), &sess)
	if !strings.HasPrefix(sess.Token, "qs_") || sess.User.ID != "Alice" || strings.Contains(rec.Body.String(), "gh-token") {
		t.Fatalf("session = %s", rec.Body)
	}

	if rec := call(mux, "GET", "/private", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token = %d", rec.Code)
	}
	if rec := call(mux, "GET", "/private", "qs_wrong", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d", rec.Code)
	}
	if rec := call(mux, "GET", "/private", sess.Token, ""); rec.Code != 200 || rec.Body.String() != "Alice gh-token" {
		t.Fatalf("private = %d %q", rec.Code, rec.Body)
	}
	if rec := call(mux, "GET", "/auth/me", sess.Token, ""); rec.Code != 200 || strings.Contains(rec.Body.String(), "gh-token") {
		t.Fatalf("me = %d %s", rec.Code, rec.Body)
	}
	if rec := call(mux, "POST", "/auth/logout", sess.Token, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", rec.Code)
	}
	if rec := call(mux, "GET", "/private", sess.Token, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("after logout = %d", rec.Code)
	}
	if got := strings.Join(*events, ","); got != "Alice:login,Alice:logout" {
		t.Fatalf("events = %s", got)
	}
}

func TestLoginRefusesUsersOffTheAllowList(t *testing.T) {
	s, events := newService(t, "mallory", "alice")
	s.GitHub.HTTP = nil
	if _, err := s.FinishLogin(context.Background(), "dev"); !errors.Is(err, ErrAuthorizationPending) {
		t.Fatalf("first poll err = %v", err)
	}
	if _, err := s.FinishLogin(context.Background(), "dev"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("err = %v, want ErrNotAllowed", err)
	}
	if got := strings.Join(*events, ","); got != "mallory:login_refused" {
		t.Fatalf("events = %s", got)
	}
}

func TestLoginReportsExpiredDeviceCode(t *testing.T) {
	s, _ := newService(t, "alice", "alice")
	mux := http.NewServeMux()
	s.Routes(mux)
	if rec := call(mux, "POST", "/auth/token", "", `{"device_code":"expired"}`); rec.Code != http.StatusGone {
		t.Fatalf("expired = %d %s", rec.Code, rec.Body)
	}
}

func TestSessionsExpire(t *testing.T) {
	s, _ := newService(t, "alice", "alice")
	now := time.Now()
	s.Now = func() time.Time { return now }
	s.TTL = time.Hour
	s.FinishLogin(context.Background(), "dev")
	sess, err := s.FinishLogin(context.Background(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(context.Background(), sess.Token); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := s.Authenticate(context.Background(), sess.Token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expired session err = %v", err)
	}
}

func TestValidateRequiresAnAllowList(t *testing.T) {
	c, _ := NewCipher(testKey)
	s := &Service{GitHub: &GitHub{ClientID: "cid"}, Store: NewMemoryStore(), Cipher: c}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "QUILL_ALLOWED_USERS") {
		t.Fatalf("err = %v", err)
	}
}

func TestCipherRoundTripAndWrongKey(t *testing.T) {
	c, err := NewCipher(testKey)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := c.Seal("gh-token")
	if err != nil || strings.Contains(string(sealed), "gh-token") {
		t.Fatalf("sealed = %q, %v", sealed, err)
	}
	if got, err := c.Open(sealed); err != nil || got != "gh-token" {
		t.Fatalf("open = %q, %v", got, err)
	}
	other, _ := NewCipher(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("opened with the wrong key")
	}
	if _, err := NewCipher("short"); err == nil {
		t.Fatal("accepted a short key")
	}
}

func TestPostgresStore(t *testing.T) {
	conn := os.Getenv("POSTGRES_CONN_STRING")
	if conn == "" {
		t.Skip("POSTGRES_CONN_STRING is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := migrate.Up(context.Background(), pool, migrate.Quill()); err != nil {
		t.Fatal(err)
	}
	s, err := NewPostgresStore(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := NewSessionToken()
	user := "it-" + token[3:15]
	if err := s.PutUser(ctx, Record{ID: user, Name: "IT", EncryptedToken: []byte{1, 2, 3}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM quill_users WHERE id = $1`, user) })
	now := time.Now()
	if err := s.CreateSession(ctx, HashToken(token), user, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	r, err := s.LookupSession(ctx, HashToken(token), now)
	if err != nil || r.ID != user || len(r.EncryptedToken) != 3 {
		t.Fatalf("lookup = %+v, %v", r, err)
	}
	if _, err := s.LookupSession(ctx, HashToken(token), now.Add(2*time.Hour)); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expired lookup err = %v", err)
	}
	if err := s.DeleteSession(ctx, HashToken(token)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupSession(ctx, HashToken(token), now); !errors.Is(err, ErrNoSession) {
		t.Fatalf("deleted lookup err = %v", err)
	}
}
