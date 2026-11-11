package demorun

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// UI is the runner's terminal output: step headers, notes, a live status
// line with a spinner, and the timeline. Colour and the live line are used
// only on a terminal, and colour not with NO_COLOR set.
type UI struct {
	out   io.Writer
	in    *bufio.Reader
	tty   bool // out is a terminal
	inTTY bool // in is a terminal, which echoes the answers itself
	color bool

	mu     sync.Mutex
	status string
	frame  int
	stop   chan struct{}
}

// NewUI writes to out and reads answers from in.
func NewUI(out *os.File, in io.Reader) *UI {
	tty := false
	if fi, err := out.Stat(); err == nil {
		tty = fi.Mode()&os.ModeCharDevice != 0
	}
	u := &UI{out: out, in: bufio.NewReader(in), tty: tty, color: tty && os.Getenv("NO_COLOR") == "", stop: make(chan struct{})}
	if f, ok := in.(*os.File); ok {
		if fi, err := f.Stat(); err == nil {
			u.inTTY = fi.Mode()&os.ModeCharDevice != 0
		}
	}
	if tty {
		go u.spin()
	}
	return u
}

const (
	bold   = "1"
	dim    = "2"
	red    = "31"
	green  = "32"
	yellow = "33"
	blue   = "34"
	purple = "35"
	cyan   = "36"
)

func (u *UI) paint(code, s string) string {
	if !u.color || s == "" {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

// println prints above the live status line.
func (u *UI) println(s string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clearStatus()
	_, _ = fmt.Fprintln(u.out, s)
	u.drawStatus()
}

func (u *UI) clearStatus() {
	if u.tty && u.status != "" {
		_, _ = fmt.Fprint(u.out, "\r\033[K")
	}
}

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (u *UI) drawStatus() {
	if u.tty && u.status != "" {
		_, _ = fmt.Fprintf(u.out, "  %s %s", u.paint(cyan, spinner[u.frame%len(spinner)]), u.paint(dim, u.status))
	}
}

func (u *UI) spin() {
	t := time.NewTicker(120 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-u.stop:
			return
		case <-t.C:
			u.mu.Lock()
			u.frame++
			u.clearStatus()
			u.drawStatus()
			u.mu.Unlock()
		}
	}
}

// Status sets the live line under the output; "" removes it. Off a
// terminal it prints nothing: the step lines say what is happening.
func (u *UI) Status(s string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clearStatus()
	u.status = s
	u.drawStatus()
}

// Close removes the live line and stops the spinner.
func (u *UI) Close() {
	u.Status("")
	close(u.stop)
}

// Title prints the runner's banner.
func (u *UI) Title(s string) { u.println(u.paint(bold, s)) }

// Step prints a step header, n of total, with one line on what the step is for.
func (u *UI) Step(n, total int, title, why string) {
	u.println("")
	u.println(u.paint(bold+";"+blue, fmt.Sprintf("==> [%d/%d] %s", n, total, title)))
	if why != "" {
		u.Note(why)
	}
}

// Section prints a header for what is not a numbered step.
func (u *UI) Section(title string) {
	u.println("")
	u.println(u.paint(bold+";"+blue, "==> "+title))
}

// Note prints an explanation.
func (u *UI) Note(lines ...string) {
	for _, l := range lines {
		u.println("    " + u.paint(dim, l))
	}
}

// Plain prints lines as they are, indented.
func (u *UI) Plain(lines ...string) {
	for _, l := range lines {
		u.println("    " + l)
	}
}

func (u *UI) OK(s string)   { u.println("  " + u.paint(green, "✓") + " " + s) }
func (u *UI) Warn(s string) { u.println("  " + u.paint(yellow, "!") + " " + s) }
func (u *UI) Fail(s string) { u.println("  " + u.paint(red, "✗") + " " + s) }

// sourceColors tells the timeline's sources apart.
var sourceColors = map[string]string{
	"fault": red, "kubernetes": yellow, "alertmanager": purple,
	"control-plane": cyan, "sensor": blue,
}

// Event prints one timeline line: when, after the fault, which source says
// so, and what.
func (u *UI) Event(since time.Duration, source, text string) {
	when := "+" + Clock(since)
	u.println(fmt.Sprintf("  %s  %s %s", u.paint(dim, when), u.paint(sourceColors[source], fmt.Sprintf("%-13s", source)), text))
}

// Ask asks a yes/no question. An empty answer is def; no answer at all (end
// of input, as with no terminal) is always no.
func (u *UI) Ask(q string, def bool) bool {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	a, ok := u.Line(q + " " + hint + " ")
	if !ok {
		return false
	}
	switch strings.ToLower(a) {
	case "":
		return def
	case "y", "yes":
		return true
	}
	return false
}

// Line asks for one line of input and reports false at end of input.
func (u *UI) Line(prompt string) (string, bool) {
	u.mu.Lock()
	u.clearStatus()
	u.status = "" // the spinner must not draw over what the user types
	_, _ = fmt.Fprint(u.out, "  "+u.paint(bold, "?")+" "+prompt)
	u.mu.Unlock()
	s, err := u.in.ReadString('\n')
	if err != nil && s == "" {
		u.println("")
		return "", false
	}
	s = strings.TrimSpace(s)
	if !u.inTTY {
		u.println(s)
	}
	return s, true
}

// Clock renders d as minutes:seconds.
func Clock(d time.Duration) string {
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

// Bar renders done of total as a fixed-width progress bar.
func Bar(done, total time.Duration, width int) string {
	n := width
	if total > 0 && done < total {
		n = int(int64(width) * int64(done) / int64(total))
	}
	return "▕" + strings.Repeat("█", n) + strings.Repeat("░", width-n) + "▏"
}
