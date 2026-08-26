package detector

import (
	"fmt"

	"github.com/mdryaaan/kubelens/internal/watcher"
)

// imagePullReasons are the kubelet waiting reasons that mean the image never
// arrived. ErrImagePull is the first failure; ImagePullBackOff is the retry
// state it settles into.
var imagePullReasons = map[string]bool{
	"ImagePullBackOff": true,
	"ErrImagePull":     true,
	"InvalidImageName": true,
}

// ImagePullRule detects containers whose image cannot be fetched.
type ImagePullRule struct{}

// NewImagePullRule builds the rule.
func NewImagePullRule() *ImagePullRule { return &ImagePullRule{} }

// Category is the failure pattern this rule detects.
func (r *ImagePullRule) Category() Category { return ImagePullBackOff }

// Describe explains what the rule looks for.
func (r *ImagePullRule) Describe() string {
	return "A container is waiting in ImagePullBackOff, ErrImagePull, or InvalidImageName."
}

// Detect flags a container stuck pulling its image.
//
// This is the one failure in the set that is almost never the application's
// fault, and the fix is usually a typo in a tag or a missing pull secret — so
// the detail carries the exact image reference, which is the field a human
// needs to see to spot either.
func (r *ImagePullRule) Detect(event watcher.WatchEvent) *Incident {
	if event.Kind != watcher.KindPod || event.Pod == nil || event.Type == watcher.Deleted {
		return nil
	}

	pod := event.Pod
	for i := range pod.Status.ContainerStatuses {
		status := &pod.Status.ContainerStatuses[i]

		waiting := status.State.Waiting
		if waiting == nil || !imagePullReasons[waiting.Reason] {
			continue
		}

		image := status.Image
		if spec := watcher.ContainerSpec(pod, status.Name); spec != nil && spec.Image != "" {
			image = spec.Image
		}

		return &Incident{
			Category:  ImagePullBackOff,
			Severity:  Critical,
			Namespace: pod.Namespace,
			Resource:  "pod/" + pod.Name,
			Container: status.Name,
			Title: fmt.Sprintf("Cannot pull image for %s in %s",
				status.Name, pod.Name),
			Detail: fmt.Sprintf(
				"Container %q is waiting with reason %s and has never started. "+
					"The image reference is %q. Kubelet reported: %s",
				status.Name, waiting.Reason, image,
				messageOr(waiting.Message, "no message")),
			FirstSeen: podTimestamp(event),
		}
	}

	return nil
}
