package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Device flow outcomes of PollToken (RFC 8628 §3.5).
var (
	ErrAuthorizationPending = errors.New("authorization_pending")
	ErrSlowDown             = errors.New("slow_down")
	ErrExpiredToken         = errors.New("expired_token")
	ErrAccessDenied         = errors.New("access_denied")
)

// Scopes requested at login: clone private repositories, push branches and
// open pull requests, and read the user's login.
const Scopes = "repo read:user"

// GitHub talks to GitHub's OAuth device flow and REST API.
type GitHub struct {
	// ClientID of the GitHub OAuth app (device flow enabled); required.
	ClientID string
	// BaseURL defaults to https://github.com.
	BaseURL string
	// APIURL defaults to https://api.github.com.
	APIURL string
	// HTTP defaults to a client with a 30s timeout.
	HTTP *http.Client
}

// DeviceCode is the first step of the device flow; the user enters
// UserCode at VerificationURI.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// GitHubUser is the subset of GET /user quill uses.
type GitHubUser struct {
	Login string `json:"login"`
	Name  string `json:"name"`
}

func (g *GitHub) base() string {
	return strings.TrimRight(orDefault(g.BaseURL, "https://github.com"), "/")
}
func (g *GitHub) api() string {
	return strings.TrimRight(orDefault(g.APIURL, "https://api.github.com"), "/")
}

func (g *GitHub) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// StartDevice requests a device and user code.
func (g *GitHub) StartDevice(ctx context.Context) (*DeviceCode, error) {
	var dc DeviceCode
	err := g.postForm(ctx, g.base()+"/login/device/code", url.Values{"client_id": {g.ClientID}, "scope": {Scopes}}, &dc)
	if err != nil {
		return nil, err
	}
	if dc.DeviceCode == "" || dc.UserCode == "" {
		return nil, errors.New("github: device code response is missing codes")
	}
	return &dc, nil
}

// PollToken exchanges a device code for an access token once the user has
// approved; until then it returns ErrAuthorizationPending or ErrSlowDown.
func (g *GitHub) PollToken(ctx context.Context, deviceCode string) (string, error) {
	var body struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	err := g.postForm(ctx, g.base()+"/login/oauth/access_token", url.Values{
		"client_id":   {g.ClientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}, &body)
	if err != nil {
		return "", err
	}
	switch body.Error {
	case "":
		if body.AccessToken == "" {
			return "", errors.New("github: token response has no access_token")
		}
		return body.AccessToken, nil
	case "authorization_pending":
		return "", ErrAuthorizationPending
	case "slow_down":
		return "", ErrSlowDown
	case "expired_token":
		return "", ErrExpiredToken
	case "access_denied":
		return "", ErrAccessDenied
	default:
		return "", fmt.Errorf("github: %s: %s", body.Error, body.Description)
	}
}

// User returns the token's user.
func (g *GitHub) User(ctx context.Context, token string) (*GitHubUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.api()+"/user", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	var u GitHubUser
	if err := g.do(req, &u); err != nil {
		return nil, err
	}
	if u.Login == "" {
		return nil, errors.New("github: /user has no login")
	}
	return &u, nil
}

func (g *GitHub) postForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return g.do(req, out)
}

func (g *GitHub) do(req *http.Request, out any) error {
	resp, err := g.client().Do(req)
	if err != nil {
		return fmt.Errorf("github: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("github: read response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("github: %s %s: %s: %s", req.Method, req.URL.Path, resp.Status, strings.TrimSpace(string(raw)))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("github: decode %s: %w", req.URL.Path, err)
	}
	return nil
}
