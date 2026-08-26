package simulator

import (
	"fmt"
	"math/rand"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kcontext "github.com/mdryaaan/kubelens/internal/context"
	"github.com/mdryaaan/kubelens/internal/detector"
	"github.com/mdryaaan/kubelens/internal/watcher"
)

// injection is one failure applied to the fake cluster.
type injection struct {
	Category detector.Category
	Events   []watcher.WatchEvent
	Logs     []string
	// LogTarget identifies which container's output the logs belong to.
	LogNamespace, LogPod, LogContainer string
	// Records are the Kubernetes events the context builder should find.
	Records  []kcontext.EventRecord
	EventKey string
}

// injector mutates the fake cluster to produce a failure.
//
// Each injector edits the actual pod or deployment object rather than
// fabricating a detection, so the incident travels through the real detector
// rules. If a rule stops matching, the demo stops showing that incident — which
// is exactly the coupling that keeps demo mode honest about what the product
// does.
type injector func(rng *rand.Rand, cluster *Cluster, now time.Time) (injection, bool)

// injectors is the catalogue, one per detectable category.
var injectors = map[detector.Category]injector{
	detector.OOMKilled:         injectOOMKilled,
	detector.CrashLoopBackOff:  injectCrashLoop,
	detector.ImagePullBackOff:  injectImagePull,
	detector.ProbeFailure:      injectProbeFailure,
	detector.PendingTimeout:    injectPendingTimeout,
	detector.DeploymentFailure: injectDeploymentFailure,
}

// injectionOrder fixes the sequence categories are injected in.
//
// A map iterates randomly, and a demo that shows different incidents each run
// is a demo nobody can rehearse. The order deliberately opens with the two most
// visually striking failures.
var injectionOrder = []detector.Category{
	detector.OOMKilled,
	detector.CrashLoopBackOff,
	detector.ImagePullBackOff,
	detector.ProbeFailure,
	detector.DeploymentFailure,
	detector.PendingTimeout,
}

func injectOOMKilled(rng *rand.Rand, cluster *Cluster, now time.Time) (injection, bool) {
	pod := pickPod(rng, cluster, func(w workload) bool { return w.memLimit != "" })
	if pod == nil {
		return injection{}, false
	}
	w, _ := cluster.Workload(pod.Name)

	status := &pod.Status.ContainerStatuses[0]
	status.RestartCount++
	status.Ready = false
	status.LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
		Reason:     "OOMKilled",
		ExitCode:   137,
		StartedAt:  metav1.NewTime(now.Add(-4 * time.Minute)),
		FinishedAt: metav1.NewTime(now.Add(-20 * time.Second)),
	}}
	status.State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
		Reason:  "CrashLoopBackOff",
		Message: fmt.Sprintf("back-off 40s restarting failed container=%s pod=%s", w.container, pod.Name),
	}}

	message := fmt.Sprintf("Container %s exceeded its memory limit of %s and was killed",
		w.container, w.memLimit)

	return injection{
		Category:     detector.OOMKilled,
		Events:       []watcher.WatchEvent{podEventFor(pod, now)},
		Logs:         oomLines(rng, w, now),
		LogNamespace: pod.Namespace, LogPod: pod.Name, LogContainer: w.container,
		EventKey: "pod/" + pod.Name,
		Records: []kcontext.EventRecord{
			{Type: "Warning", Reason: "OOMKilling", Message: message, Count: 1, Timestamp: now.Add(-25 * time.Second)},
			{Type: "Warning", Reason: "BackOff", Message: fmt.Sprintf(
				"Back-off restarting failed container %s in pod %s_%s", w.container, pod.Name, pod.Namespace),
				Count: status.RestartCount, Timestamp: now.Add(-10 * time.Second)},
		},
	}, true
}

