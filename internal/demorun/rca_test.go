package demorun

import "testing"

func TestRCAPicksTheRCASectionOfATicket(t *testing.T) {
	ticket := "# PodCrashLooping on ic-target/demo/Deployment/orders\n\nSignal: PodCrashLooping\n\n---\n\n## Root-cause analysis\n\n### Summary\n\nA rollout broke orders.\n\n### Confidence\n\nmedium\n\n---\n\n## Advice\n\nrollback_deployment\n"
	want := "## Root-cause analysis\n\n### Summary\n\nA rollout broke orders.\n\n### Confidence\n\nmedium"
	if got := RCA(ticket); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got := RCA("# no RCA here\n"); got != "" {
		t.Errorf("no RCA: got %q", got)
	}
}
