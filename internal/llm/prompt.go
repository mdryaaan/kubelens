package llm

import (
	"fmt"
	"strings"
)

// SystemPrompt frames the model's role and, critically, forbids invented
// evidence.
//
// The citation rule is the load-bearing instruction. An explanation that quotes
// a log line reads as authoritative, so a fabricated quote is worse than no
// quote at all — it manufactures confidence in an analysis nothing supports.
// The instruction is reinforced by the verification step, which drops any
// citation not literally present in the context; prompting alone is a first
// line of defence, never a guarantee.
//
// The second instruction that matters is the deference to the rule. The
// detector read the container's actual status field. That is stronger evidence
// than any amount of prose, and a model that "corrects" it is almost always
// wrong — so disagreement is allowed but has to be justified from the evidence.
const SystemPrompt = `You are a Kubernetes site reliability engineer explaining why a
workload is failing. You are given one incident that a deterministic rule already
detected, together with the container logs, Kubernetes events, and resource spec that
concern it.

Your job is to explain the ROOT CAUSE in plain English, so that an engineer who has
not looked at this cluster knows what broke and what to do next.

Rules you must follow:
1. Answer with a single JSON object and nothing else. No prose before or after.
2. "cited_evidence" must contain ONLY lines copied verbatim from the CONTAINER LOGS or
   KUBERNETES EVENTS sections you were given. Never invent, paraphrase, reformat, or
   reconstruct a line. If nothing in the evidence supports your explanation, return an
   empty array. A wrong quote is worse than no quote.
3. The "category" was determined by a rule that read the container's actual status
   fields. Repeat that category unless the evidence plainly contradicts it, and if you
   do disagree, say why in the explanation and cite the line that convinced you.
4. Explain the specific cause, not the general category. "The container ran out of
   memory" restates the label. "The JVM heap is capped at 512Mi by the container limit
   while the application allocates a 700Mi cache on startup" is an explanation.
5. "suggested_fix" is one concrete action: a field to change, a value to raise, a
   command to run. Not "investigate further".
6. "confidence" is your genuine probability that the explanation is correct, from 0 to
   1. Do not default to 0.9.

What the six categories mean:
- CrashLoopBackOff: the container keeps starting and exiting, so kubelet is backing off
  between restarts. The interesting question is always why it exits.
- OOMKilled: the kernel killed the container for exceeding its memory cgroup limit.
  Look for the limit in the spec and for allocation behaviour in the logs.
- ImagePullBackOff: the image could not be fetched. Usually a bad tag, a missing pull
  secret, or a registry that is unreachable.
- ProbeFailure: a readiness or liveness probe keeps failing. Look at the probe's own
  timing settings as often as at the application.
- PendingTimeout: the scheduler could not place the pod. The reason is almost always in
  the PodScheduled condition — insufficient resources, node selectors, unbound volumes.
- DeploymentFailure: a rollout exceeded its progress deadline. The cause is usually
  visible in the pods the new ReplicaSet created.`

const userPromptTemplate = `Incident to explain:
  category (from the rule): %s
  namespace: %s
  resource: %s%s

Evidence you may cite from — nothing outside this block exists:
---BEGIN EVIDENCE---
%s
---END EVIDENCE---

Respond with exactly this JSON shape:
%s`

// UnknownCategory is what BuildPrompt shows when the caller has not run a rule
// first.
//
// The eval harness uses this: measuring a model against a corpus while handing
// it the right answer in the prompt would measure nothing but its ability to
// copy. Everywhere else the rule's category is given, because the rule read the
// container's actual status and the model should not have to guess at it.
const UnknownCategory = "(not determined — you must classify from the evidence alone)"

// BuildPrompt renders the full user prompt for a request.
func BuildPrompt(req Request) string {
	container := ""
	if req.Container != "" {
		container = fmt.Sprintf("\n  container: %s", req.Container)
	}

	evidence := strings.TrimSpace(req.Context)
	if evidence == "" {
		evidence = "(no evidence was available)"
	}

	category := req.Category
	if strings.TrimSpace(category) == "" {
		category = UnknownCategory
	}

	return fmt.Sprintf(userPromptTemplate,
		category, req.Namespace, req.Resource, container, evidence, SchemaJSON)
}

// RepairPrompt is appended on the single retry after a malformed response. It
// restates only the format requirement, since the analysis itself may have been
// sound and only the envelope was wrong.
const RepairPrompt = `Your previous response could not be parsed.
Respond again with ONE valid JSON object and no other text.
"category" must be exactly one of: CrashLoopBackOff, OOMKilled, ImagePullBackOff,
ProbeFailure, PendingTimeout, DeploymentFailure.
"cited_evidence" must be an array of strings copied verbatim from the evidence block.`
