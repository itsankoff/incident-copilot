// Command demo is the make demo runner. make demo's bootstrap (bootstrap.sh)
// checks the prerequisites and starts it from the repository root. It sets up
// both kind clusters, deploys the demo app, the Control plane, and the Sensor,
// injects a fault, and follows it to an RCA in a live timeline.
//
//	go run ./cmd/demo [-fault crash-loop] [-timeout 20m]
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	ossignal "os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/itsankoff/incident-copilot/internal/demorun"
	"github.com/itsankoff/incident-copilot/internal/fault"
)

const (
	targetCtx = "kind-ic-target"
	cpCtx     = "kind-ic-control-plane"
	envFile   = ".env.demo"
	logDir    = "build/demo"
	steps     = 8
)

func main() {
	faultName := flag.String("fault", "crash-loop", "the fault scenario to roll out (eval/scenarios)")
	timeout := flag.Duration("timeout", 20*time.Minute, "how long to follow the fault to its RCA")
	flag.Parse()

	ctx, stop := ossignal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ui := demorun.NewUI(os.Stdout, os.Stdin)
	r := &runner{ui: ui, timeout: *timeout}
	err := r.run(ctx, *faultName)
	r.stopPage()
	ui.Close()
	switch {
	case ctx.Err() != nil:
		fmt.Println("\nInterrupted. Run make demo again any time: it reuses what is already set up.")
		os.Exit(130)
	case err != nil:
		fmt.Fprintln(os.Stderr, "\nmake demo:", err)
		os.Exit(1)
	}
}

type runner struct {
	ui      *demorun.UI
	timeout time.Duration

	scenario fault.Scenario
	hasKey   bool
	page     *exec.Cmd // the port-forward to the Control plane
	pageBase string    // where page serves the Control plane, such as http://localhost:8080
}

func (r *runner) run(ctx context.Context, faultName string) error {
	all, err := fault.LoadScenarios("eval/scenarios")
	if err != nil {
		return err
	}
	i := slices.IndexFunc(all, func(s fault.Scenario) bool { return s.Name == faultName })
	if i < 0 {
		var names []string
		for _, s := range all {
			names = append(names, s.Name)
		}
		return fmt.Errorf("no fault scenario %q: pick one of %s, as in make demo F=oom", faultName, strings.Join(names, ", "))
	}
	r.scenario = all[i]
	ui := r.ui

	ui.Title("incident-copilot demo: break a rollout, and watch incident-copilot investigate it")
	ui.Note(fmt.Sprintf("Fault: %s, a rollout of %s: %q.", r.scenario.Name, r.scenario.Inject.Target, r.scenario.Inject.ChangeCause),
		"Each step's full output goes to "+logDir+"/. Running make demo again reuses what is already set up.")

	ui.Step(1, steps, "LLM key and ticketing", "incident-copilot investigates with an LLM through OpenRouter, the only credential the demo needs.")
	if err := r.setupKey(); err != nil {
		return err
	}

	ui.Step(2, steps, "Kubernetes clusters",
		"Two kind clusters, with Docker containers as nodes: ic-target stands in for your production cluster,")
	ui.Note("and ic-control-plane runs incident-copilot apart from it, as it would in production. Existing clusters are reused.")
	if err := r.make(ctx, "kind-clusters", "creating or reusing ic-target and ic-control-plane", "Clusters ic-target and ic-control-plane are up"); err != nil {
		return err
	}

	ui.Step(3, steps, "Signal sources and observability in ic-target",
		"Prometheus scrapes the demo app and Kubernetes and fires alerts, Alertmanager sends them to the Control plane,")
	ui.Note("Loki and Alloy collect the pods' logs, and Grafana shows all of it. The first install takes a few minutes.")
	if err := r.make(ctx, "observability", "installing Prometheus, Alertmanager, Loki, Alloy, and Grafana with Helm", "Prometheus, Alertmanager, Loki, Alloy, and Grafana are running"); err != nil {
		return err
	}

	ui.Step(4, steps, "Demo app in ic-target",
		"orders, inventory, and loadgen: a small shop under constant load. The fault breaks orders.")
	if err := r.make(ctx, "deploy", "building demo-svc and rolling it out", "orders, inventory, and loadgen are Ready"); err != nil {
		return err
	}

	ui.Step(5, steps, "Control plane in ic-control-plane",
		"It receives alerts, runs Triage, opens a ticket per Incident, and investigates each new Incident with the LLM.")
	if err := r.resetState(ctx); err != nil {
		return err
	}
	if err := r.make(ctx, "control-plane", "building incident-copilot and deploying the Control plane", "The Control plane is running"); err != nil {
		return err
	}

	ui.Step(6, steps, "Sensor in ic-target",
		"A read-only component that answers the investigation's Evidence queries about ic-target. It dials out")
	ui.Note("to the Control plane, so nothing connects into ic-target.")
	if err := r.make(ctx, "sensor", "deploying the Sensor", "The Sensor is running"); err != nil {
		return err
	}
	r.waitLinkUp(ctx)

	ui.Step(7, steps, "Healthy baseline", "Before the fault, every demo pod is Ready and no demo alert fires.")
	if err := r.make(ctx, "clear", "restoring the demo app's baseline", "The demo app is healthy"); err != nil {
		return err
	}

	ui.Step(8, steps, "The fault, live",
		"From here on, one timeline: what the cluster does, what Alertmanager fires, and what the Control plane and the")
	ui.Note("Sensor log about the Incident, from the Signal to the RCA in the ticket.")
	f, err := r.follow(ctx)
	if err != nil {
		return err
	}
	if err := r.result(ctx, f); err != nil {
		return err
	}
	return r.cleanup(ctx)
}

