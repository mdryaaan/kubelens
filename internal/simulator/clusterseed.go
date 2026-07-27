// Package simulator generates a deterministic fake cluster so kubelens is
// fully runnable with no Kubernetes cluster at all.
package simulator

import (
	"fmt"
	"math/rand"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// workload describes one service in the fake cluster.
//
// The shapes are drawn from real deployments rather than invented: a JVM
// service with a heap near its limit, a Node API with a readiness probe, a
// batch worker with no probes at all. Each one is chosen so that a specific
// incident category is plausible for it, which is what makes the injected
// failures believable rather than arbitrary.
type workload struct {
	namespace string
	name      string
	container string
	image     string
	replicas  int32
	memLimit  string
	cpuLimit  string
	runtime   runtimeKind
	probe     bool
}

// runtimeKind decides what the generated logs look like.
type runtimeKind string

const (
	runtimeJVM    runtimeKind = "jvm"
	runtimeNode   runtimeKind = "node"
	runtimeGo     runtimeKind = "go"
	runtimePython runtimeKind = "python"
)

// workloads is the fixed catalogue the simulated cluster is built from.
var workloads = []workload{
	{"payments", "payment-api", "api", "ghcr.io/acme/payment-api:1.4.2", 4, "512Mi", "500m", runtimeJVM, true},
	{"payments", "ledger-worker", "worker", "ghcr.io/acme/ledger-worker:2.1.0", 2, "1Gi", "1", runtimeGo, false},
	{"auth", "auth-service", "auth", "ghcr.io/acme/auth-service:3.0.7", 3, "256Mi", "250m", runtimeNode, true},
	{"auth", "session-cache", "cache", "redis:7.2-alpine", 2, "512Mi", "500m", runtimeGo, true},
	{"shop", "checkout-api", "api", "ghcr.io/acme/checkout-api:5.2.1", 3, "384Mi", "500m", runtimeNode, true},
	{"shop", "catalog-indexer", "indexer", "ghcr.io/acme/catalog-indexer:0.9.4", 2, "2Gi", "2", runtimePython, false},
	{"platform", "notification-relay", "relay", "ghcr.io/acme/notification-relay:1.1.3", 2, "256Mi", "200m", runtimeGo, true},
	{"platform", "report-builder", "builder", "ghcr.io/acme/report-builder:4.0.0", 1, "1Gi", "1", runtimePython, false},
}

// nodeNames are the nodes pods are scheduled onto.
var nodeNames = []string{
	"ip-10-0-1-42.eu-west-1.compute.internal",
	"ip-10-0-2-17.eu-west-1.compute.internal",
	"ip-10-0-3-88.eu-west-1.compute.internal",
	"ip-10-0-4-201.eu-west-1.compute.internal",
}

// Cluster is the generated fake cluster.
type Cluster struct {
	Nodes       []string
	Pods        []*corev1.Pod
	Deployments []*appsv1.Deployment
	// podWorkload maps a pod name back to the workload that produced it, so the
	// incident generator knows what kind of runtime it is failing.
	podWorkload map[string]workload
}

// seedCluster builds the fake cluster deterministically from rng.
//
// Every random choice — the pod name suffix, the node, the start time — comes
// from the seeded generator and nothing else, so two runs with the same seed
// produce byte-identical clusters. That is what makes a demo rehearsable.
func seedCluster(rng *rand.Rand, now metav1.Time) *Cluster {
	cluster := &Cluster{
		Nodes:       append([]string(nil), nodeNames...),
		podWorkload: make(map[string]workload),
	}

	for _, w := range workloads {
		cluster.Deployments = append(cluster.Deployments, buildDeployment(w, now))

		replicaSet := randomSuffix(rng, 5)
		for i := int32(0); i < w.replicas; i++ {
			pod := buildPod(w, replicaSet, randomSuffix(rng, 5), nodeNames[rng.Intn(len(nodeNames))], now, rng)
			cluster.Pods = append(cluster.Pods, pod)
			cluster.podWorkload[pod.Name] = w
		}
	}

	return cluster
}

// PodCount returns how many pods the cluster holds.
func (c *Cluster) PodCount() int { return len(c.Pods) }

// Workload returns the workload a pod belongs to.
func (c *Cluster) Workload(podName string) (workload, bool) {
	w, ok := c.podWorkload[podName]
	return w, ok
}

// Pod finds a pod by name.
func (c *Cluster) Pod(name string) *corev1.Pod {
	for _, pod := range c.Pods {
		if pod.Name == name {
			return pod
		}
	}
	return nil
}

// Deployment finds a deployment by namespace and name.
func (c *Cluster) Deployment(namespace, name string) *appsv1.Deployment {
	for _, deploy := range c.Deployments {
		if deploy.Namespace == namespace && deploy.Name == name {
			return deploy
		}
	}
	return nil
}

// Unhealthy counts pods not in the Running phase or with a waiting container.
func (c *Cluster) Unhealthy() int {
	count := 0
	for _, pod := range c.Pods {
		if pod.Status.Phase != corev1.PodRunning {
			count++
			continue
		}
		for i := range pod.Status.ContainerStatuses {
			if pod.Status.ContainerStatuses[i].State.Waiting != nil {
				count++
				break
			}
		}
	}
	return count
}

func buildDeployment(w workload, now metav1.Time) *appsv1.Deployment {
	replicas := w.replicas
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: w.name, Namespace: w.namespace,
			CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour)),
			Labels:            map[string]string{"app": w.name},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": w.name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": w.name}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{buildContainer(w)}},
			},
		},
		Status: appsv1.DeploymentStatus{
			Replicas: replicas, ReadyReplicas: replicas, UpdatedReplicas: replicas,
			AvailableReplicas: replicas,
			Conditions: []appsv1.DeploymentCondition{{
				Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue,
				Reason: "MinimumReplicasAvailable", LastTransitionTime: now,
			}, {
				Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue,
				Reason: "NewReplicaSetAvailable", LastTransitionTime: now, LastUpdateTime: now,
			}},
		},
	}
}

