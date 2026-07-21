package llm

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/mdryaaan/kubelens/internal/detector"
)

// OfflineDisclaimer must accompany every result this provider produces.
//
// It is not decoration. This provider does not run a model — it applies a small
// set of hand-written templates and keyword matches to the evidence. Reporting
// its output as model accuracy would be a fabricated measurement, so the
// disclaimer is carried in the explanation text itself, stored with the record,
// served by the API, shown in the dashboard, and repeated in the README beside
// any number derived from it.
const OfflineDisclaimer = "BASELINE (no model): produced by deterministic templates, " +
	"not by an LLM. Use --provider ollama or --provider claude for real model reasoning."

// Offline explains incidents without a model.
//
// It exists so the whole product — detect, explain, verify, store, stream —
// runs on a machine with no Ollama daemon and no API key, and so the eval
// harness has a labelled control to measure a real model against. A model that
// cannot beat a page of keyword matching is not earning its inference cost, and
// without this baseline there is no way to know whether it does.
type Offline struct{}

// NewOffline builds the offline baseline provider.
func NewOffline() *Offline { return &Offline{} }

// Name identifies the provider.
func (o *Offline) Name() string { return ProviderOffline }

// Model reports that no model is involved.
//
// The string is deliberately not a plausible model name: it appears in the API
// response and in the dashboard's settings page, and anyone reading either
// should be able to tell at a glance that these explanations are not a model's.
func (o *Offline) Model() string { return "template-baseline (not a model)" }

// categorySignals are the phrases that identify each failure in raw text.
//
// Ordered by specificity within each category: the first match is the line
// cited, so the most diagnostic phrase is listed first.
var categorySignals = map[detector.Category][]string{
	detector.OOMKilled: {
		"OutOfMemoryError", "OOMKilled", "out of memory", "Killed process",
		"memory cgroup out of memory", "heap space",
	},
	detector.ImagePullBackOff: {
		"ImagePullBackOff", "ErrImagePull", "manifest unknown", "pull access denied",
		"not found: manifest", "unauthorized", "no such host",
	},
	detector.CrashLoopBackOff: {
		"CrashLoopBackOff", "Back-off restarting failed container", "panic:",
		"Traceback (most recent call last)", "Exception in thread", "exited with code",
	},
	detector.ProbeFailure: {
		"Readiness probe failed", "Liveness probe failed", "Startup probe failed",
		"probe failed", "connection refused",
	},
	detector.PendingTimeout: {
		"Insufficient cpu", "Insufficient memory", "didn't match Pod's node affinity",
		"had untolerated taint", "FailedScheduling", "unbound immediate PersistentVolumeClaims",
	},
	detector.DeploymentFailure: {
		"ProgressDeadlineExceeded", "has timed out progressing", "ReplicaSet",
	},
}

// templates carry the explanation shape for each category.
var templates = map[detector.Category]struct {
	summary string
	fix     string
}{
	detector.OOMKilled: {
		summary: "The container exceeded its memory limit and the kernel terminated it. " +
			"The process asked the cgroup for more memory than the limit allows, so it was " +
			"killed rather than allowed to grow.",
		fix: "Raise the container's memory limit, or reduce what the application allocates at " +
			"startup — whichever the log's allocation pattern points to.",
	},
	detector.CrashLoopBackOff: {
		summary: "The container starts, exits, and is restarted, so kubelet is backing off " +
			"between attempts. The exit is happening early enough that the process never " +
			"reaches a steady state.",
		fix: "Read the last lines before each exit and fix the error the process reports there; " +
			"the backoff itself is a symptom, not the cause.",
	},
	detector.ImagePullBackOff: {
		summary: "The image could not be fetched, so the container has never started. " +
			"Kubelet is retrying the pull with a growing backoff.",
		fix: "Check the image tag for a typo and confirm the pull secret grants access to that " +
			"registry.",
	},
	detector.ProbeFailure: {
		summary: "A health probe keeps failing, so Kubernetes considers the pod unable to serve " +
			"traffic. This is as often a probe configured too aggressively as an unhealthy " +
			"application.",
		fix: "Compare the probe's initialDelaySeconds and timeoutSeconds against how long the " +
			"application actually takes to answer, then fix whichever is wrong.",
	},
	detector.PendingTimeout: {
		summary: "The scheduler could not place this pod on any node, so it has stayed Pending. " +
			"The PodScheduled condition carries the specific obstacle.",
		fix: "Address the scheduler's stated reason — add capacity, relax the node selector, or " +
			"bind the outstanding volume claim.",
	},
	detector.DeploymentFailure: {
		summary: "The rollout exceeded its progress deadline and Kubernetes stopped waiting for " +
			"it. The new ReplicaSet's pods never became ready.",
		fix: "Inspect the pods the new ReplicaSet created — their failure is the rollout's " +
			"failure — and roll back if the previous version is still serving.",
	},
}