func injectCrashLoop(rng *rand.Rand, cluster *Cluster, now time.Time) (injection, bool) {
	pod := pickPod(rng, cluster, func(workload) bool { return true })
	if pod == nil {
		return injection{}, false
	}
	w, _ := cluster.Workload(pod.Name)

	status := &pod.Status.ContainerStatuses[0]
	status.RestartCount += 2
	status.Ready = false
	status.LastTerminationState = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
		Reason:     "Error",
		ExitCode:   1,
		StartedAt:  metav1.NewTime(now.Add(-90 * time.Second)),
		FinishedAt: metav1.NewTime(now.Add(-30 * time.Second)),
	}}
	status.State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
		Reason:  "CrashLoopBackOff",
		Message: fmt.Sprintf("back-off 2m40s restarting failed container=%s pod=%s", w.container, pod.Name),
	}}

	return injection{
		Category:     detector.CrashLoopBackOff,
		Events:       []watcher.WatchEvent{podEventFor(pod, now)},
		Logs:         crashLines(rng, w, now),
		LogNamespace: pod.Namespace, LogPod: pod.Name, LogContainer: w.container,
		EventKey: "pod/" + pod.Name,
		Records: []kcontext.EventRecord{
			{Type: "Warning", Reason: "BackOff", Message: fmt.Sprintf(
				"Back-off restarting failed container %s in pod %s_%s", w.container, pod.Name, pod.Namespace),
				Count: status.RestartCount, Timestamp: now.Add(-15 * time.Second)},
		},
	}, true
}

func injectImagePull(rng *rand.Rand, cluster *Cluster, now time.Time) (injection, bool) {
	pod := pickPod(rng, cluster, func(workload) bool { return true })
	if pod == nil {
		return injection{}, false
	}
	w, _ := cluster.Workload(pod.Name)

	// A tag that does not exist is by far the most common cause, so the
	// simulated failure edits the tag rather than inventing a registry outage.
	badImage := w.image + "-hotfix"
	pod.Spec.Containers[0].Image = badImage
	pod.Status.Phase = corev1.PodPending

	status := &pod.Status.ContainerStatuses[0]
	status.Image = badImage
	status.Ready = false
	status.State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
		Reason:  "ImagePullBackOff",
		Message: fmt.Sprintf("Back-off pulling image %q", badImage),
	}}

	pullMessage := fmt.Sprintf(
		"Failed to pull image %q: rpc error: code = NotFound desc = failed to pull and unpack image: "+
			"not found: manifest unknown", badImage)

	return injection{
		Category:     detector.ImagePullBackOff,
		Events:       []watcher.WatchEvent{podEventFor(pod, now)},
		Logs:         imagePullLines(rng, w, now),
		LogNamespace: pod.Namespace, LogPod: pod.Name, LogContainer: w.container,
		EventKey: "pod/" + pod.Name,
		Records: []kcontext.EventRecord{
			{Type: "Normal", Reason: "Pulling", Message: fmt.Sprintf("Pulling image %q", badImage),
				Count: 1, Timestamp: now.Add(-70 * time.Second)},
			{Type: "Warning", Reason: "Failed", Message: pullMessage, Count: 4, Timestamp: now.Add(-40 * time.Second)},
			{Type: "Warning", Reason: "Failed", Message: fmt.Sprintf(
				"Error: ErrImagePull for container %s", w.container), Count: 4, Timestamp: now.Add(-30 * time.Second)},
		},
	}, true
}

func injectProbeFailure(rng *rand.Rand, cluster *Cluster, now time.Time) (injection, bool) {
	pod := pickPod(rng, cluster, func(w workload) bool { return w.probe })
	if pod == nil {
		return injection{}, false
	}
	w, _ := cluster.Workload(pod.Name)

	pod.Status.ContainerStatuses[0].Ready = false
	setPodCondition(pod, corev1.PodReady, corev1.ConditionFalse, "ContainersNotReady",
		fmt.Sprintf("containers with unready status: [%s]", w.container))

	message := fmt.Sprintf(
		"Readiness probe failed: Get \"http://10.244.1.%d:8080/healthz\": context deadline exceeded "+
			"(Client.Timeout exceeded while awaiting headers)", 20+rng.Intn(200))

	// The probe rule needs several warnings inside its window: one failure is
	// how a slow container reports it is not ready yet, not an incident.
	var events []watcher.WatchEvent
	var records []kcontext.EventRecord
	for i := 0; i < 4; i++ {
		at := now.Add(-time.Duration(4-i) * 20 * time.Second)
		events = append(events, probeWarningEvent(pod, w, message, at))
		records = append(records, kcontext.EventRecord{
			Type: "Warning", Reason: "Unhealthy", Message: message,
			Count: int32(i + 1), Timestamp: at, //nolint:gosec // i is bounded by the loop above
		})
	}

	return injection{
		Category:     detector.ProbeFailure,
		Events:       events,
		Logs:         probeLines(rng, w, now),
		LogNamespace: pod.Namespace, LogPod: pod.Name, LogContainer: w.container,
		EventKey: "pod/" + pod.Name,
		Records:  records,
	}, true
}

