package demorun

import "strings"

// Follow tracks the Incident that the fault's Signals open or join: the first
// Signal about the Target Deployment picks it.
type Follow struct {
	Target string // the Deployment the fault rolled out to

	Incident      int64
	Decision      string // open or join
	TicketRef     string
	Adapter       string // the ticketing adapter: local, github, ...
	Investigating bool
	Ready         bool   // the RCA is in the ticket and it is ready for review
	URL           string // the ticket's URL, or a path on the Control plane

	pending bool // a Signal about Target arrived before its Incident id
}

// Observe records m and reports whether it is worth showing: a milestone of
// the followed Incident, or of the Sensor link.
func (f *Follow) Observe(m Milestone) bool {
	switch m.Msg {
	case "link.up", "link.down":
		return true
	case "signal.received":
		if f.Incident == 0 && strings.HasSuffix(m.Str("target"), "/"+f.Target) {
			f.Incident = m.Incident
			f.pending = m.Incident == 0
			return true
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
		f.Decision = m.Str("decision")
	case "ticket.created":
		f.TicketRef, f.Adapter = m.Str("ref"), m.Str("adapter")
	case "ticket.commented":
		if f.TicketRef == "" {
			f.TicketRef, f.Adapter = m.Str("ref"), m.Str("adapter")
		}
	case "investigation.started":
		f.Investigating = true
	case "ticket.ready":
		f.Ready = true
		f.TicketRef = m.Str("ref")
		f.URL = m.Str("url")
	}
	return true
}