// Explain produces a templated explanation from the evidence alone.
func (o *Offline) Explain(_ context.Context, req Request) (Explanation, error) {
	category, cited, confidence := o.classify(req)

	template, ok := templates[category]
	if !ok {
		template = templates[detector.CrashLoopBackOff]
	}

	summary := fmt.Sprintf("%s %s", OfflineDisclaimer, template.summary)
	if detail := o.specificDetail(req, category); detail != "" {
		summary += " " + detail
	}

	explanation := Explanation{
		Category:      category,
		Confidence:    confidence,
		Summary:       summary,
		CitedEvidence: cited,
		SuggestedFix:  template.fix,
	}

	if err := explanation.Validate(); err != nil {
		return Explanation{}, err
	}
	return explanation, nil
}

// classify picks the category whose signals appear in the evidence.
//
// Every citation it returns is a line lifted verbatim out of the evidence
// slice, so by construction it cannot fabricate one. That is a property of the
// mechanism rather than an achievement, and it is stated wherever this
// provider's citation score is reported next to a model's.
func (o *Offline) classify(req Request) (detector.Category, []string, float64) {
	ruleCategory, ruleErr := detector.ParseCategory(req.Category)

	best := ruleCategory
	var cited []string
	matched := 0

	for category, signals := range categorySignals {
		var hits []string
		for _, signal := range signals {
			for _, line := range req.Evidence {
				if strings.Contains(strings.ToLower(line), strings.ToLower(signal)) {
					hits = append(hits, line)
					break
				}
			}
		}
		if len(hits) > matched {
			matched = len(hits)
			best = category
			cited = hits
		}
	}

	if len(cited) > 3 {
		cited = cited[:3]
	}

	switch {
	case ruleErr != nil && matched == 0:
		// Nothing to go on at all: the rule's label was unreadable and the
		// evidence matched nothing.
		return detector.CrashLoopBackOff, nil, 0.2
	case ruleErr != nil:
		return best, cited, 0.5
	case matched == 0:
		// Defer to the rule, which read the container's actual status.
		return ruleCategory, nil, 0.4
	case best == ruleCategory:
		return ruleCategory, cited, 0.85
	default:
		// The evidence points somewhere else than the rule did. The rule read a
		// status field and this read text, so the rule wins — but the
		// disagreement lowers confidence rather than being hidden.
		return ruleCategory, cited, 0.55
	}
}

var (
	memoryLimitPattern = regexp.MustCompile(`(?i)memory limit ([0-9]+[KMG]i?)`)
	exitCodePattern    = regexp.MustCompile(`(?i)exit code (\d+)`)
)

// specificDetail lifts a concrete fact out of the incident detail so the
// explanation says something about this workload rather than only about the
// category in general.
func (o *Offline) specificDetail(req Request, category detector.Category) string {
	switch category {
	case detector.OOMKilled:
		if m := memoryLimitPattern.FindStringSubmatch(req.Context); len(m) == 2 {
			return fmt.Sprintf("The configured limit is %s.", m[1])
		}
	case detector.CrashLoopBackOff:
		if m := exitCodePattern.FindStringSubmatch(req.Context); len(m) == 2 {
			return fmt.Sprintf("The container's last exit code was %s.", m[1])
		}
	}
	return ""
}
