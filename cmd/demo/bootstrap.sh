#!/usr/bin/env bash
# make demo's bootstrap, run from the repository root: check every prerequisite
# of the demo on macOS or Linux, offer to install what is missing (only after a
# yes), check the Docker daemon and its resources, then build and start the
# Go demo runner (cmd/demo) with this script's arguments.
set -euo pipefail

# shellcheck source=../../deploy/versions.env
source deploy/versions.env

# The two clusters need at least this much of Docker; the recommended amount
# leaves room for the observability stack under load. Docker Desktop's VM
# reports a little less memory than its setting, hence 7.5 and 9.5 GiB.
MIN_CPUS=4 MIN_MEM_GIB10=75 REC_CPUS=6 REC_MEM_GIB10=95

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  B=$'\033[1m' DIM=$'\033[2m' RED=$'\033[31m' GREEN=$'\033[32m' YELLOW=$'\033[33m' R=$'\033[0m'
else
  B='' DIM='' RED='' GREEN='' YELLOW='' R=''
fi
ok()   { printf '  %s✓%s %-8s %s%s%s\n' "$GREEN" "$R" "$1" "$DIM" "$2" "$R"; }
bad()  { printf '  %s✗%s %-8s %s\n' "$RED" "$R" "$1" "$2"; }
warn() { printf '  %s!%s %s\n' "$YELLOW" "$R" "$*"; }
say()  { printf '    %s\n' "$@"; }
# ask QUESTION: true only on an explicit yes. No answer, or no terminal, is no.
ask() {
  local a=''
  printf '    %s %s[y/N]%s ' "$1" "$B" "$R"
  read -r a || true
  [ -t 0 ] || echo "$a" # a terminal echoes the answer itself
  [[ $a =~ ^[Yy]([Ee][Ss])?$ ]]
}

os=$(uname -s)
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) arch=$(uname -m) ;;
esac
case "$os" in
  Darwin | Linux) ;;
  *) echo "make demo supports macOS and Linux; this is $os."; exit 1 ;;
esac

# On Linux, tools are installed for this user only, with no sudo.
if [ "$os" = Linux ]; then
  export PATH="$HOME/.local/bin:$HOME/.local/go/bin:$PATH"
fi

github=no
if [ -f .env.demo ] && grep -qE '^IC_GITHUB_REPO=.+' .env.demo && grep -qE '^IC_GITHUB_TOKEN=.+' .env.demo; then github=yes; fi

purpose() {
  case "$1" in
    go) echo "builds incident-copilot and runs the demo runner" ;;
    docker) echo "runs the local clusters: each kind node is a Docker container" ;;
    kind) echo "creates the two clusters, ic-control-plane and ic-target" ;;
    kubectl) echo "deploys into both clusters and reads their state" ;;
    helm) echo "installs Prometheus, Alertmanager, Loki, and Grafana into ic-target" ;;
    gh) echo "reads your GitHub tickets, because .env.demo sets IC_GITHUB_REPO and IC_GITHUB_TOKEN" ;;
  esac
}

# install_cmd TOOL: the command that installs TOOL here, or nothing when it
# cannot be installed reliably from this script.
install_cmd() {
  local t=$1
  if [ "$os" = Darwin ]; then
    [ "$t" = docker ] && return
    command -v brew >/dev/null || return 0
    if command -v "$t" >/dev/null; then echo "brew upgrade $t"; else echo "brew install $t"; fi
    return
  fi
  local bin='$HOME/.local/bin'
  case "$t" in
    kind) echo "mkdir -p $bin && curl -fsSLo $bin/kind https://kind.sigs.k8s.io/dl/$KIND_VERSION/kind-linux-$arch && chmod +x $bin/kind" ;;
    kubectl) echo "mkdir -p $bin && curl -fsSLo $bin/kubectl https://dl.k8s.io/release/$KUBECTL_VERSION/bin/linux/$arch/kubectl && chmod +x $bin/kubectl" ;;
    helm) echo "mkdir -p $bin && curl -fsSL https://get.helm.sh/helm-$HELM_VERSION-linux-$arch.tar.gz | tar -xz -C $bin --strip-components=1 linux-$arch/helm" ;;
    go) echo "rm -rf \$HOME/.local/go && mkdir -p \$HOME/.local && curl -fsSL https://go.dev/dl/go$GO_VERSION.linux-$arch.tar.gz | tar -xz -C \$HOME/.local" ;;
  esac
}

manual_help() {
  case "$os:$1" in
    Darwin:docker) say "Install Docker Desktop from https://www.docker.com/products/docker-desktop/ and start it." \
      "Colima or OrbStack work too." ;;
    Linux:docker) say "Install Docker Engine for your distribution: https://docs.docker.com/engine/install/" \
      "then let your user run it: sudo usermod -aG docker \$USER, and log in again." ;;
    Linux:gh) say "Install it from your package manager: https://github.com/cli/cli/blob/trunk/docs/install_linux.md" ;;
    Darwin:*) say "Install Homebrew from https://brew.sh and re-run make demo, or install $1 yourself." ;;
  esac
}

