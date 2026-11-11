package demorun

import "testing"

func observeAll(t *testing.T, f *Follow, lines ...string) []bool {
	t.Helper()
	var shown []bool
	for _, l := range lines {
		m, ok := ParseMilestone([]byte(l))
		if !ok {
			t.Fatalf("not a milestone: %s", l)
		}
		shown = append(shown, f.Observe(m))
	}
	return shown
}

func TestFollowTracksTheTargetsIncidentToReady(t *testing.T) {
	f := &Follow{Target: "orders"}
	shown := observeAll(t, f,
		`{"msg":"signal.received","incident":2,"name":"Other","target":"ic-target/demo/Deployment/inventory"}`,
		`{"msg":"signal.received","incident":5,"name":"PodCrashLooping","target":"ic-target/demo/Deployment/orders"}`,
		`{"msg":"triage.decision","incident":5,"decision":"open"}`,
		`{"msg":"triage.decision","incident":2,"decision":"join"}`,
		`{"msg":"ticket.created","incident":5,"adapter":"local","ref":"9"}`,
		`{"msg":"investigation.started","incident":5,"model":"openrouter/free"}`,
		`{"msg":"rca.posted","incident":5,"ref":"9"}`,
		`{"msg":"ticket.ready","incident":5,"ref":"9","url":"/tickets/9"}`,
	)
	want := []bool{false, true, true, false, true, true, true, true}
	for i := range want {
		if shown[i] != want[i] {
			t.Errorf("line %d shown=%v, want %v", i, shown[i], want[i])
		}
	}
	if f.Incident != 5 || f.Decision != "open" || f.TicketRef != "9" || f.Adapter != "local" || !f.Investigating || !f.Ready || f.URL != "/tickets/9" {
		t.Errorf("follow = %+v", *f)
	}
}

func TestFollowLocksOnTheTriageDecisionAfterAnUnnumberedSignal(t *testing.T) {
	f := &Follow{Target: "orders"}
	observeAll(t, f,
		`{"msg":"signal.received","name":"CrashLoopBackOff","source":"sensor/ic-target","target":"ic-target/demo/Deployment/orders"}`,
		`{"msg":"triage.decision","incident":7,"decision":"join"}`,
	)
	if f.Incident != 7 || f.Decision != "join" {
		t.Errorf("follow = %+v", *f)
	}
}

func TestFollowShowsSensorLinkMilestones(t *testing.T) {
	f := &Follow{Target: "orders"}
	if shown := observeAll(t, f, `{"msg":"link.down","error":"EOF"}`); !shown[0] {
		t.Error("link.down not shown")
	}
}

func TestFollowDoesNotLockOnADecisionAfterAnotherTargetsSignal(t *testing.T) {
	f := &Follow{Target: "orders"}
	observeAll(t, f,
		`{"msg":"signal.received","name":"CrashLoopBackOff","target":"ic-target/demo/Deployment/orders"}`,
		`{"msg":"signal.received","name":"Other","target":"ic-target/demo/Deployment/inventory"}`,
		`{"msg":"triage.decision","incident":2,"decision":"join"}`,
	)
	if f.Incident != 0 {
		t.Errorf("locked on another Target's Incident: %+v", *f)
	}
	observeAll(t, f,
		`{"msg":"signal.received","name":"PodCrashLooping","target":"ic-target/demo/Deployment/orders"}`,
		`{"msg":"triage.decision","incident":5,"decision":"open"}`,
	)
	if f.Incident != 5 {
		t.Errorf("follow = %+v", *f)
	}
}

func TestFollowRecordsTheInvestigationsEnd(t *testing.T) {
	for _, c := range []struct {
		line      string
		exhausted bool
	}{
		{`{"msg":"investigation.finished","incident":5,"confidence":"low"}`, false},
		{`{"msg":"investigation.exhausted","incident":5,"reason":"steps"}`, true},
	} {
		f := &Follow{Target: "orders"}
		observeAll(t, f,
			`{"msg":"signal.received","incident":5,"target":"ic-target/demo/Deployment/orders"}`,
			`{"msg":"investigation.started","incident":5}`,
			c.line,
		)
		if !f.Finished || f.Exhausted != c.exhausted || f.Ready {
			t.Errorf("%s: follow = %+v", c.line, *f)
		}
	}
}