func buildContainer(w workload) corev1.Container {
	container := corev1.Container{
		Name:  w.container,
		Image: w.image,
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse(w.memLimit),
				corev1.ResourceCPU:    resource.MustParse(w.cpuLimit),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse(halfQuantity(w.memLimit)),
			},
		},
	}

	if w.probe {
		container.ReadinessProbe = &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
				Path: "/healthz", Port: intstr.FromInt32(8080),
			}},
			InitialDelaySeconds: 5, PeriodSeconds: 10,
			TimeoutSeconds: 1, FailureThreshold: 3,
		}
		container.LivenessProbe = &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
				Path: "/livez", Port: intstr.FromInt32(8080),
			}},
			InitialDelaySeconds: 15, PeriodSeconds: 20,
			TimeoutSeconds: 2, FailureThreshold: 3,
		}
	}

	return container
}

func buildPod(w workload, replicaSet, suffix, node string, now metav1.Time, rng *rand.Rand) *corev1.Pod {
	started := metav1.NewTime(now.Add(-time.Duration(rng.Intn(6*60*60)) * time.Second))

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              fmt.Sprintf("%s-%s-%s", w.name, replicaSet, suffix),
			Namespace:         w.namespace,
			CreationTimestamp: started,
			Labels:            map[string]string{"app": w.name, "pod-template-hash": replicaSet},
		},
		Spec: corev1.PodSpec{
			NodeName:      node,
			RestartPolicy: corev1.RestartPolicyAlways,
			Containers:    []corev1.Container{buildContainer(w)},
		},
		Status: corev1.PodStatus{
			Phase:     corev1.PodRunning,
			StartTime: &started,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: w.container, Image: w.image, Ready: true, RestartCount: 0,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: started}},
			}},
		},
	}
}

// halfQuantity halves a memory string for the request, so requests and limits
// differ the way they do in a real manifest.
func halfQuantity(limit string) string {
	quantity := resource.MustParse(limit)
	value := quantity.Value() / 2
	return resource.NewQuantity(value, quantity.Format).String()
}

const suffixAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

func randomSuffix(rng *rand.Rand, n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = suffixAlphabet[rng.Intn(len(suffixAlphabet))]
	}
	return string(out)
}
