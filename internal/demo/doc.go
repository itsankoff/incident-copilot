// Package demo implements demo-svc, the small workload that runs in the kind
// cluster so incident-copilot has something realistic to investigate.
//
// The telemetry it emits (metrics, logs, probes) is a contract described in
// docs/design/demo-environment.md. Fault injection is not implemented yet.
package demo
