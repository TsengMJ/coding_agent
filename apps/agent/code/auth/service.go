package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// DefaultSessionTTL is how long a login lasts.
const DefaultSessionTTL = 30 * 24 * time.Hour

// ErrNotAllowed is returned when a GitHub user is not on the allow list.
var ErrNotAllowed = errors.New("this GitHub account is not allowed on this server")

// Service signs users in with the GitHub device flow and checks sessions.
type Service struct {
	GitHub *GitHub
	Store  Store
	Cipher *Cipher
	// Allowed lists the GitHub logins that may sign in; required, since
	// anyone with a GitHub account can complete the device flow.
	Allowed []string
	// TTL defaults to DefaultSessionTTL.
	TTL time.Duration
	// OnEvent, when set, is told about logins and logouts (audit log).
	OnEvent func(ctx context.Context, user, action string)
	// Now defaults to time.Now.
	Now func() time.Time
}

// Validate checks the configuration before serving.
func (s *Service) Validate() error {
	switch {
	case s.GitHub == nil || s.GitHub.ClientID == "":
		return errors.New("auth: a GitHub OAuth client ID is required (QUILL_GITHUB_CLIENT_ID)")
	case s.Store == nil:
		return errors.New("auth: a session store is required")
	case s.Cipher == nil:
		return fmt.Errorf("auth: %s is required to encrypt GitHub tokens", SecretKeyEnvVar)
	case len(s.Allowed) == 0:
		return errors.New("auth: the allow list is empty; set QUILL_ALLOWED_USERS to the GitHub logins of your team")
	}
	return nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) event(ctx context.Context, user, action string) {
	if s.OnEvent != nil {
		s.OnEvent(ctx, user, action)
	}
}

// Session is a successful login.
type Session struct {
	Token string `json:"token"`
	User  User   `json:"user"`
}

// FinishLogin polls GitHub once; when the user has approved, it checks the
// allow list and creates a session.
func (s *Service) FinishLogin(ctx context.Context, deviceCode string) (*Session, error) {
	ghToken, err := s.GitHub.PollToken(ctx, deviceCode)
	if err != nil {
		return nil, err
	}
	gu, err := s.GitHub.User(ctx, ghToken)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(s.Allowed, func(a string) bool { return strings.EqualFold(a, gu.Login) }) {
		s.event(ctx, gu.Login, "login_refused")
		return nil, fmt.Errorf("%w: %s", ErrNotAllowed, gu.Login)
	}
	sealed, err := s.Cipher.Seal(ghToken)
	if err != nil {
		return nil, err
	}
	if err := s.Store.PutUser(ctx, Record{ID: gu.Login, Name: gu.Name, EncryptedToken: sealed}); err != nil {
		return nil, err
	}
	token, err := NewSessionToken()
	if err != nil {
		return nil, err
	}
	ttl := s.TTL
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	if err := s.Store.CreateSession(ctx, HashToken(token), gu.Login, s.now().Add(ttl)); err != nil {
		return nil, err
	}
	s.event(ctx, gu.Login, "login")
	return &Session{Token: token, User: User{ID: gu.Login, Name: gu.Name}}, nil
}

// Authenticate returns the user of a session token.
func (s *Service) Authenticate(ctx context.Context, token string) (User, error) {
	if token == "" {
		return User{}, ErrNoSession
	}
	r, err := s.Store.LookupSession(ctx, HashToken(token), s.now())
	if err != nil {
		return User{}, err
	}
	gh, err := s.Cipher.Open(r.EncryptedToken)
	if err != nil {
		return User{}, err
	}
	return User{ID: r.ID, Name: r.Name, GitHubToken: gh}, nil
}

// Logout revokes a session token.
func (s *Service) Logout(ctx context.Context, token string) error {
	u, err := s.Authenticate(ctx, token)
	if err != nil {
		return err
	}
	if err := s.Store.DeleteSession(ctx, HashToken(token)); err != nil {
		return err
	}
	s.event(ctx, u.ID, "logout")
	return nil
}

// BearerToken reads "Authorization: Bearer <token>".
func BearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// Routes adds the login API to mux; none of it needs a session:
//
//	POST /auth/device -> DeviceCode (show UserCode, open VerificationURI)
//	POST /auth/token  {"device_code"} -> 200 Session; 428 pending / slow_down; 410 expired; 403 denied or not allowed
//	POST /auth/logout -> 204 (Bearer token)
//	GET  /auth/me     -> User (Bearer token)
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/device", func(w http.ResponseWriter, r *http.Request) {
		dc, err := s.GitHub.StartDevice(r.Context())
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, dc)
	})
	mux.HandleFunc("POST /auth/token", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			DeviceCode string `json:"device_code"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.DeviceCode == "" {
			writeError(w, http.StatusBadRequest, errors.New(`want {"device_code": "..."}`))
			return
		}
		sess, err := s.FinishLogin(r.Context(), body.DeviceCode)
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, sess)
		case errors.Is(err, ErrAuthorizationPending), errors.Is(err, ErrSlowDown):
			writeError(w, http.StatusPreconditionRequired, err)
		case errors.Is(err, ErrExpiredToken):
			writeError(w, http.StatusGone, err)
		case errors.Is(err, ErrAccessDenied), errors.Is(err, ErrNotAllowed):
			writeError(w, http.StatusForbidden, err)
		default:
			writeError(w, http.StatusBadGateway, err)
		}
	})
	mux.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Logout(r.Context(), BearerToken(r)); err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /auth/me", func(w http.ResponseWriter, r *http.Request) {
		u, err := s.Authenticate(r.Context(), BearerToken(r))
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		writeJSON(w, http.StatusOK, u)
	})
}

// Require lets requests with a valid session through, with the user in
// their context; others get 401.
func (s *Service) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := s.Authenticate(r.Context(), BearerToken(r))
		if errors.Is(err, ErrNoSession) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="quill"`)
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
