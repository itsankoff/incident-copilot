package signal

// Target is the workload an Incident is about, by name and never by UID
// (ADR 0002 D4). Kind is opaque to the core.
type Target struct {
	Platform  string `json:"platform"`
	Location  string `json:"location"`
	Namespace string `json:"namespace,omitempty"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
}

func (t Target) String() string {
	if t.Namespace == "" {
		return t.Location + "/" + t.Kind + "/" + t.Name
	}
	return t.Location + "/" + t.Namespace + "/" + t.Kind + "/" + t.Name
}

// Mapping resolves a Target from an Alert's labels. It never walks owners and
// never guesses: labels that name no workload give no Target.
type Mapping struct {
	Platform       string
	NamespaceLabel string
	Workloads      []WorkloadKey // the first present label wins
}

// WorkloadKey is a label whose value names a workload of Kind.
type WorkloadKey struct {
	Label string
	Kind  string
}

// KubePrometheus is the default Mapping, for the labels kube-prometheus rules set.
var KubePrometheus = Mapping{
	Platform:       "kubernetes",
	NamespaceLabel: "namespace",
	Workloads: []WorkloadKey{
		{"deployment", "Deployment"},
		{"statefulset", "StatefulSet"},
		{"daemonset", "DaemonSet"},
		{"cronjob", "CronJob"},
		{"job_name", "Job"},
	},
}

// Resolve returns the Target labels name in location, or nil if they name none.
func (m Mapping) Resolve(location string, labels map[string]string) *Target {
	ns := labels[m.NamespaceLabel]
	if ns == "" {
		return nil
	}
	for _, w := range m.Workloads {
		if name := labels[w.Label]; name != "" {
			return &Target{Platform: m.Platform, Location: location, Namespace: ns, Kind: w.Kind, Name: name}
		}
	}
	return nil
}