// setupKey makes sure .env.demo holds an OpenRouter key, or explains what
// the demo shows without one, and says where tickets go.
func (r *runner) setupKey() error {
	ui := r.ui
	env, err := demorun.ReadEnvFile(envFile)
	if err != nil {
		return err
	}
	switch key := os.Getenv("OPENROUTER_API_KEY"); {
	case env["OPENROUTER_API_KEY"] != "":
		ui.OK("OpenRouter key found in .env.demo")
		r.hasKey = true
	case key != "" && ui.Ask("Your shell sets OPENROUTER_API_KEY. Save it to .env.demo, where the Control plane's deploy reads it?", true):
		if err := demorun.SetEnv(envFile, "OPENROUTER_API_KEY", key); err != nil {
			return err
		}
		ui.OK("Saved to .env.demo, which only you can read and Git ignores")
		r.hasKey = true
	default:
		ui.Warn("No OpenRouter key in .env.demo. Getting one is free:")
		ui.Plain("1. Create an account at https://openrouter.ai (Google, GitHub, or email).",
			"2. Open https://openrouter.ai/settings/keys and choose Create API Key. No credit is needed:",
			"   the demo uses OpenRouter's free models (IC_LLM_MODEL=openrouter/free).",
			"3. Paste the key below, or put OPENROUTER_API_KEY=sk-or-... into .env.demo at the repository root.")
		key, ok := r.secret("Paste your OpenRouter key, or press Enter to go on without one: ")
		if ok && key != "" {
			if err := demorun.SetEnv(envFile, "OPENROUTER_API_KEY", key); err != nil {
				return err
			}
			ui.OK("Saved to .env.demo, which only you can read and Git ignores")
			r.hasKey = true
		} else {
			ui.Warn("Going on without a key: the Control plane opens the ticket but cannot investigate, so there is no RCA.")
		}
	}
	if env["IC_GITHUB_REPO"] != "" && env["IC_GITHUB_TOKEN"] != "" {
		ui.OK("Tickets go to GitHub Issues in " + env["IC_GITHUB_REPO"] + ", because .env.demo sets IC_GITHUB_REPO")
	} else {
		ui.OK("Tickets go to the Control plane's local ticket page")
		ui.Note("Local ticketing is for the demo only. In production, incident-copilot writes to GitHub Issues (ServiceNow is planned).")
	}
	return nil
}

// secret reads a line without echoing it on a terminal.
func (r *runner) secret(prompt string) (string, bool) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return r.ui.Line(prompt)
	}
	r.ui.Status("")
	fmt.Print("  ? " + prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	return strings.TrimSpace(string(b)), err == nil
}

