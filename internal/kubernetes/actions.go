package kubernetes

import (
	"math"

	"github.com/itsankoff/incident-copilot/internal/action"
)

// RollbackDeployment rolls a Deployment back exactly one revision (ADR 0003
// D2). It is declared only: running it, its preconditions, Preview, and check
// are #57. The bound "exactly one revision back" needs the Deployment's
// current revision, so it is the precondition revision_is_one_back, checked
// on fresh state; revision pins the revision that step expects to return to.
// By hand, rollout undo without --to-revision goes exactly one back.
var RollbackDeployment = action.Declaration{
	Name:        "rollback_deployment",
	Description: "Roll a Deployment back to its previous ReplicaSet revision, exactly one revision back.",
	TargetKinds: []string{"Deployment"},
	Args: []action.Arg{{
		Name:        "revision",
		Description: "The ReplicaSet revision to return to, read from workload_state: the one just before the Deployment's current revision.",
		Type:        "integer", Minimum: 1, Maximum: math.MaxInt64,
	}},
	Preconditions: []string{"previous_revision_exists", "revision_is_one_back"},
	Preview:       "pod_template_diff",
	Check:         "rollout_complete",
	ByHand:        "kubectl -n {namespace} rollout undo deployment/{name}",
}

// Actions are the Actions a Kubernetes Location's Executor offers.
func Actions() []action.Declaration {
	return []action.Declaration{RollbackDeployment}
}
