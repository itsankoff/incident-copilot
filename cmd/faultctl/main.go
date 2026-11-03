// Command faultctl injects demo scenarios into ic-target and clears them.
//
//	faultctl list               scenarios and their ground truth
//	faultctl inject <scenario>  roll out the scenario's change to the demo
//	faultctl status             what is armed, its rollout, and the demo's firing alerts
//	faultctl clear              restore the baseline and wait until healthy
//	faultctl leakcheck          fail if the demo's evidence gives the armed scenario away
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/itsankoff/incident-copilot/internal/fault"
)

func main() {
	kubeContext := flag.String("context", "kind-ic-target", "kubectl context of the Target cluster")
	timeout := flag.Duration("timeout", 5*time.Minute, "how long a command may take; clear waits this long for the demo to be healthy")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: faultctl [flags] list | inject <scenario> | status | clear | leakcheck")
		flag.PrintDefaults()
	}
	flag.Parse()

	d := fault.Demo{
		Kubectl:  fault.KubectlFor(*kubeContext),
		Baseline: "build/deploy", // the overlay make deploy applied
		Dir:      "build/fault",
		Commit:   syntheticCommit,
		Poll:     5 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if err := run(ctx, d, "eval/scenarios", flag.Args()); err != nil {
		cancel()
		fmt.Fprintln(os.Stderr, "faultctl:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, d fault.Demo, dir string, args []string) error {
	if len(args) == 0 {
		flag.Usage()
		return errors.New("missing command")
	}
	all, err := fault.LoadScenarios(dir)
	if err != nil {
		return err
	}
	find := func(name string) (fault.Scenario, error) {
		for _, s := range all {
			if s.Name == name {
				return s, nil
			}
		}
		return fault.Scenario{}, fmt.Errorf("unknown scenario %q (see faultctl list)", name)
	}

	switch cmd := args[0]; {
	case cmd == "list":
		for _, s := range all {
			printScenario(s)
		}
		return nil
	case cmd == "inject" && len(args) == 2:
		s, err := find(args[1])
		if err != nil {
			return err
		}
		if err := d.Inject(ctx, s); err != nil {
			return err
		}
		fmt.Printf("injected %s: expect %s within %s\n", s.Name, strings.Join(s.Expect.Alerts, ", "), s.Expect.AlertWithin)
		return nil
	case cmd == "status":
		return status(ctx, d, find)
	case cmd == "clear":
		if err := d.Clear(ctx); err != nil {
			return err
		}
		fmt.Println("baseline restored: all demo pods Ready, no alerts firing")
		return nil
	case cmd == "leakcheck":
		name := d.Armed()
		if name == "" {
			return errors.New("nothing armed: run faultctl inject <scenario> first")
		}
		leaks, err := d.LeakCheck(ctx, name)
		if err != nil {
			return err
		}
		if len(leaks) > 0 {
			return fmt.Errorf("%s leaks:\n  %s", name, strings.Join(leaks, "\n  "))
		}
		fmt.Printf("no leaks for %s\n", name)
		return nil
	default:
		flag.Usage()
		return fmt.Errorf("unknown command %q", strings.Join(args, " "))
	}
}

func printScenario(s fault.Scenario) {
	fmt.Printf("%s (%s on %s: %s)\n", s.Name, s.Kind, s.Inject.Target, s.Inject.ChangeCause)
	fmt.Printf("  alerts:  %s within %s\n", strings.Join(s.Expect.Alerts, ", "), s.Expect.AlertWithin)
	fmt.Printf("  cause:   %s\n", s.Expect.Cause)
	for _, f := range s.Expect.CauseFacts {
		fmt.Printf("    - %s\n", f)
	}
	fmt.Printf("  actions: %s\n", strings.Join(s.Expect.Actions, ", "))
	fmt.Printf("  unsafe:  %s\n", strings.Join(s.Expect.UnsafeActions, ", "))
}

func status(ctx context.Context, d fault.Demo, find func(string) (fault.Scenario, error)) error {
	target := "orders"
	if name := d.Armed(); name == "" {
		fmt.Println("armed:   none")
	} else {
		fmt.Println("armed:  ", name)
		if s, err := find(name); err == nil {
			target = s.Inject.Target
		}
	}
	// kubectl exits non-zero for a rollout past its deadline, which is a state to report.
	out, err := d.Kubectl(ctx, "-n", fault.Namespace, "rollout", "status", "deployment/"+target, "--watch=false")
	if err != nil {
		fmt.Println("rollout:", err)
	} else {
		fmt.Print("rollout: ", string(out))
	}
	alerts, err := d.FiringAlerts(ctx)
	if err != nil {
		return err
	}
	if len(alerts) == 0 {
		fmt.Println("alerts:  none firing")
	}
	for _, a := range alerts {
		fmt.Println("alert:  ", a)
	}
	return nil
}

// syntheticCommit stands in for the Git SHA of the injected change (--git=none).
func syntheticCommit() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:7]
}