func injectPendingTimeout(rng *rand.Rand, cluster *Cluster, now time.Time) (injection, bool) {
	pod := pickPod(rng, cluster, func(workload) bool { return true })
	if pod == nil {
		return injection{}, false
	}
	w, _ := cluster.Workload(pod.Name)

	// Backdated past the rule's threshold, because the incident is the
	// duration, not the state.
	created := metav1.NewTime(now.Add(-11 * time.Minute))
	pod.CreationTimestamp = created
	pod.Status.StartTime = nil
	pod.Status.Phase = corev1.PodPending
	pod.Spec.NodeName = ""
	pod.Status.ContainerStatuses = nil

	message := fmt.Sprintf(
		"0/%d nodes are available: %d Insufficient cpu, %d Insufficient memory. "+
			"preemption: 0/%d nodes are available: %d No preemption victims found for incoming pod.",
		len(cluster.Nodes), len(cluster.Nodes)-1, 1, len(cluster.Nodes), len(cluster.Nodes))

	setPodCondition(pod, corev1.PodScheduled, corev1.ConditionFalse, "Unschedulable", message)

	return injection{
		Category:     detector.PendingTimeout,
		Events:       []watcher.WatchEvent{podEventFor(pod, now)},
		Logs:         pendingLines(rng, w, now),
		LogNamespace: pod.Namespace, LogPod: pod.Name, LogContainer: w.container,
		EventKey: "pod/" + pod.Name,
		Records: []kcontext.EventRecord{
			{Type: "Warning", Reason: "FailedScheduling", Message: message,
				Count: 9, Timestamp: now.Add(-9 * time.Minute)},
		},
	}, true
}

func injectDeploymentFailure(rng *rand.Rand, cluster *Cluster, now time.Time) (injection, bool) {
	if len(cluster.Deployments) == 0 {
		return injection{}, false
	}
	deploy := cluster.Deployments[rng.Intn(len(cluster.Deployments))]

	replicaSet := fmt.Sprintf("%s-%s", deploy.Name, randomSuffix(rng, 5))
	message := fmt.Sprintf("ReplicaSet %q has timed out progressing.", replicaSet)

	deploy.Status.ReadyReplicas = 1
	deploy.Status.UpdatedReplicas = 2
	deploy.Status.AvailableReplicas = 1
	setDeploymentCondition(deploy, appsv1.DeploymentProgressing, corev1.ConditionFalse,
		"ProgressDeadlineExceeded", message, now)

	return injection{
		Category: detector.DeploymentFailure,
		Events: []watcher.WatchEvent{{
			Kind: watcher.KindDeployment, Type: watcher.Modified,
			Namespace: deploy.Namespace, Name: deploy.Name,
			Timestamp: now, Deploy: deploy.DeepCopy(),
		}},
		EventKey: "deployment/" + deploy.Name,
		Records: []kcontext.EventRecord{
			{Type: "Warning", Reason: "ProgressDeadlineExceeded", Message: message,
				Count: 1, Timestamp: now.Add(-30 * time.Second)},
		},
	}, true
}

// pickPod chooses a healthy pod matching a predicate.
//
// Healthy only: injecting a second failure into an already-broken pod would
// produce a state Kubernetes never actually reports, and the detector would be
// right to be confused by it.
func pickPod(rng *rand.Rand, cluster *Cluster, match func(workload) bool) *corev1.Pod {
	var candidates []*corev1.Pod

	for _, pod := range cluster.Pods {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		if len(pod.Status.ContainerStatuses) == 0 ||
			pod.Status.ContainerStatuses[0].State.Waiting != nil {
			continue
		}
		w, ok := cluster.Workload(pod.Name)
		if !ok || !match(w) {
			continue
		}
		candidates = append(candidates, pod)
	}

	if len(candidates) == 0 {
		return nil
	}
	return candidates[rng.Intn(len(candidates))]
}

func podEventFor(pod *corev1.Pod, now time.Time) watcher.WatchEvent {
	return watcher.WatchEvent{
		Kind: watcher.KindPod, Type: watcher.Modified,
		Namespace: pod.Namespace, Name: pod.Name,
		Timestamp: now, Pod: pod.DeepCopy(),
	}
}

