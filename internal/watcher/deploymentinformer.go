package watcher

import (
	"time"

	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/informers"
)

// registerDeploymentInformer wires the Deployment informer into the factory.
//
// Deployments are watched for rollout health specifically: a Progressing
// condition that has gone False is how Kubernetes reports a rollout that ran
// out of its deadline, and it is invisible from the pods alone.
func registerDeploymentInformer(factory informers.SharedInformerFactory, out chan<- WatchEvent) error {
	informer := factory.Apps().V1().Deployments().Informer()

	_, err := informer.AddEventHandler(resourceEventHandler(out, convertDeployment))
	if err != nil {
		return fmt.Errorf("registering deployment informer: %w", err)
	}
	return nil
}

func convertDeployment(obj any, eventType EventType) (WatchEvent, bool) {
	deploy, ok := obj.(*appsv1.Deployment)
	if !ok || deploy == nil {
		return WatchEvent{}, false
	}

	return WatchEvent{
		Kind:      KindDeployment,
		Type:      eventType,
		Namespace: deploy.Namespace,
		Name:      deploy.Name,
		Timestamp: deploymentTimestamp(deploy),
		Deploy:    deploy,
	}, true
}

func deploymentTimestamp(deploy *appsv1.Deployment) time.Time {
	// The newest condition transition is when the rollout actually changed
	// state, which is the moment worth reporting.
	newest := deploy.CreationTimestamp.Time
	for _, condition := range deploy.Status.Conditions {
		if condition.LastTransitionTime.Time.After(newest) {
			newest = condition.LastTransitionTime.Time
		}
	}
	return newest
}

// DeploymentCondition finds a condition by type.
func DeploymentCondition(deploy *appsv1.Deployment, kind appsv1.DeploymentConditionType) *appsv1.DeploymentCondition {
	if deploy == nil {
		return nil
	}
	for i := range deploy.Status.Conditions {
		if deploy.Status.Conditions[i].Type == kind {
			return &deploy.Status.Conditions[i]
		}
	}
	return nil
}
