package link

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/itsankoff/incident-copilot/internal/evidence"
)

// Client is a Sensor's end of its link.
type Client struct {
	url, location, token string
	http                 *http.Client
}

// NewClient returns a Client that dials the Control plane at baseURL.
func NewClient(baseURL, location, token string) *Client {
	// The timeout outlasts a held poll and catches a link that died silently.
	return &Client{strings.TrimSuffix(baseURL, "/"), location, token, &http.Client{Timeout: pollHold + 10*time.Second}}
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.url+"/link/"+url.PathEscape(c.location)+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		return nil, ErrRefused
	}
	return resp, nil
}

// Next waits for the next Call. ok is false when none came during the poll.
func (c *Client) Next(ctx context.Context) (call evidence.Call, ok bool, err error) {
	resp, err := c.do(ctx, http.MethodGet, "/next", nil)
	if err != nil {
		return evidence.Call{}, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return evidence.Call{}, false, nil
	case http.StatusOK:
		err := json.NewDecoder(resp.Body).Decode(&call)
		return call, err == nil, err
	}
	return evidence.Call{}, false, fmt.Errorf("poll: status %d", resp.StatusCode)
}

// Reply sends the Answer to Call id.
func (c *Client) Reply(ctx context.Context, id string, a evidence.Answer) error {
	body, err := json.Marshal(a)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, "/replies/"+url.PathEscape(id), body)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("reply: status %d", resp.StatusCode)
	}
	return nil
}
