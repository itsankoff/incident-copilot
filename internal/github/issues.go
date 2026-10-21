// Package github is the GitHub Issues ticketing adapter: one issue per
// Incident, with later Signals as comments. A ticket reference is the issue
// number.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Issues writes tickets to one repository through the GitHub REST API.
type Issues struct {
	apiURL string // e.g. https://api.github.com
	repo   string // owner/name
	token  string
	client *http.Client
}

func New(apiURL, repo, token string, client *http.Client) *Issues {
	return &Issues{apiURL: strings.TrimSuffix(apiURL, "/"), repo: repo, token: token, client: client}
}

// Create opens an issue and returns its number.
func (g *Issues) Create(ctx context.Context, title, body string) (string, error) {
	var out struct {
		Number int `json:"number"`
	}
	if err := g.post(ctx, "/repos/"+g.repo+"/issues", map[string]string{"title": title, "body": body}, &out); err != nil {
		return "", fmt.Errorf("github: create issue: %w", err)
	}
	return strconv.Itoa(out.Number), nil
}

// Comment adds a comment to issue ref.
func (g *Issues) Comment(ctx context.Context, ref, body string) error {
	if err := g.post(ctx, "/repos/"+g.repo+"/issues/"+ref+"/comments", map[string]string{"body": body}, nil); err != nil {
		return fmt.Errorf("github: comment on issue %s: %w", ref, err)
	}
	return nil
}

func (g *Issues) post(ctx context.Context, path string, in map[string]string, out any) error {
	b, _ := json.Marshal(in) // a map of strings always marshals
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.apiURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("status %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
