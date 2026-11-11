// Package localtickets is the demo's ticketing adapter: the Control plane keeps
// each ticket and its comments in SQLite and shows them on a read-only page,
// so the demo needs no GitHub repository or token. It is for demo purposes
// only; production uses GitHub Issues, and ServiceNow is planned (#68). A
// ticket reference is the ticket's number.
package localtickets

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS tickets (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	title      TEXT NOT NULL,
	body       TEXT NOT NULL,
	closed     INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS comments (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	ticket_id  INTEGER NOT NULL REFERENCES tickets(id),
	body       TEXT NOT NULL,
	created_at TEXT NOT NULL
);`

// Tickets is the local ticketing adapter. It keeps its own database, apart
// from the Control plane's state: a ticket is written while the state's
// transaction holds that database's only connection.
type Tickets struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens or creates the tickets database at path.
func Open(path string, now func() time.Time) (*Tickets, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("tickets %s: %w", path, err)
	}
	return &Tickets{db: db, now: now}, nil
}

// Shutdown closes the database.
func (t *Tickets) Shutdown() error { return t.db.Close() }

// Name names the adapter in the Control plane's log.
func (t *Tickets) Name() string { return "local" }

// URL is ticket ref's page, relative to the Control plane's address.
func (t *Tickets) URL(ref string) string { return "/tickets/" + ref }

// Create stores a ticket and returns its number.
func (t *Tickets) Create(ctx context.Context, title, body string) (string, error) {
	res, err := t.db.ExecContext(ctx, `INSERT INTO tickets (title, body, created_at) VALUES (?, ?, ?)`, title, body, t.stamp())
	if err != nil {
		return "", fmt.Errorf("local tickets: create: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", fmt.Errorf("local tickets: create: %w", err)
	}
	return strconv.FormatInt(id, 10), nil
}

// Comment adds a comment to ticket ref.
func (t *Tickets) Comment(ctx context.Context, ref, body string) error {
	res, err := t.db.ExecContext(ctx, `INSERT INTO comments (ticket_id, body, created_at)
		SELECT id, ?, ? FROM tickets WHERE id = ?`, body, t.stamp(), ref)
	if err != nil {
		return fmt.Errorf("local tickets: comment on %s: %w", ref, err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return fmt.Errorf("local tickets: comment on %s: no such ticket", ref)
	}
	return nil
}

// State reports whether ticket ref is closed. The page is read-only, so only
// Close closes a local ticket, and it gives no reason.
func (t *Tickets) State(ctx context.Context, ref string) (bool, string, error) {
	var closed bool
	if err := t.db.QueryRowContext(ctx, `SELECT closed FROM tickets WHERE id = ?`, ref).Scan(&closed); err != nil {
		return false, "", fmt.Errorf("local tickets: read %s: %w", ref, err)
	}
	return closed, "", nil
}

// Close closes ticket ref.
func (t *Tickets) Close(ctx context.Context, ref string) error {
	return t.update(ctx, ref, `UPDATE tickets SET closed = 1 WHERE id = ?`, "close")
}

// SetTitle changes the title of ticket ref.
func (t *Tickets) SetTitle(ctx context.Context, ref, title string) error {
	return t.update(ctx, ref, `UPDATE tickets SET title = ? WHERE id = ?`, "retitle", title)
}

func (t *Tickets) update(ctx context.Context, ref, query, what string, args ...any) error {
	res, err := t.db.ExecContext(ctx, query, append(args, ref)...)
	if err != nil {
		return fmt.Errorf("local tickets: %s %s: %w", what, ref, err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return fmt.Errorf("local tickets: %s %s: no such ticket", what, ref)
	}
	return nil
}

func (t *Tickets) stamp() string { return t.now().UTC().Format(time.RFC3339) }

// Handler serves the read-only ticket page: GET /tickets lists the tickets,
// newest first, GET /tickets/{ref} shows one with its comments, and
// GET /tickets/{ref}.md is that ticket as stored, in markdown.
func (t *Tickets) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tickets", func(w http.ResponseWriter, r *http.Request) {
		list, err := t.list(r.Context())
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		render(w, view{Title: "Tickets", List: list})
	})
	mux.HandleFunc("GET /tickets/{ref}", func(w http.ResponseWriter, r *http.Request) {
		ref, asMarkdown := strings.CutSuffix(r.PathValue("ref"), ".md")
		tk, err := t.ticket(r.Context(), ref)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			http.NotFound(w, r)
		case err != nil:
			http.Error(w, "internal error", http.StatusInternalServerError)
		case asMarkdown:
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			// Each part ends in one newline, so a rule never turns the line
			// above it into a heading.
			_, _ = fmt.Fprintf(w, "# %s (#%s)\n\nOpened %s\n\n%s\n", tk.Title, tk.Ref, tk.Created, strings.TrimRight(tk.Body, "\n"))
			for _, c := range tk.Comments {
				_, _ = fmt.Fprintf(w, "\n---\n\nComment %s\n\n%s\n", c.Created, strings.TrimRight(c.Body, "\n"))
			}
		default:
			render(w, view{Title: "#" + tk.Ref + " " + tk.Title, Ticket: &tk})
		}
	})
	return mux
}

// ticket and comment hold the text as stored, in markdown.
type ticket struct {
	Ref          string
	Title        string
	Created      string
	Body         string
	Comments     []comment
	CommentCount int // in the list only
}

type comment struct {
	Created string
	Body    string
}

func (t *Tickets) list(ctx context.Context) ([]ticket, error) {
	rows, err := t.db.QueryContext(ctx, `SELECT t.id, t.title, t.created_at, COUNT(c.id)
		FROM tickets t LEFT JOIN comments c ON c.ticket_id = t.id GROUP BY t.id ORDER BY t.id DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ticket
	for rows.Next() {
		var tk ticket
		if err := rows.Scan(&tk.Ref, &tk.Title, &tk.Created, &tk.CommentCount); err != nil {
			return nil, err
		}
		out = append(out, tk)
	}
	return out, rows.Err()
}

