package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type request struct {
	method, path, auth, accept string
	body                       map[string]string
}

// fakeGitHub records requests and answers each with status and body.
func fakeGitHub(t *testing.T, status int, body string) (*httptest.Server, *[]request) {
	t.Helper()
	var got []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]string
		_ = json.Unmarshal(b, &m)
		got = append(got, request{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Accept"), m})
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestCreate(t *testing.T) {
	srv, got := fakeGitHub(t, http.StatusCreated, `{"number":42,"html_url":"https://github.com/o/r/issues/42"}`)
	iss := New(srv.URL, "o/r", "tok", srv.Client())

	ref, err := iss.Create(context.Background(), "KubePodCrashLooping on demo/orders", "body text")
	if err != nil {
		t.Fatal(err)
	}
	if ref != "42" {
		t.Errorf("ref %q, want 42", ref)
	}
	want := request{"POST", "/repos/o/r/issues", "Bearer tok", "application/vnd.github+json",
		map[string]string{"title": "KubePodCrashLooping on demo/orders", "body": "body text"}}
	if len(*got) != 1 || !equal((*got)[0], want) {
		t.Errorf("requests %+v, want [%+v]", *got, want)
	}
}

func TestComment(t *testing.T) {
	srv, got := fakeGitHub(t, http.StatusCreated, `{"id":1}`)
	iss := New(srv.URL, "o/r", "tok", srv.Client())

	if err := iss.Comment(context.Background(), "42", "joined"); err != nil {
		t.Fatal(err)
	}
	want := request{"POST", "/repos/o/r/issues/42/comments", "Bearer tok", "application/vnd.github+json",
		map[string]string{"body": "joined"}}
	if len(*got) != 1 || !equal((*got)[0], want) {
		t.Errorf("requests %+v, want [%+v]", *got, want)
	}
}

func TestState(t *testing.T) {
	for _, tt := range []struct {
		body   string
		closed bool
		reason string
	}{
		{`{"number":42,"state":"open","state_reason":null}`, false, ""},
		{`{"number":42,"state":"closed","state_reason":"completed"}`, true, "completed"},
		{`{"number":42,"state":"closed","state_reason":"not_planned"}`, true, "not_planned"},
	} {
		srv, got := fakeGitHub(t, http.StatusOK, tt.body)
		iss := New(srv.URL, "o/r", "tok", srv.Client())

		closed, reason, err := iss.State(context.Background(), "42")
		if err != nil {
			t.Fatal(err)
		}
		if closed != tt.closed || reason != tt.reason {
			t.Errorf("%s: closed %v reason %q, want %v %q", tt.body, closed, reason, tt.closed, tt.reason)
		}
		want := request{"GET", "/repos/o/r/issues/42", "Bearer tok", "application/vnd.github+json", nil}
		if len(*got) != 1 || !equal((*got)[0], want) {
			t.Errorf("requests %+v, want [%+v]", *got, want)
		}
	}
}

func TestCloseAndSetTitle(t *testing.T) {
	srv, got := fakeGitHub(t, http.StatusOK, `{"number":42}`)
	iss := New(srv.URL, "o/r", "tok", srv.Client())

	if err := iss.Close(context.Background(), "42"); err != nil {
		t.Fatal(err)
	}
	if err := iss.SetTitle(context.Background(), "42", "[warning] A on demo/orders"); err != nil {
		t.Fatal(err)
	}
	want := []request{
		{"PATCH", "/repos/o/r/issues/42", "Bearer tok", "application/vnd.github+json", map[string]string{"state": "closed", "state_reason": "completed"}},
		{"PATCH", "/repos/o/r/issues/42", "Bearer tok", "application/vnd.github+json", map[string]string{"title": "[warning] A on demo/orders"}},
	}
	if len(*got) != 2 || !equal((*got)[0], want[0]) || !equal((*got)[1], want[1]) {
		t.Errorf("requests %+v, want %+v", *got, want)
	}
}

func TestErrorStatus(t *testing.T) {
	srv, _ := fakeGitHub(t, http.StatusUnauthorized, `{"message":"Bad credentials"}`)
	iss := New(srv.URL, "o/r", "tok", srv.Client())

	if _, err := iss.Create(context.Background(), "t", "b"); err == nil {
		t.Error("Create: no error on 401")
	}
	if err := iss.Comment(context.Background(), "42", "b"); err == nil {
		t.Error("Comment: no error on 401")
	}
	if _, _, err := iss.State(context.Background(), "42"); err == nil {
		t.Error("State: no error on 401")
	}
	if err := iss.Close(context.Background(), "42"); err == nil {
		t.Error("Close: no error on 401")
	}
	if err := iss.SetTitle(context.Background(), "42", "t"); err == nil {
		t.Error("SetTitle: no error on 401")
	}
}

func equal(a, b request) bool {
	if a.method != b.method || a.path != b.path || a.auth != b.auth || a.accept != b.accept || len(a.body) != len(b.body) {
		return false
	}
	for k, v := range b.body {
		if a.body[k] != v {
			return false
		}
	}
	return true
}

func TestIssueURLIsKnownOnlyForGitHubDotCom(t *testing.T) {
	if got := New("https://api.github.com", "o/r", "tok", nil).URL("42"); got != "https://github.com/o/r/issues/42" {
		t.Errorf("github.com URL = %q", got)
	}
	if got := New("https://ghe.example/api/v3", "o/r", "tok", nil).URL("42"); got != "" {
		t.Errorf("GitHub Enterprise URL = %q, want none", got)
	}
}
