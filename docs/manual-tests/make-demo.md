# Manual test: make demo

This test checks `make demo` from the bootstrap to a finished RCA: the prerequisite and Docker checks, the eight steps, the live timeline for the `crash-loop` and `oom` faults, a re-run on existing clusters, and the cleanup offer. It works on clusters that already exist and never deletes them. Run it whenever `cmd/demo`, `internal/demorun`, the Makefile's demo targets, or the milestone log lines change.

The install paths of the bootstrap are covered by unit tests with stubbed tools (`go test ./cmd/demo`). Do not test them by uninstalling tools.

## Before you start

- **macOS or Linux**, with the tools from [`docs/development.md`](../development.md#prerequisites), `jq`, and Docker with at least 6 CPUs and 10 GB of memory.
- **`.env.demo` at the repository root with `OPENROUTER_API_KEY`.** For local tickets, leave `IC_GITHUB_REPO` and `IC_GITHUB_TOKEN` unset. With them set, the test opens real issues in that repository.
- **A shell where `kubectl config get-contexts -o name` lists `kind-ic-control-plane` and `kind-ic-target`.** Every command here pins one of those contexts.
- **An uncommitted tree needs a new image tag each run**, or the redeploy keeps the old build: `export VERSION=dev-$(date +%s)`.

Run every command from the repository root.

## 1. The bootstrap's checks

```sh
make demo F=nope </dev/null
```

Expect one `✓` line per tool, each saying what the tool is for, and a `✓ docker has N CPUs and X GiB` line, or a `!` warning under 6 CPUs or 10 GB. Then the runner refuses the unknown fault and lists the four scenarios. No cluster is touched. `gh` is listed only when `.env.demo` sets `IC_GITHUB_REPO`.

To see the Docker checks, quit Docker Desktop (on Linux: `sudo systemctl stop docker`) and run `make demo` again. Expect `its daemon is not reachable` and how to start it on your platform, not a stack trace. Start Docker again.

## 2. crash-loop, end to end

```sh
make demo
```

Check each step:

1. `OpenRouter key found in .env.demo`, and `Tickets go to the Control plane's local ticket page`, followed by the demo-only note.
2. to 7. Each step has a header and a line on what it is for, a spinner with the elapsed time while it runs, and a `✓` line with how long it took. On existing clusters, step 2 reuses them. `build/demo/` holds one log per step.
5. If the Control plane already has state, the runner explains why a clean state is needed and asks. Answer `y`.
6. `Sensor link up: the Sensor dialed out to the Control plane`.

In step 8, the timeline shows, in this order:

- `fault rolled out to orders: "orders: pin order TTL in the manifest"`
- `kubernetes` lines for the new `orders` pod: an `Error` exit, then `CrashLoopBackOff`, and its restarts
- `alertmanager` lines: `DeploymentRolloutStuck` and `PodCrashLooping` firing for `demo/orders`
- `control-plane` lines: the Signal received, `Triage opened Incident N`, `Ticket ... created (local ticketing)`, `Investigation started`, one `step` line per LLM request and Evidence query, `Investigation finished` with a confidence, `RCA written to ticket`, and `Ticket ... is ready for review`

While waiting for the alert, the live line shows a progress bar against the scenario's `alert_within`. Then the `Result` section prints the ticket's URL and the RCA, starting with `## Root-cause analysis`.

Open the URL in a browser. The ticket page shows the same RCA. Answer `n` to both cleanup questions, and press Enter.

## 3. oom, on the same clusters

```sh
make demo F=oom
```

Step 2 says the clusters already exist, and step 7 restores the baseline left by step 2. Answer `y` to a clean Control plane state. The timeline shows `orders` pods with `last exit OOMKilled (code 137)` and rising restarts, then `ContainerOOMKilled` and `PodCrashLooping` firing, then the same `control-plane` milestones as in step 2, through `ready for review`, and the RCA.

## 4. A re-run that keeps the state

```sh
make demo
```

Answer `n` to a clean Control plane state. The timeline shows `Triage joined the Signal to open Incident N`, and no investigation. About 90 seconds later, the `Result` section explains that the fault joined an open Incident, and how to get a new investigation.

## 5. Without an OpenRouter key

Move the key aside and run once:

```sh
cp .env.demo .env.demo.bak && sed -i.tmp '/^OPENROUTER_API_KEY=/d' .env.demo && rm -f .env.demo.tmp
make demo
```

Step 1 explains how to create a free OpenRouter account and key, and where to put it. Press Enter instead of pasting a key. Expect the warning that there will be no RCA. Answer `y` to a clean state. The timeline ends after the ticket is created, and the `Result` section explains why there is no RCA. Put the key back: `mv .env.demo.bak .env.demo`. Then redeploy the Control plane with it: `make control-plane`.

## Clean up

```sh
make clear
```

Do not run `make down` on shared clusters.
