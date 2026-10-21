package signal

import "testing"

func TestKubePrometheusResolve(t *testing.T) {
	const loc = "kind-incident-copilot"
	target := func(ns, kind, name string) *Target {
		return &Target{Platform: "kubernetes", Location: loc, Namespace: ns, Kind: kind, Name: name}
	}
	tests := []struct {
		name   string
		labels map[string]string
		want   *Target
	}{
		{"deployment", map[string]string{"namespace": "demo", "deployment": "orders"}, target("demo", "Deployment", "orders")},
		{"statefulset", map[string]string{"namespace": "db", "statefulset": "pg"}, target("db", "StatefulSet", "pg")},
		{"daemonset", map[string]string{"namespace": "kube-system", "daemonset": "kube-proxy"}, target("kube-system", "DaemonSet", "kube-proxy")},
		{"cronjob", map[string]string{"namespace": "ops", "cronjob": "backup"}, target("ops", "CronJob", "backup")},
		{"job_name", map[string]string{"namespace": "ops", "job_name": "backup-29000"}, target("ops", "Job", "backup-29000")},
		{"first in order wins", map[string]string{"namespace": "demo", "job_name": "j", "deployment": "orders", "statefulset": "s"}, target("demo", "Deployment", "orders")},
		{"pod also present", map[string]string{"namespace": "demo", "deployment": "orders", "pod": "orders-7d9f-abcde"}, target("demo", "Deployment", "orders")},

		// Never a guess.
		{"pod only", map[string]string{"namespace": "demo", "pod": "orders-7d9f8c6b5-x2k4p"}, nil},
		{"pod and container only", map[string]string{"namespace": "demo", "pod": "orders-7d9f8c6b5-x2k4p", "container": "orders"}, nil},
		{"replicaset is not mapped", map[string]string{"namespace": "demo", "replicaset": "orders-7d9f8c6b5"}, nil},
		{"scrape job is not job_name", map[string]string{"namespace": "demo", "job": "orders"}, nil},
		{"no namespace", map[string]string{"deployment": "orders"}, nil},
		{"empty workload value", map[string]string{"namespace": "demo", "deployment": ""}, nil},
		{"no labels", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := KubePrometheus.Resolve(loc, tt.labels)
			switch {
			case tt.want == nil && got != nil:
				t.Fatalf("got Target %+v, want none", *got)
			case tt.want != nil && got == nil:
				t.Fatalf("got no Target, want %+v", *tt.want)
			case tt.want != nil && *got != *tt.want:
				t.Fatalf("got %+v, want %+v", *got, *tt.want)
			}
		})
	}
}
