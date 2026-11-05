// Package core calls the internal API of core-service: service tokens (client credentials) and
// access checks of users to properties (SECURITY.md §3).
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/inspecto-system/media-service/internal/config"
)

// Action is the kind of access asked for.
type Action string

// Access actions of GET /internal/v1/access/properties/{id}.
const (
	Read  Action = "read"
	Write Action = "write"
)

const (
	requestTimeout = 3 * time.Second
	// accessCacheTTL bounds how long a positive decision is reused (SECURITY.md §3: at most 60 s).
	accessCacheTTL = 60 * time.Second
	// tokenRenewMargin renews the service token before it expires.
	tokenRenewMargin = 30 * time.Second
	maxResponseBytes = 64 << 10
)

// ErrUnavailable means core-service could not answer; callers report DEPENDENCY_UNAVAILABLE.
var ErrUnavailable = errors.New("core-service unavailable")

// Client is safe for concurrent use.
type Client struct {
	base   string
	id     string
	secret string
	http   *http.Client
	now    func() time.Time

	mu           sync.Mutex
	token        string
	tokenExpires time.Time
	allowed      map[accessKey]time.Time
}

type accessKey struct {
	property, user uuid.UUID
	action         Action
}

// New returns a client of the core-service internal API.
func New(cfg config.Core) *Client {
	return &Client{
		base:    strings.TrimRight(cfg.URL, "/"),
		id:      cfg.ClientID,
		secret:  cfg.ClientSecret,
		http:    &http.Client{Timeout: requestTimeout},
		now:     time.Now,
		allowed: make(map[accessKey]time.Time),
	}
}

// CanAccess tells whether the user may perform the action on the property. A missing property and
// a property of another user are the same "no" (core answers 404 for both).
func (c *Client) CanAccess(ctx context.Context, propertyID, userID uuid.UUID, action Action) (bool, error) {
	key := accessKey{property: propertyID, user: userID, action: action}
	if c.cachedAllowed(key) {
		return true, nil
	}
	status, err := c.checkAccess(ctx, key)
	if status == http.StatusUnauthorized {
		// The token may have been revoked by a restart of core: get a new one and retry once.
		c.dropToken()
		status, err = c.checkAccess(ctx, key)
	}
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusOK:
		c.remember(key)
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("%w: access check answered %d", ErrUnavailable, status)
	}
}

func (c *Client) checkAccess(ctx context.Context, key accessKey) (int, error) {
	token, err := c.serviceToken(ctx)
	if err != nil {
		return 0, err
	}
	query := url.Values{"userId": {key.user.String()}, "action": {string(key.action)}}
	endpoint := c.base + "/internal/v1/access/properties/" + key.property.String() + "?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer closeBody(resp)
	return resp.StatusCode, nil
}

// serviceToken returns a cached service token or obtains a new one.
func (c *Client) serviceToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.token != "" && c.now().Before(c.tokenExpires) {
		token := c.token
		c.mu.Unlock()
		return token, nil
	}
	c.mu.Unlock()

	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {c.id}, "client_secret": {c.secret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/internal/v1/auth/service-token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer closeBody(resp)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: service token request answered %d", ErrUnavailable, resp.StatusCode)
	}
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&body); err != nil || body.AccessToken == "" {
		return "", fmt.Errorf("%w: unreadable service token response", ErrUnavailable)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = body.AccessToken
	c.tokenExpires = c.now().Add(time.Duration(body.ExpiresIn)*time.Second - tokenRenewMargin)
	return c.token, nil
}

func (c *Client) dropToken() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = ""
}

func (c *Client) cachedAllowed(key accessKey) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.allowed[key]
	if ok && c.now().Before(until) {
		return true
	}
	delete(c.allowed, key)
	return false
}

func (c *Client) remember(key accessKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, until := range c.allowed { // drop expired entries so the map does not grow forever
		if !now.Before(until) {
			delete(c.allowed, k)
		}
	}
	c.allowed[key] = now.Add(accessCacheTTL)
}

// closeBody drains a little of the body so the connection can be reused, then closes it.
func closeBody(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	_ = resp.Body.Close()
}
