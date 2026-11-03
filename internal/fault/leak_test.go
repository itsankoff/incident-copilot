package fault

import (
	"slices"
	"testing"
)

func TestLeaks(t *testing.T) {
	for _, tc := range []struct {
		scenario, text string
		want           []string
	}{
		{"oom", `{"msg":"fault injected"}`, []string{"fault", "injected"}},
		{"oom", `faultctl ran; Injection done`, []string{"faultctl", "Injection"}},
		{"crash-loop", `scenario crash-loop armed`, []string{"crash-loop"}},
		{"bad-image", `image: demo-svc:bad-image`, []string{"bad-image"}},
		// Kubernetes' own words are evidence, not leaks.
		{"oom", `schedulerName: default-scheduler, reason: OOMKilled`, nil},
		{"unschedulable", `PodScheduled False Unschedulable; alert PodUnschedulable`, nil},
		{"crash-loop", `reason: CrashLoopBackOff`, nil},
	} {
		if got := Leaks(tc.scenario, tc.text); !slices.Equal(got, tc.want) {
			t.Errorf("Leaks(%q, %q) = %q, want %q", tc.scenario, tc.text, got, tc.want)
		}
	}
}
