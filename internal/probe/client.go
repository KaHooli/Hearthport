package probe

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxBody caps how much of any response is read, so a misbehaving server
// cannot exhaust memory.
const maxBody = 8 << 20

// Client is a GET-only HTTP client. It has no method for other verbs on
// purpose: the probe must never change anything on the servers it checks.
type Client struct {
	BaseURL string
	Header  http.Header
	HTTP    *http.Client
}

// NewClient returns a client with a timeout and redirects limited to the same host.
func NewClient(baseURL string, header http.Header, timeout time.Duration) *Client {
	base, _ := url.Parse(strings.TrimRight(baseURL, "/"))
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Header:  header,
		HTTP: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				if base != nil && req.URL.Host != base.Host {
					return fmt.Errorf("refusing cross-host redirect to %s", req.URL.Host)
				}
				return nil
			},
		},
	}
}

// HTTPError is returned for non-2xx responses.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body)
}

// StatusOf returns the HTTP status of err, or 0 if err is not an HTTPError.
func StatusOf(err error) int {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// Get fetches path (relative to BaseURL, or absolute) and returns the body.
func (c *Client) Get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	u := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		u = c.BaseURL + path
	}
	if len(query) > 0 {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u += sep + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range c.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		return nil, &HTTPError{Status: resp.StatusCode, Body: msg}
	}
	return body, nil
}

// GetJSON fetches path and decodes the JSON body into v.
func (c *Client) GetJSON(ctx context.Context, path string, query url.Values, v any) error {
	body, err := c.Get(ctx, path, query)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decoding %s: %w", path, err)
	}
	return nil
}

// GetXML fetches path and decodes the XML body into v.
func (c *Client) GetXML(ctx context.Context, path string, query url.Values, v any) error {
	body, err := c.Get(ctx, path, query)
	if err != nil {
		return err
	}
	if err := xml.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decoding %s: %w", path, err)
	}
	return nil
}