// make runs one Makefile target with its output in logDir, and a spinner.
func (r *runner) make(ctx context.Context, target, doing, done string, vars ...string) error {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	logPath := filepath.Join(logDir, target+".log")
	log, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	cmd := exec.CommandContext(ctx, "make", append([]string{target}, vars...)...)
	cmd.Stdout, cmd.Stderr = log, log
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		r.ui.Status(fmt.Sprintf("%s (make %s, %s)", doing, target, clock(time.Since(start))))
		select {
		case err := <-exited:
			r.ui.Status("")
			took := clock(time.Since(start))
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				r.ui.Fail(fmt.Sprintf("make %s failed after %s. Its last lines (all of them are in %s):", target, took, logPath))
				r.ui.Plain(tail(logPath, 15)...)
				return fmt.Errorf("make %s: %w", target, err)
			}
			r.ui.OK(fmt.Sprintf("%s (%s)", done, took))
			return nil
		case <-tick.C:
		}
	}
}

func clock(d time.Duration) string {
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

func tail(path string, n int) []string {
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	return lines[max(0, len(lines)-n):]
}

func kubectl(ctx context.Context, kubeContext string, args ...string) ([]byte, error) {
	return fault.KubectlFor(kubeContext)(ctx, args...)
}

// resetState offers a clean Control plane state on a re-run: an Incident
// from an earlier run would still own orders, so the fault would join it and
// not be investigated again.
func (r *runner) resetState(ctx context.Context) error {
	icp := func(args ...string) error {
		_, err := kubectl(ctx, cpCtx, append([]string{"-n", "incident-copilot"}, args...)...)
		return err
	}
	if icp("get", "pvc", "control-plane-state") != nil {
		return nil // the first run: there is no state yet
	}
	r.ui.Note("The Control plane still holds the Incidents of an earlier run. The fault would join the open Incident about",
		"orders instead of opening a new one, and a joined Incident is not investigated again.")
	if !r.ui.Ask("Start the Control plane from a clean state? This deletes the demo's Incidents, local tickets, and audit log.", true) {
		r.ui.Note("Keeping the state: expect the fault to join the earlier Incident, with no new RCA.")
		return nil
	}
	r.ui.Status("deleting the Control plane's state")
	for _, args := range [][]string{
		{"scale", "deploy/control-plane", "--replicas=0"},
		{"wait", "--for=delete", "pod", "-l", "app.kubernetes.io/name=control-plane", "--timeout=120s"},
		{"delete", "pvc", "control-plane-state", "--wait", "--timeout=120s"},
		// make control-plane creates the volume again, and the pod waits for it.
		{"scale", "deploy/control-plane", "--replicas=1"},
	} {
		if err := icp(args...); err != nil {
			r.ui.Status("")
			return err
		}
	}
	r.ui.Status("")
	r.ui.OK("The Control plane's state is cleared")
	return nil
}

// waitLinkUp waits for the Sensor to log link.up.
func (r *runner) waitLinkUp(ctx context.Context) {
	r.ui.Status("waiting for the Sensor to dial out to the Control plane")
	defer r.ui.Status("")
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		out, _ := kubectl(ctx, targetCtx, "-n", "incident-copilot", "logs", "deploy/sensor", "-c", "sensor", "--tail=200")
		last := ""
		for _, l := range strings.Split(string(out), "\n") {
			if m, ok := demorun.ParseMilestone([]byte(l)); ok {
				last = m.Msg
			}
		}
		if last == "link.up" {
			r.ui.Status("")
			r.ui.OK("Sensor link up: the Sensor dialed out to the Control plane")
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
	r.ui.Status("")
	r.ui.Warn("The Sensor has not logged link.up yet. Without the link the investigation has no evidence;")
	r.ui.Note("see Troubleshooting in docs/demo.md.")
}

// event is one timeline line, from a source, or a milestone to render.
type event struct {
	source    string
	text      string
	milestone *demorun.Milestone
}

// follow injects the fault and shows the timeline until the fault's Incident
// is ready for review, or it is clear that it will not be.
func (r *runner) follow(ctx context.Context) (*demorun.Follow, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ui, s := r.ui, r.scenario
	r.startPage()

	events := make(chan event, 64)
	since := time.Now().Add(-5 * time.Second) // the nodes' clocks may differ a little
	go streamLogs(ctx, cpCtx, "control-plane", "control-plane", since, events)
	go streamLogs(ctx, targetCtx, "sensor", "sensor", since, events)
	pods, alerts := &demorun.Pods{Target: s.Inject.Target}, &demorun.Alerts{}
	// The baseline is not news: only changes after the fault are.
	if b, err := kubectl(ctx, targetCtx, "-n", fault.Namespace, "get", "pods", "-o", "json"); err == nil {
		_, _ = pods.Diff(b)
	}
	demo := fault.Demo{Kubectl: fault.KubectlFor(targetCtx)}
	if a, err := demo.FiringAlerts(ctx); err == nil {
		alerts.Diff(a)
	}

	if err := r.make(ctx, "fault", "rolling out the fault", "Fault rolled out", "F="+s.Name); err != nil {
		return nil, err
	}
	start := time.Now()
	ui.Section("Timeline (minutes:seconds since the fault)")
	ui.Event(0, "fault", fmt.Sprintf("rolled out to %s: %q", s.Inject.Target, s.Inject.ChangeCause))
	go poll(ctx, 3*time.Second, events, func() []string {
		b, err := kubectl(ctx, targetCtx, "-n", fault.Namespace, "get", "pods", "-o", "json")
		if err != nil {
			return nil
		}
		lines, _ := pods.Diff(b)
		return lines
	}, "kubernetes")
	go poll(ctx, 5*time.Second, events, func() []string {
		a, err := demo.FiringAlerts(ctx)
		if err != nil {
			return nil
		}
		return alerts.Diff(a)
	}, "alertmanager")

	f := &demorun.Follow{Target: s.Inject.Target}
	var alerted bool
	var joinedAt time.Time
	step := ""
	deadline := time.After(r.timeout)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		switch {
		case f.Ready:
			return f, nil
		case !r.hasKey && f.TicketRef != "":
			return f, nil
		case f.Decision == "join" && !f.Investigating:
			if joinedAt.IsZero() {
				joinedAt = time.Now()
			} else if time.Since(joinedAt) > 90*time.Second {
				return f, nil
			}
		}
		select {
		case e := <-events:
			text := e.text
			if m := e.milestone; m != nil {
				if !f.Observe(*m) {
					continue
				}
				text = m.String()
				if m.Msg == "investigation.step" {
					text, step = "  "+text, m.Str("step")
				}
			}
			alerted = alerted || e.source == "alertmanager"
			ui.Event(time.Since(start), e.source, text)
		case <-tick.C:
		case <-deadline:
			ui.Status("")
			ui.Fail(fmt.Sprintf("The fault's Incident was not ready for review after %s.", r.timeout))
			ui.Note("Check the pods with make status, and the Control plane's log with:",
				"kubectl --context "+cpCtx+" -n incident-copilot logs deploy/control-plane",
				"docs/demo.md has a Troubleshooting section.")
			return nil, errors.New("timed out following the fault")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		elapsed := time.Since(start)
		switch {
		case f.Incident == 0 && !alerted:
			within := time.Duration(s.Expect.AlertWithin)
			ui.Status(fmt.Sprintf("waiting for Prometheus to fire %s %s %s of up to %s",
				strings.Join(s.Expect.Alerts, " or "), demorun.Bar(elapsed, within, 20), clock(elapsed), clock(within)))
		case f.Incident == 0:
			ui.Status("waiting for a Signal to reach the Control plane " + clock(elapsed))
		case f.Investigating && step != "":
			ui.Status(fmt.Sprintf("investigating with the LLM: step %s done %s", step, clock(elapsed)))
		case f.Investigating:
			ui.Status("investigating with the LLM " + clock(elapsed))
		default:
			ui.Status("triage and ticketing " + clock(elapsed))
		}
	}
}

// poll sends the lines that check returns, every interval, as source's events.
func poll(ctx context.Context, every time.Duration, out chan<- event, check func() []string, source string) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		for _, l := range check() {
			select {
			case out <- event{source: source, text: l}:
			case <-ctx.Done():
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// streamLogs follows a Deployment's log from since and sends its milestones.
// It reconnects when kubectl stops, and skips lines it has already sent.
func streamLogs(ctx context.Context, kubeContext, deploy, container string, since time.Time, out chan<- event) {
	source := deploy
	seen := map[string]bool{}
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, "kubectl", "--context", kubeContext, "-n", "incident-copilot", "logs", "-f",
			"deploy/"+deploy, "-c", container, "--since-time="+since.UTC().Format(time.RFC3339))
		stdout, err := cmd.StdoutPipe()
		if err == nil && cmd.Start() == nil {
			sc := bufio.NewScanner(stdout)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			for sc.Scan() {
				line := sc.Text()
				m, ok := demorun.ParseMilestone([]byte(line))
				if !ok || seen[line] {
					continue
				}
				seen[line] = true
				select {
				case out <- event{source: source, milestone: &m}:
				case <-ctx.Done():
					return
				}
			}
			_ = cmd.Wait()
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

// startPage forwards a local port to the Control plane, which serves the
// local ticket page.
func (r *runner) startPage() {
	port := 8080
	if l, err := net.Listen("tcp", "127.0.0.1:8080"); err == nil {
		_ = l.Close()
	} else if l, err := net.Listen("tcp", "127.0.0.1:0"); err == nil {
		port = l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
	}
	log, err := os.Create(filepath.Join(logDir, "port-forward.log"))
	if err != nil {
		return
	}
	// Not bound to the timeline's context: the page stays up until the end.
	cmd := exec.Command("kubectl", "--context", cpCtx, "-n", "incident-copilot", "port-forward", "svc/control-plane", fmt.Sprintf("%d:8080", port))
	cmd.Stdout, cmd.Stderr = log, log
	if cmd.Start() != nil {
		_ = log.Close()
		return
	}
	r.page, r.pageBase = cmd, fmt.Sprintf("http://localhost:%d", port)
}

func (r *runner) stopPage() {
	if r.page != nil {
		_ = r.page.Process.Kill()
		_ = r.page.Wait()
		r.page = nil
	}
}

// result prints where the ticket is, and its RCA.
func (r *runner) result(ctx context.Context, f *demorun.Follow) error {
	ui := r.ui
	ui.Status("")
	ui.Section("Result")
	url := f.URL
	if f.Adapter != "github" && r.pageBase != "" && f.TicketRef != "" && (url == "" || strings.HasPrefix(url, "/")) {
		if url == "" {
			url = "/tickets/" + f.TicketRef
		}
		url = r.pageBase + url
	}
	switch {
	case f.Ready:
		ui.OK(fmt.Sprintf("Incident %d: the RCA is in ticket %s, ready for review", f.Incident, f.TicketRef))
	case f.Decision == "join":
		ui.Warn(fmt.Sprintf("The fault joined Incident %d, which was already open, so it was not investigated again.", f.Incident))
		ui.Note("Run make demo again and answer yes to a clean Control plane state, to see a new investigation.")
	default:
		ui.Warn(fmt.Sprintf("Incident %d has ticket %s, but no RCA: there is no OpenRouter key, so nothing investigated it.", f.Incident, f.TicketRef))
		ui.Note("Add OPENROUTER_API_KEY to .env.demo (step 1 explains how), and run make demo again.")
	}
	if url != "" {
		ui.Plain("Ticket: " + url)
	}
	if !f.Ready {
		return nil
	}
	if rca := r.fetchRCA(ctx, f, url); rca != "" {
		ui.Plain("")
		for _, l := range strings.Split(rca, "\n") {
			ui.Plain("│ " + l)
		}
	} else {
		ui.Note("Open the ticket to read the RCA.")
	}
	return nil
}

// fetchRCA reads the RCA from the ticket: from the local ticket page, or
// with gh from GitHub Issues.
func (r *runner) fetchRCA(ctx context.Context, f *demorun.Follow, url string) string {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if f.Adapter == "github" {
		out, err := exec.CommandContext(ctx, "gh", "issue", "view", url, "--json", "comments", "--jq", ".comments[].body").Output()
		if err != nil {
			return ""
		}
		return demorun.RCA(string(out))
	}
	if r.pageBase == "" {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.pageBase+"/tickets/"+f.TicketRef+".md", nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return demorun.RCA(string(b))
}

// cleanup offers make clear and make down, and keeps the ticket page up
// until the user is done with it.
func (r *runner) cleanup(ctx context.Context) error {
	ui := r.ui
	ui.Section("Next")
	ui.Note("docs/demo.md explains how to read the RCA and the audit log. Try another fault: make demo F=oom.",
		"Grafana, Prometheus, and Alertmanager: run make port-forward, then open http://localhost:3000.")
	if ui.Ask("Restore the demo app's healthy baseline now (make clear)?", false) {
		if err := r.make(ctx, "clear", "restoring the demo app's baseline", "The demo app is healthy"); err != nil {
			return err
		}
	}
	if ui.Ask("Delete both clusters now (make down)? This deletes the Incidents, tickets, and audit log too.", false) {
		r.stopPage()
		return r.make(ctx, "down", "deleting both clusters", "Both clusters are deleted")
	}
	if r.page != nil {
		r.ui.Line("The ticket page at " + r.pageBase + "/tickets stays up until you press Enter. ")
	}
	return nil
}
