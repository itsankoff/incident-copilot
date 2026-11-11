package demorun

import "testing"

// Sample lines in the milestone log contract shared with #89: the Control
// plane's JSON slog, msg is the audit event type, and incident is the Incident id.
func TestMilestoneRendersEachContractLine(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{
			`{"time":"2026-11-11T10:00:01Z","level":"INFO","msg":"signal.received","role":"control-plane","incident":3,"source":"ic-target-alertmanager","name":"PodCrashLooping","target":"kubernetes/ic-target/demo/Deployment/orders"}`,
			"Control plane received Signal PodCrashLooping from ic-target-alertmanager about kubernetes/ic-target/demo/Deployment/orders",
		},
		{
			`{"time":"2026-11-11T10:00:01Z","level":"INFO","msg":"triage.decision","incident":3,"decision":"open"}`,
			"Triage opened Incident 3",
		},
		{
			`{"time":"2026-11-11T10:00:01Z","level":"INFO","msg":"triage.decision","incident":3,"decision":"join"}`,
			"Triage joined the Signal to open Incident 3",
		},
		{
			`{"time":"2026-11-11T10:00:02Z","level":"INFO","msg":"ticket.created","incident":3,"adapter":"local","ref":"7"}`,
			"Ticket 7 created (local ticketing)",
		},
		{
			`{"time":"2026-11-11T10:00:02Z","level":"INFO","msg":"ticket.commented","incident":3,"adapter":"github","ref":"46","kind":"signal"}`,
			"Ticket 46 commented: signal (github ticketing)",
		},
		{
			`{"time":"2026-11-11T10:00:02Z","level":"INFO","msg":"investigation.started","incident":3,"model":"openrouter/free","budget":{"steps":8,"tokens":100000,"time":"5m0s"}}`,
			"Investigation started with openrouter/free (budget: 8 steps, 100000 tokens, 5m0s)",
		},
		{
			`{"time":"2026-11-11T10:00:09Z","level":"INFO","msg":"investigation.step","incident":3,"step":1,"llm":"nvidia/nemotron-3.5-lightning:free","ok":true}`,
			"step 1: LLM nvidia/nemotron-3.5-lightning:free answered",
		},
		{
			`{"time":"2026-11-11T10:00:10Z","level":"INFO","msg":"investigation.step","incident":3,"step":2,"tool":"workload_state","ok":true}`,
			"step 2: Evidence query workload_state ok",
		},
		{
			`{"time":"2026-11-11T10:00:11Z","level":"INFO","msg":"investigation.step","incident":3,"step":3,"tool":"events","ok":false}`,
			"step 3: Evidence query events failed",
		},
		{
			`{"time":"2026-11-11T13:10:58Z","level":"INFO","msg":"investigation.step","incident":3,"step":5,"tool":"submit_rca","ok":true}`,
			"step 5: the LLM submitted its RCA",
		},
		{
			`{"time":"2026-11-11T10:00:40Z","level":"INFO","msg":"investigation.finished","incident":3,"confidence":"medium","steps":4,"duration":"38.5s"}`,
			"Investigation finished: confidence medium after 4 steps in 38.5s",
		},
		{
			`{"time":"2026-11-11T10:00:40Z","level":"INFO","msg":"investigation.finished","incident":3,"confidence":"low","steps":2,"duration":12300000000}`,
			"Investigation finished: confidence low after 2 steps in 12.3s",
		},
		{
			`{"time":"2026-11-11T10:00:09Z","level":"INFO","msg":"investigation.step","incident":3,"step":4,"llm":true,"ok":false}`,
			"step 4: LLM request failed",
		},
		{
			`{"time":"2026-11-11T10:05:02Z","level":"INFO","msg":"investigation.exhausted","incident":3,"confidence":"low","steps":8,"duration":"4m58.1s","budget":"steps"}`,
			"Investigation ran out of its steps budget: confidence low after 8 steps in 4m58.1s",
		},
		{
			`{"time":"2026-11-11T10:00:01Z","level":"INFO","msg":"triage.decision","incident":3,"decision":"reopen","reason":"a Signal for resolved Incident 3"}`,
			"Triage reopened resolved Incident 3 (a Signal for resolved Incident 3)",
		},
		{
			`{"time":"2026-11-11T10:09:01Z","level":"INFO","msg":"triage.decision","incident":3,"decision":"resolve"}`,
			"Triage resolved Incident 3: its last Signal ended",
		},
		{
			`{"time":"2026-11-11T10:09:01Z","level":"INFO","msg":"ticket.closed","incident":3,"adapter":"local","ref":"7"}`,
			"Ticket 7 closed (local ticketing)",
		},
		{
			`{"time":"2026-11-11T10:00:02Z","level":"INFO","msg":"investigation.skipped","incident":3,"reason":"the Incident has no Target"}`,
			"Investigation skipped: the Incident has no Target",
		},
		{
			`{"time":"2026-11-11T10:00:20Z","level":"INFO","msg":"investigation.cancelled","incident":3,"reason":"incident 3 closed: idle","steps":2}`,
			"Investigation cancelled after 2 steps: incident 3 closed: idle",
		},
		{
			`{"time":"2026-11-11T10:00:20Z","level":"ERROR","msg":"investigation.failed","incident":3,"error":"comment on ticket 7: 502"}`,
			"Investigation failed: comment on ticket 7: 502",
		},
		{
			`{"time":"2026-11-11T10:00:42Z","level":"INFO","msg":"remediation.started","incident":3,"model":"openrouter/free"}`,
			"Remediation started with openrouter/free: drafting Advice from the RCA",
		},
		{
			`{"time":"2026-11-11T10:00:45Z","level":"INFO","msg":"remediation.step","incident":3,"step":2,"tool":"submit_advice","ok":true}`,
			"step 2: the LLM submitted its Advice",
		},
		{
			`{"time":"2026-11-11T10:00:44Z","level":"INFO","msg":"remediation.step","incident":3,"step":1,"llm":"nvidia/nemotron-3.5-lightning:free","ok":true}`,
			"step 1: LLM nvidia/nemotron-3.5-lightning:free answered",
		},
		{
			`{"time":"2026-11-11T10:00:46Z","level":"INFO","msg":"remediation.finished","incident":3,"submitted":true,"steps":2}`,
			"Remediation finished: Advice drafted after 2 steps",
		},
		{
			`{"time":"2026-11-11T10:00:46Z","level":"INFO","msg":"remediation.finished","incident":3,"submitted":false,"steps":8}`,
			"Remediation stopped after 8 steps without Advice",
		},
		{
			`{"time":"2026-11-11T10:00:42Z","level":"INFO","msg":"remediation.skipped","incident":3,"reason":"no Executor is linked for Location ic-target"}`,
			"No Advice drafted: no Executor is linked for Location ic-target",
		},
		{
			`{"time":"2026-11-11T10:00:47Z","level":"INFO","msg":"advice.posted","incident":3,"ref":"7","level":"L1"}`,
			"Advice written to ticket 7, at autonomy level L1",
		},
		{
			`{"time":"2026-11-11T10:00:41Z","level":"INFO","msg":"rca.posted","incident":3,"ref":"7"}`,
			"RCA written to ticket 7",
		},
		{
			`{"time":"2026-11-11T10:00:41Z","level":"INFO","msg":"ticket.ready","incident":3,"ref":"7","url":"/tickets/7"}`,
			"Ticket 7 is ready for review",
		},
		{
			`{"time":"2026-11-11T10:00:00Z","level":"INFO","msg":"link.up","role":"sensor","location":"ic-target"}`,
			"Sensor link up: the Sensor dialed out to the Control plane",
		},
		{
			`{"time":"2026-11-11T10:00:00Z","level":"WARN","msg":"link.down","role":"sensor","error":"EOF"}`,
			"Sensor link down (EOF)",
		},
	}
	for _, c := range cases {
		m, ok := ParseMilestone([]byte(c.line))
		if !ok {
			t.Errorf("not a milestone: %s", c.line)
			continue
		}
		if got := m.String(); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", m.Msg, got, c.want)
		}
	}
}

func TestMilestoneIgnoresOtherLines(t *testing.T) {
	for _, line := range []string{
		`{"time":"2026-11-11T10:00:00Z","level":"INFO","msg":"control plane listening","addr":":8080"}`,
		`not json at all`,
		``,
	} {
		if m, ok := ParseMilestone([]byte(line)); ok {
			t.Errorf("%q parsed as milestone %+v", line, m)
		}
	}
}

func TestMilestoneKeepsTheIncident(t *testing.T) {
	m, ok := ParseMilestone([]byte(`{"time":"2026-11-11T10:00:01.5Z","level":"INFO","msg":"triage.decision","incident":12,"decision":"open"}`))
	if !ok {
		t.Fatal("not a milestone")
	}
	if m.Incident != 12 {
		t.Errorf("incident %d", m.Incident)
	}
}