# offer_homebrew: on macOS, Homebrew installs the other tools. Offered once.
brew_offered=no
offer_homebrew() {
  [ "$os" = Darwin ] && [ "$brew_offered" = no ] && ! command -v brew >/dev/null || return 0
  brew_offered=yes
  local cmd='/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"'
  bad brew "is missing. Homebrew installs the tools below on macOS."
  say "To install it, make demo would run Homebrew's official installer, which explains" \
    "what it does and asks for your password itself:" "  $cmd"
  ask "Install Homebrew now?" || return 0
  bash -c "$cmd" || true
  # Where Homebrew's installer puts brew. Tests point this at their sandbox,
  # so they can never reach a real Homebrew.
  local b
  for b in ${IC_DEMO_BREW_PATHS:-/opt/homebrew/bin/brew /usr/local/bin/brew}; do
    if [ -x "$b" ]; then eval "$("$b" shellenv)"; fi
  done
  hash -r
  if command -v brew >/dev/null; then
    ok brew "installed. Add Homebrew to your shell profile as its installer says."
  else
    bad brew "is still not usable after the install"
  fi
}

go_too_old() {
  local v
  v=$(go env GOVERSION 2>/dev/null | sed 's/^go//')
  ! printf '%s\n%s\n' "$GO_MIN_VERSION" "$v" | sort -V -C
}

printf '%s==> Checking prerequisites%s %s(%s, %s)%s\n' "$B" "$R" "$DIM" "$os" "$arch" "$R"
tools=(go docker kind kubectl helm)
[ "$github" = yes ] && tools+=(gh)
missing=()
installed_local=no
for t in "${tools[@]}"; do
  if command -v "$t" >/dev/null && ! { [ "$t" = go ] && go_too_old; }; then
    ok "$t" "$(purpose "$t")"
    continue
  fi
  if command -v "$t" >/dev/null; then
    bad "$t" "is older than $GO_MIN_VERSION. It $(purpose "$t")."
  else
    bad "$t" "is missing. It $(purpose "$t")."
  fi
  [ "$t" != docker ] && offer_homebrew
  cmd=$(install_cmd "$t")
  if [ -z "$cmd" ]; then
    manual_help "$t"
    missing+=("$t")
    continue
  fi
  say "To install it, make demo would run:" "  $cmd"
  if ! ask "Install $t now?"; then
    missing+=("$t")
    continue
  fi
  [ "$os" = Linux ] && installed_local=yes
  if bash -c "$cmd" && hash -r && command -v "$t" >/dev/null && ! { [ "$t" = go ] && go_too_old; }; then
    ok "$t" "installed"
  else
    bad "$t" "is still not usable after the install"
    missing+=("$t")
  fi
done
if [ "$installed_local" = yes ]; then
  warn "Installed into ~/.local. Add this line to your shell profile so your own shell finds the tools:"
  say "export PATH=\"\$HOME/.local/bin:\$HOME/.local/go/bin:\$PATH\""
fi
if [ ${#missing[@]} -gt 0 ]; then
  echo
  echo "make demo needs ${missing[*]}. Install them, then run make demo again."
  exit 1
fi

printf '%s==> Checking Docker%s\n' "$B" "$R"
if ! err=$(docker info 2>&1 >/dev/null); then
  bad docker "is installed, but its daemon is not reachable."
  case "$os" in
    Darwin) say "Start Docker Desktop (open -a Docker), wait until it says it is running," \
      "then run make demo again. With Colima: colima start --cpu $REC_CPUS --memory 10." ;;
    Linux)
      if grep -qi 'permission denied' <<<"$err"; then
        say "Your user may not use the Docker daemon. Run: sudo usermod -aG docker \$USER" \
          "then log out and in again, and run make demo again."
      else
        say "Start the Docker daemon: sudo systemctl start docker" \
          "(Docker Desktop for Linux: start it from your applications), then run make demo again."
      fi ;;
  esac
  exit 1
fi
read -r cpus mem < <(docker info --format '{{.NCPU}} {{.MemTotal}}') || true
cpus=${cpus:-0} mem=${mem:-0}
mem10=$((mem * 10 / 1073741824)) # tenths of a GiB
memtxt="$((mem10 / 10)).$((mem10 % 10)) GiB"
resources_help() {
  case "$os" in
    Darwin) say "Docker Desktop: Settings > Resources > Advanced: set CPUs to $REC_CPUS and Memory to 10 GB, then Apply & restart." \
      "Colima: colima stop && colima start --cpu $REC_CPUS --memory 10." ;;
    Linux) say "Docker Engine uses this machine's CPUs and memory directly, so the machine needs more of them." \
      "Docker Desktop for Linux: Settings > Resources > Advanced: CPUs $REC_CPUS, Memory 10 GB." ;;
  esac
}
if [ "$cpus" -lt "$MIN_CPUS" ] || [ "$mem10" -lt "$MIN_MEM_GIB10" ]; then
  bad docker "has $cpus CPUs and $memtxt. The two demo clusters need at least $MIN_CPUS CPUs and 8 GB ($REC_CPUS CPUs and 10 GB recommended)."
  resources_help
  exit 1
fi
if [ "$cpus" -lt "$REC_CPUS" ] || [ "$mem10" -lt "$REC_MEM_GIB10" ]; then
  warn "Docker has $cpus CPUs and $memtxt: enough to run, but $REC_CPUS CPUs and 10 GB are recommended."
  resources_help
else
  ok docker "has $cpus CPUs and $memtxt"
fi

mkdir -p build/demo
go build -o build/demo/runner ./cmd/demo
exec build/demo/runner "$@"
