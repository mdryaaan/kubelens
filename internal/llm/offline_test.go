package llm

import (
	"context"
	"strings"
	"testing"

	"github.com/mdryaaan/kubelens/internal/detector"
)

// Nothing derived from this provider may be presented as model output.
func TestOfflineNeverPresentsItselfAsAModel(t *testing.T) {
	provider := NewOffline()

	if !strings.Contains(provider.Model(), "not a model") {
		t.Errorf("Model() = %q, which could be mistaken for a model name", provider.Model())
	}

	got, err := provider.Explain(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("Explain failed: %v", err)
	}
	if !strings.HasPrefix(got.Summary, OfflineDisclaimer) {
		t.Errorf("the explanation does not lead with the disclaimer:\n%s", got.Summary)
	}
}

func TestOfflineClassifiesFromEvidence(t *testing.T) {
	tests := []struct {
		name     string
		category string
		evidence []string
		want     detector.Category
	}{
		{
			name:     "oom from a jvm trace",
			category: "OOMKilled",
			evidence: []string{"FATAL java.lang.OutOfMemoryError: Java heap space"},
			want:     detector.OOMKilled,
		},
		{
			name:     "image pull from a registry message",
			category: "ImagePullBackOff",
			evidence: []string{`Failed to pull image: rpc error: manifest unknown`},
			want:     detector.ImagePullBackOff,
		},
		{
			name:     "probe failure from a kubelet message",
			category: "ProbeFailure",
			evidence: []string{"Readiness probe failed: HTTP probe failed with statuscode: 503"},
			want:     detector.ProbeFailure,
		},
		{
			name:     "scheduling from the scheduler's reason",
			category: "PendingTimeout",
			evidence: []string{"0/4 nodes are available: 4 Insufficient cpu."},
			want:     detector.PendingTimeout,
		},
		{
			name:     "rollout from the progress deadline",
			category: "DeploymentFailure",
			evidence: []string{`ReplicaSet "payment-api-7d9f" has timed out progressing.`},
			want:     detector.DeploymentFailure,
		},
	}

	provider := NewOffline()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := provider.Explain(context.Background(), Request{
				Category: tc.category, Evidence: tc.evidence,
				Context: strings.Join(tc.evidence, "\n"),
			})
			if err != nil {
				t.Fatalf("Explain failed: %v", err)
			}
			if got.Category != tc.want {
				t.Errorf("category = %s, want %s", got.Category, tc.want)
			}
			if got.SuggestedFix == "" {
				t.Error("no suggested fix was produced")
			}
			if err := got.Validate(); err != nil {
				t.Errorf("the baseline produced an invalid explanation: %v", err)
			}
		})
	}
}

// The rule read the container's actual status field. That is stronger evidence
// than keyword matching over prose, so the rule wins — but the disagreement
// lowers confidence rather than being hidden.
func TestOfflineDefersToTheRuleButLowersConfidence(t *testing.T) {
	provider := NewOffline()

	agreeing, _ := provider.Explain(context.Background(), Request{
		Category: "OOMKilled",
		Evidence: []string{"FATAL java.lang.OutOfMemoryError: Java heap space"},
	})
	disagreeing, _ := provider.Explain(context.Background(), Request{
		Category: "OOMKilled",
		Evidence: []string{"Readiness probe failed: connection refused"},
	})

	if disagreeing.Category != detector.OOMKilled {
		t.Errorf("the rule's category was overridden: %s", disagreeing.Category)
	}
	if disagreeing.Confidence >= agreeing.Confidence {
		t.Errorf("disagreement did not lower confidence: %v vs %v",
			disagreeing.Confidence, agreeing.Confidence)
	}
}

// The baseline lifts its citations out of the evidence, so it cannot fabricate
// one. That is a property of the mechanism, not an achievement — and this test
// pins it, because the number is reported next to a real model's.
func TestOfflineCitationsAlwaysComeFromTheEvidence(t *testing.T) {
	evidence := []string{
		"INFO  starting payment-api 1.4.2",
		"FATAL java.lang.OutOfMemoryError: Java heap space",
	}

	got, err := NewOffline().Explain(context.Background(), Request{
		Category: "OOMKilled", Evidence: evidence,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CitedEvidence) == 0 {
		t.Fatal("expected at least one citation")
	}

	allowed := make(map[string]bool, len(evidence))
	for _, line := range evidence {
		allowed[line] = true
	}
	for _, cited := range got.CitedEvidence {
		if !allowed[cited] {
			t.Errorf("citation %q is not in the evidence", cited)
		}
	}
}

// With nothing to go on, low confidence is the honest answer.
func TestOfflineWithNoEvidence(t *testing.T) {
	got, err := NewOffline().Explain(context.Background(), Request{Category: "OOMKilled"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Confidence > 0.5 {
		t.Errorf("confidence = %v with no evidence at all", got.Confidence)
	}
	if len(got.CitedEvidence) != 0 {
		t.Errorf("citations were produced from nothing: %v", got.CitedEvidence)
	}
}

// The explanation should say something about this workload, not only about the
// category in general.
func TestOfflineLiftsASpecificFact(t *testing.T) {
	got, err := NewOffline().Explain(context.Background(), Request{
		Category: "OOMKilled",
		Context:  "  memory limit 512Mi\n  1 | FATAL java.lang.OutOfMemoryError",
		Evidence: []string{"FATAL java.lang.OutOfMemoryError"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Summary, "512Mi") {
		t.Errorf("the specific limit did not reach the explanation:\n%s", got.Summary)
	}
}
