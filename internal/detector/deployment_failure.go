package detector

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/mdryaaan/kubelens/internal/watcher"
)

// DeploymentFailureRule detects rollouts that gave up.
type DeploymentFailureRule struct{}

// NewDeploymentFailureRule builds the rule.
func NewDeploymentFailureRule() *DeploymentFailureRule { return &DeploymentFailureRule{} }

// Category is the failure pattern this rule detects.
func (r *DeploymentFailureRule) Category() Category { return DeploymentFailure }

// Describe explains what the rule looks for.
func (r *DeploymentFailureRule) Describe() string {
	return "A Deployment's Progressing condition has gone False — the rollout exceeded its deadline."
}

// Detect flags a deployment whose rollout has stalled.
//
// This is the only rule that works at the workload level rather than the pod
// level, and it catches something the pod rules structurally cannot: a rollout
// that is failing even though every individual pod looks explainable on its
// own. It is also what surfaces a bad deploy while the previous ReplicaSet is
// still serving traffic, which is exactly when someone can still roll back
// cheaply.
func (r *DeploymentFailureRule) Detect(event watcher.WatchEvent) *Incident {
	if event.Kind != watcher.KindDeployment || event.Deploy == nil || event.Type == watcher.Deleted {
		return nil
	}

	deploy := event.Deploy
	progressing := watcher.DeploymentCondition(deploy, appsv1.DeploymentProgressing)
	if progressing == nil || progressing.Status != corev1.ConditionFalse {
		return nil
	}

	desired := int32(1)
	if deploy.Spec.Replicas != nil {
		desired = *deploy.Spec.Replicas
	}

	return &Incident{
		Category:  DeploymentFailure,
		Severity:  Critical,
		Namespace: deploy.Namespace,
		Resource:  "deployment/" + deploy.Name,
		Title:     fmt.Sprintf("Rollout of %s has stalled", deploy.Name),
		Detail: fmt.Sprintf(
			"Deployment %q reports Progressing=False with reason %s. %d of %d replica(s) are "+
				"ready and %d are updated. Kubernetes has stopped waiting for this rollout to "+
				"complete. Controller message: %s",
			deploy.Name, messageOr(progressing.Reason, "unknown"),
			deploy.Status.ReadyReplicas, desired, deploy.Status.UpdatedReplicas,
			messageOr(progressing.Message, "no message")),
		FirstSeen: progressing.LastTransitionTime.Time,
	}
}