func (t *Tickets) ticket(ctx context.Context, ref string) (ticket, error) {
	tk := ticket{Ref: ref}
	if err := t.db.QueryRowContext(ctx, `SELECT title, body, created_at FROM tickets WHERE id = ?`, ref).
		Scan(&tk.Title, &tk.Body, &tk.Created); err != nil {
		return ticket{}, err
	}
	rows, err := t.db.QueryContext(ctx, `SELECT body, created_at FROM comments WHERE ticket_id = ? ORDER BY id`, ref)
	if err != nil {
		return ticket{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var c comment
		if err := rows.Scan(&c.Body, &c.Created); err != nil {
			return ticket{}, err
		}
		tk.Comments = append(tk.Comments, c)
	}
	return tk, rows.Err()
}

// markdown renders ticket text as a tracker would. goldmark's defaults omit
// raw HTML and drop dangerous link targets such as javascript:, so text from
// an Alert cannot inject markup.
func markdown(s string) template.HTML {
	var b bytes.Buffer
	if err := goldmark.Convert([]byte(s), &b); err != nil {
		return template.HTML(template.HTMLEscapeString(s))
	}
	return template.HTML(b.String()) //nolint:gosec // goldmark output with raw HTML omitted
}

type view struct {
	Title  string
	List   []ticket
	Ticket *ticket
}

func render(w http.ResponseWriter, v view) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = page.Execute(w, v)
}

var page = template.Must(template.New("page").Funcs(template.FuncMap{"markdown": markdown}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · incident-copilot</title>
<style>
body { font: 15px/1.5 system-ui, sans-serif; max-width: 60rem; margin: 0 auto; padding: 1rem; color: #1f2328; background: #fff; }
a { color: #0969da; }
.demo { background: #fff8c5; border: 1px solid #d4a72c; border-radius: 6px; padding: .5rem .75rem; }
table { border-collapse: collapse; width: 100%; }
td, th { text-align: left; padding: .4rem .5rem; border-bottom: 1px solid #d0d7de; }
.box { border: 1px solid #d0d7de; border-radius: 6px; margin: 1rem 0; }
.box header { background: #f6f8fa; border-bottom: 1px solid #d0d7de; padding: .4rem .75rem; color: #59636e; font-size: 13px; }
.box > div { padding: 0 .75rem; overflow-wrap: anywhere; }
code { background: #eff1f3; border-radius: 4px; padding: 0 .25rem; }
</style>
</head>
<body>
<p class="demo"><strong>Demo only.</strong> These tickets come from incident-copilot's local ticketing adapter, which is for demo purposes only. Production uses GitHub Issues; ServiceNow is planned (#68).</p>
{{- with .Ticket}}
<p><a href="/tickets">All tickets</a></p>
<h1>{{.Title}} <span style="color:#59636e">#{{.Ref}}</span></h1>
<section class="box"><header>Opened {{.Created}}</header><div>{{markdown .Body}}</div></section>
{{- range .Comments}}
<section class="box"><header>Comment {{.Created}}</header><div>{{markdown .Body}}</div></section>
{{- end}}
{{- else}}
<h1>Tickets</h1>
{{- if .List}}
<table>
<tr><th>#</th><th>Title</th><th>Opened</th><th>Comments</th></tr>
{{- range .List}}
<tr><td>{{.Ref}}</td><td><a href="/tickets/{{.Ref}}">{{.Title}}</a></td><td>{{.Created}}</td><td>{{.CommentCount}}</td></tr>
{{- end}}
</table>
{{- else}}
<p>No tickets yet.</p>
{{- end}}
{{- end}}
</body>
</html>
`))
