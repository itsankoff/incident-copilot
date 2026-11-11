package demorun

import "strings"

// Follow tracks the Incident that the fault's Signals open or join: the first
// Signal about the Target Deployment picks it.
type Follow struct {
	Target string // the Deployment the fault rolled out to

	Incident      int64
	Decision      string // Triage's first decision about it: open, join, or reopen
	TicketRef     string
	Adapter       string // the ticketing adapter: local, github, ...
	Investigating bool
	Finished      bool   // the investigation ended, finished or exhausted
	Exhausted     bool   // it ran out of budget
	Stopped       string // why the investigation was cancelled or failed
	Posted        bool   // the RCA is in the ticket
	Advising      bool   // the remediation loop is drafting Advice
	Ready         bool   // the RCA and the Advice are in the ticket, ready for review
	URL           string // the ticket's URL, or a path on the Control plane

	// pending: a Signal about Target arrived without its Incident id, so the
	// next triage.decision is about it, unless another Signal came between.
	pending bool
}

// Observe records m and reports whether it is worth showing: a milestone of
// the followed Incident, or of the Sensor link.
func (f *Follow) Observe(m Milestone) bool {
	switch m.Msg {
	case "link.up", "link.down":
		return true
	case "signal.received":
		about := strings.HasSuffix(m.Str("target"), "/"+f.Target)
		if f.Incident == 0 {
			f.pending = about && m.Incident == 0
			if about {
				f.Incident = m.Incident
				return true
			}
		}
	case "triage.decision":
		if f.pending && m.Incident != 0 {
			f.Incident, f.pending = m.Incident, false
		}
	}
	if f.Incident == 0 || m.Incident != f.Incident {
		return false
	}
	switch m.Msg {
	case "triage.decision":
		if f.Decision == "" {
			f.Decision = m.Str("decision")
		}
	case "ticket.created":
		f.TicketRef, f.Adapter = m.Str("ref"), m.Str("adapter")
	case "ticket.commented":
		if f.TicketRef == "" {
			f.TicketRef, f.Adapter = m.Str("ref"), m.Str("adapter")
		}
	case "investigation.started":
		f.Investigating = true
	case "investigation.finished":
		f.Finished = true
	case "investigation.exhausted":
		f.Finished, f.Exhausted = true, true
	case "investigation.cancelled":
		f.Stopped = m.Str("reason")
	case "investigation.failed":
		f.Stopped = m.Str("error")
	case "rca.posted":
		f.Posted = true
	case "remediation.started":
		f.Advising = true
	case "ticket.ready":
		f.Ready = true
		f.TicketRef = m.Str("ref")
		f.URL = m.Str("url")
	}
	return true
}

// Joined reports whether the fault's Signal joined an Incident that already
// existed, open or resolved, which is not investigated again.
func (f *Follow) Joined() bool {
	return f.Decision == "join" || f.Decision == "reopen"
}