func probeWarningEvent(pod *corev1.Pod, w workload, message string, at time.Time) watcher.WatchEvent {
	return watcher.WatchEvent{
		Kind: watcher.KindEvent, Type: watcher.Added,
		Namespace: pod.Namespace, Name: pod.Name, Timestamp: at,
		Event: &corev1.Event{
			ObjectMeta:    metav1.ObjectMeta{Namespace: pod.Namespace},
			Type:          corev1.EventTypeWarning,
			Reason:        "Unhealthy",
			Message:       message,
			LastTimestamp: metav1.NewTime(at),
			InvolvedObject: corev1.ObjectReference{
				Kind: "Pod", Name: pod.Name, Namespace: pod.Namespace,
				FieldPath: fmt.Sprintf("spec.containers{%s}", w.container),
			},
		},
	}
}

func setPodCondition(pod *corev1.Pod, kind corev1.PodConditionType, status corev1.ConditionStatus, reason, message string) {
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == kind {
			pod.Status.Conditions[i].Status = status
			pod.Status.Conditions[i].Reason = reason
			pod.Status.Conditions[i].Message = message
			return
		}
	}
	pod.Status.Conditions = append(pod.Status.Conditions, corev1.PodCondition{
		Type: kind, Status: status, Reason: reason, Message: message,
	})
}

func setDeploymentCondition(deploy *appsv1.Deployment, kind appsv1.DeploymentConditionType,
	status corev1.ConditionStatus, reason, message string, now time.Time) {
	for i := range deploy.Status.Conditions {
		if deploy.Status.Conditions[i].Type == kind {
			deploy.Status.Conditions[i].Status = status
			deploy.Status.Conditions[i].Reason = reason
			deploy.Status.Conditions[i].Message = message
			deploy.Status.Conditions[i].LastTransitionTime = metav1.NewTime(now)
			deploy.Status.Conditions[i].LastUpdateTime = metav1.NewTime(now)
			return
		}
	}
	deploy.Status.Conditions = append(deploy.Status.Conditions, appsv1.DeploymentCondition{
		Type: kind, Status: status, Reason: reason, Message: message,
		LastTransitionTime: metav1.NewTime(now), LastUpdateTime: metav1.NewTime(now),
	})
}

// podIsBroken reports whether a pod is in any state the detectors would flag.
func podIsBroken(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return true
	}
	for i := range pod.Status.ContainerStatuses {
		status := &pod.Status.ContainerStatuses[i]
		if status.State.Waiting != nil || !status.Ready {
			return true
		}
	}
	return len(pod.Status.ContainerStatuses) == 0
}

// healPod returns a pod to a healthy Running state.
//
// The restart count is kept: a container that crashed six times and then
// stabilised still restarted six times, and zeroing it would erase history the
// dashboard legitimately shows.
func healPod(pod *corev1.Pod, w workload, now time.Time) {
	started := metav1.NewTime(now)

	pod.Spec.Containers[0].Image = w.image
	pod.Status.Phase = corev1.PodRunning
	pod.Status.StartTime = &started

	restarts := int32(0)
	if len(pod.Status.ContainerStatuses) > 0 {
		restarts = pod.Status.ContainerStatuses[0].RestartCount
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: w.container, Image: w.image, Ready: true, RestartCount: restarts,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: started}},
	}}

	if pod.Spec.NodeName == "" {
		pod.Spec.NodeName = nodeNames[0]
	}
	setPodCondition(pod, corev1.PodScheduled, corev1.ConditionTrue, "", "")
	setPodCondition(pod, corev1.PodReady, corev1.ConditionTrue, "", "")
}

// deploymentProgressing returns a stalled Progressing condition, if any.
func deploymentProgressing(deploy *appsv1.Deployment) *appsv1.DeploymentCondition {
	for i := range deploy.Status.Conditions {
		condition := &deploy.Status.Conditions[i]
		if condition.Type == appsv1.DeploymentProgressing && condition.Status == corev1.ConditionFalse {
			return condition
		}
	}
	return nil
}

// healDeployment completes a stalled rollout.
func healDeployment(deploy *appsv1.Deployment, now time.Time) {
	desired := int32(1)
	if deploy.Spec.Replicas != nil {
		desired = *deploy.Spec.Replicas
	}

	deploy.Status.ReadyReplicas = desired
	deploy.Status.UpdatedReplicas = desired
	deploy.Status.AvailableReplicas = desired
	setDeploymentCondition(deploy, appsv1.DeploymentProgressing, corev1.ConditionTrue,
		"NewReplicaSetAvailable", "ReplicaSet has successfully progressed.", now)
}
