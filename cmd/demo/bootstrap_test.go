package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// sandbox runs bootstrap.sh with a PATH of stub tools only, so nothing real is
// installed, started, or checked.
type sandbox struct {
	t    *testing.T
	root string // the working directory: a stand-in repository root
	bin  string // the only directory on PATH
	home string
	log  string // every stubbed install and runner call, one per line
	env  []string
}

// forbidden are the tools a bootstrap test must never run for real.
var forbidden = []string{"brew", "apt", "apt-get", "dnf", "yum", "zypper", "pacman", "apk", "snap", "port",
	"sudo", "curl", "wget", "softwareupdate", "installer", "pip", "pip3", "npm"}

const stubPrefix = "#!/bin/sh\necho \"$(basename \"$0\") $*\" >> \"$STUB_LOG\"\n"

func newSandbox(t *testing.T, os_ string) *sandbox {
	t.Helper()
	dir := t.TempDir()
	s := &sandbox{t: t, root: filepath.Join(dir, "repo"), bin: filepath.Join(dir, "bin"), home: filepath.Join(dir, "home"), log: filepath.Join(dir, "calls.log")}
	for _, d := range []string{s.root, s.bin, s.home, filepath.Join(s.root, "deploy")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	versions, err := os.ReadFile("../../deploy/versions.env")
	if err != nil {
		t.Fatal(err)
	}
	s.write(filepath.Join(s.root, "deploy/versions.env"), string(versions))
	// The system tools the script itself uses.
	for _, tool := range []string{"bash", "sed", "sort", "grep", "mkdir", "chmod", "rm", "tar", "basename", "cat"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		if err := os.Symlink(p, filepath.Join(s.bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	// Every package manager and installer is a stub that marks the run as
	// forbidden; a test that means one to run replaces its stub.
	for _, tool := range forbidden {
		s.stub(tool, `echo "FORBIDDEN $0" >> "$STUB_LOG"; exit 1`)
	}
	s.stub("uname", `[ "$1" = -m ] && echo x86_64 || echo `+os_)
	for _, tool := range []string{"kind", "kubectl", "helm"} {
		s.stub(tool, "")
	}
	// The stubbed go build makes a runner that only logs its arguments.
	s.stub("go", `[ "$1" = env ] && echo go1.25.5
[ "$1" = build ] && mkdir -p "${3%/*}" && printf '#!/bin/sh\necho "runner $*" >> "$STUB_LOG"\n' > "$3" && chmod +x "$3"
exit 0`)
	s.docker("ok", 8, 104)
	// IC_DEMO_BREW_PATHS keeps the bootstrap away from this machine's Homebrew.
	s.env = []string{"PATH=" + s.bin, "HOME=" + s.home, "STUB_LOG=" + s.log, "STUB_BIN=" + s.bin,
		"IC_DEMO_BREW_PATHS=" + filepath.Join(dir, "no-such-brew")}
	return s
}

func (s *sandbox) write(path, content string) {
	s.t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		s.t.Fatal(err)
	}
}

// stub makes a tool that logs its call and then runs body.
func (s *sandbox) stub(tool, body string) { s.write(filepath.Join(s.bin, tool), stubPrefix+body+"\n") }

func (s *sandbox) remove(tool string) { _ = os.Remove(filepath.Join(s.bin, tool)) }

// docker stubs a daemon that is ok, down, or denied, with cpus and tenths of
// a GiB of memory.
func (s *sandbox) docker(state string, cpus int, gib10 int64) {
	mem := (gib10*(1<<30) + 9) / 10
	switch state {
	case "ok":
		s.stub("docker", `[ "$2" = --format ] && echo "`+strconv.Itoa(cpus)+` `+strconv.FormatInt(mem, 10)+`"; exit 0`)
	case "down":
		s.stub("docker", `echo "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?" >&2; exit 1`)
	case "denied":
		s.stub("docker", `echo "permission denied while trying to connect to the Docker daemon socket" >&2; exit 1`)
	}
}

// run runs the bootstrap with stdin as the user's answers.
func (s *sandbox) run(stdin string, args ...string) (out string, calls []string, err error) {
	s.t.Helper()
	script, _ := filepath.Abs("bootstrap.sh")
	bash, _ := exec.LookPath("bash")
	cmd := exec.Command(bash, append([]string{script}, args...)...)
	cmd.Dir = s.root
	cmd.Env = s.env
	cmd.Stdin = strings.NewReader(stdin)
	b, err := cmd.CombinedOutput()
	logged, _ := os.ReadFile(s.log)
	if strings.Contains(string(logged), "FORBIDDEN") {
		s.t.Errorf("the bootstrap ran a tool it must not:\n%s", logged)
	}
	for _, l := range strings.Split(strings.TrimSpace(string(logged)), "\n") {
		// Checks (command -v, version and docker info) are not installs.
		if strings.HasPrefix(l, "uname") || strings.HasPrefix(l, "go env") || strings.HasPrefix(l, "docker info") || l == "" {
			continue
		}
		calls = append(calls, l)
	}
	return string(b), calls, err
}

func wantContains(t *testing.T, out string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(out, sub) {
			t.Errorf("output lacks %q:\n%s", sub, out)
		}
	}
}

func TestBootstrapStartsTheRunnerWhenEverythingIsThere(t *testing.T) {
	s := newSandbox(t, "Darwin")
	out, calls, err := s.run("", "-fault", "oom")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantContains(t, out, "creates the two clusters, ic-control-plane and ic-target", "has 8 CPUs and 10.4 GiB")
	if strings.Contains(out, "gh") {
		t.Errorf("gh checked without GitHub ticketing:\n%s", out)
	}
	if want := []string{"go build -o build/demo/runner ./cmd/demo", "runner -fault oom"}; strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %q, want %q", calls, want)
	}
}

func TestBootstrapChecksGhOnlyForGitHubTicketing(t *testing.T) {
	s := newSandbox(t, "Darwin")
	s.write(filepath.Join(s.root, ".env.demo"), "IC_GITHUB_REPO=me/tickets\nIC_GITHUB_TOKEN=x\n")
	s.stub("brew", "")
	out, calls, err := s.run("\n")
	if err == nil {
		t.Fatalf("ran without gh:\n%s", out)
	}
	wantContains(t, out, "gh", "reads your GitHub tickets", "brew install gh", "make demo needs gh")
	if len(calls) != 0 {
		t.Errorf("calls without a yes: %q", calls)
	}
}

func TestBootstrapInstallsWithBrewOnlyAfterYes(t *testing.T) {
	s := newSandbox(t, "Darwin")
	s.remove("kind")
	// The stubbed brew "installs" the tool by creating it.
	s.stub("brew", `printf '#!/bin/sh\n' > "$STUB_BIN/$2"; chmod +x "$STUB_BIN/$2"`)

	out, calls, err := s.run("n\n")
	if err == nil {
		t.Fatalf("ran without kind:\n%s", out)
	}
	wantContains(t, out, "kind", "is missing", "brew install kind", "make demo needs kind")
	if len(calls) != 0 {
		t.Errorf("installed on no: %q", calls)
	}

	out, calls, err = s.run("y\n")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantContains(t, out, "installed")
	if len(calls) != 3 || calls[0] != "brew install kind" || calls[2] != "runner" {
		t.Errorf("calls = %q", calls)
	}
}

func TestBootstrapWithoutHomebrewExplainsInstead(t *testing.T) {
	s := newSandbox(t, "Darwin")
	s.remove("helm")
	s.remove("brew")
	out, _, err := s.run("n\n")
	if err == nil {
		t.Fatalf("ran without helm:\n%s", out)
	}
	wantContains(t, out, "Install Homebrew from https://brew.sh")
}

func TestBootstrapInstallsOnLinuxIntoHomeWithoutSudo(t *testing.T) {
	s := newSandbox(t, "Linux")
	s.remove("kind")
	// The stubbed curl writes an executable to its -o file.
	s.stub("curl", `while [ $# -gt 0 ]; do case "$1" in -*o) o=$2; shift;; esac; shift; done; printf '#!/bin/sh\n' > "$o"; chmod +x "$o"`)
	out, calls, err := s.run("y\n")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantContains(t, out, "$HOME/.local/bin/kind https://kind.sigs.k8s.io/dl/v0.31.0/kind-linux-amd64", "Add this line to your shell profile")
	if strings.Contains(out, "sudo") {
		t.Errorf("sudo in output:\n%s", out)
	}
	if len(calls) != 3 || !strings.HasPrefix(calls[0], "curl -fsSLo "+s.home+"/.local/bin/kind") {
		t.Errorf("calls = %q", calls)
	}
}

func TestBootstrapNeverInstallsDocker(t *testing.T) {
	for _, c := range []struct{ os, want string }{
		{"Darwin", "Install Docker Desktop from https://www.docker.com/products/docker-desktop/"},
		{"Linux", "https://docs.docker.com/engine/install/"},
	} {
		s := newSandbox(t, c.os)
		s.remove("docker")
		s.stub("brew", "")
		out, calls, err := s.run("y\n")
		if err == nil {
			t.Fatalf("%s: ran without docker:\n%s", c.os, out)
		}
		wantContains(t, out, c.want, "make demo needs docker")
		if len(calls) != 0 {
			t.Errorf("%s: calls = %q", c.os, calls)
		}
	}
}

func TestBootstrapExplainsAnUnreachableDockerDaemon(t *testing.T) {
	for _, c := range []struct{ os, state, want string }{
		{"Darwin", "down", "Start Docker Desktop (open -a Docker)"},
		{"Linux", "down", "sudo systemctl start docker"},
		{"Linux", "denied", "sudo usermod -aG docker $USER"},
	} {
		s := newSandbox(t, c.os)
		s.docker(c.state, 0, 0)
		out, calls, err := s.run("")
		if err == nil {
			t.Fatalf("%s %s: ran:\n%s", c.os, c.state, out)
		}
		wantContains(t, out, "its daemon is not reachable", c.want)
		if strings.Contains(out, "Cannot connect") || len(calls) != 0 {
			t.Errorf("%s %s: raw error or calls %q:\n%s", c.os, c.state, calls, out)
		}
	}
}

func TestBootstrapChecksDockerResources(t *testing.T) {
	for _, c := range []struct {
		os      string
		cpus    int
		gib10   int64
		fails   bool
		wantOut string
	}{
		{"Darwin", 2, 38, true, "Docker Desktop: Settings > Resources > Advanced: set CPUs to 6 and Memory to 10 GB"},
		{"Linux", 8, 70, true, "Docker Engine uses this machine's CPUs and memory directly"},
		{"Darwin", 8, 79, false, "enough to run, but 6 CPUs and 10 GB are recommended"},
		{"Darwin", 8, 97, false, "has 8 CPUs and 9.7 GiB"},
	} {
		s := newSandbox(t, c.os)
		s.docker("ok", c.cpus, c.gib10)
		out, calls, err := s.run("")
		if (err != nil) != c.fails {
			t.Fatalf("%s %d/%d: err %v:\n%s", c.os, c.cpus, c.gib10, err, out)
		}
		wantContains(t, out, c.wantOut)
		if ran := len(calls) == 2; ran == c.fails {
			t.Errorf("%s %d/%d: calls %q", c.os, c.cpus, c.gib10, calls)
		}
	}
}

func TestBootstrapOffersHomebrewOnAFreshMac(t *testing.T) {
	s := newSandbox(t, "Darwin")
	s.remove("kind")
	s.remove("brew")
	// The stubbed Homebrew installer script creates a brew that "installs" by
	// creating the tool.
	s.stub("curl", `cat <<'SCRIPT'
printf '#!/bin/sh\necho "brew $*" >> "$STUB_LOG"\nprintf "#!/bin/sh\\n" > "$STUB_BIN/$2"; chmod +x "$STUB_BIN/$2"\n' > "$STUB_BIN/brew"; chmod +x "$STUB_BIN/brew"
SCRIPT`)

	out, calls, err := s.run("n\n")
	if err == nil || len(calls) != 0 {
		t.Fatalf("err %v calls %q:\n%s", err, calls, out)
	}
	wantContains(t, out, "Homebrew installs the tools below", "raw.githubusercontent.com/Homebrew/install", "make demo needs kind")

	out, calls, err = s.run("y\ny\n")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(calls) != 4 || !strings.HasPrefix(calls[0], "curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh") || calls[1] != "brew install kind" {
		t.Errorf("calls = %q", calls)
	}
}

func TestBootstrapUpgradesAnOldGoWithBrew(t *testing.T) {
	s := newSandbox(t, "Darwin")
	s.stub("go", `[ "$1" = env ] && echo go1.22.1; exit 0`)
	s.stub("brew", "")
	out, _, err := s.run("")
	if err == nil {
		t.Fatalf("ran with an old go:\n%s", out)
	}
	wantContains(t, out, "is older than 1.25", "brew upgrade go")
}

func TestBootstrapNeedsGhOnlyWithBothGitHubSettings(t *testing.T) {
	s := newSandbox(t, "Darwin")
	s.write(filepath.Join(s.root, ".env.demo"), "IC_GITHUB_REPO=me/tickets\n")
	if out, _, err := s.run(""); err != nil {
		t.Fatalf("gh required without IC_GITHUB_TOKEN: %v\n%s", err, out)
	}
}

func TestBootstrapPinsKubectlOnLinux(t *testing.T) {
	s := newSandbox(t, "Linux")
	s.remove("kubectl")
	out, _, _ := s.run("")
	wantContains(t, out, "https://dl.k8s.io/release/v1.35.0/bin/linux/amd64/kubectl")
}

// The sandbox holds only because the bootstrap finds every tool through PATH.
// An absolute path to a tool would reach this machine's real one.
func TestBootstrapNamesNoToolByAbsolutePath(t *testing.T) {
	b, err := os.ReadFile("bootstrap.sh")
	if err != nil {
		t.Fatal(err)
	}
	abs := regexp.MustCompile(`(?:^|[^\w.:/~$}-])(/(?:bin|sbin|usr|opt|etc|var|private|Library|Applications|System)(?:/[\w.+-]+)*)`)
	allowed := map[string]bool{"/usr/bin/env": true, "/bin/bash": true, "/opt/homebrew/bin/brew": true, "/usr/local/bin/brew": true}
	for i, line := range strings.Split(string(b), "\n") {
		for _, m := range abs.FindAllStringSubmatch(line, -1) {
			if !allowed[m[1]] {
				t.Errorf("line %d names %s", i+1, m[1])
			}
			// Homebrew's own paths only as the default the tests override.
			if strings.HasSuffix(m[1], "/brew") && !strings.Contains(line, "${IC_DEMO_BREW_PATHS:-") {
				t.Errorf("line %d names %s outside IC_DEMO_BREW_PATHS", i+1, m[1])
			}
		}
	}
}
