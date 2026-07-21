package llm

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mdryaaan/kubelens/internal/detector"
)

// Explanation is a validated root-cause analysis of one incident.
type Explanation struct {
	// Category is the model's own classification. It is kept separate from the
	// rule's category so a disagreement is visible rather than silently
	// overwriting a deterministic result.
	Category   detector.Category `json:"category"`
	Confidence float64           `json:"confidence"`
	Summary    string            `json:"explanation"`
	// CitedEvidence holds lines the model says support its analysis. They are
	// unverified at this point — verification happens in the explanation
	// package, against the context the model was actually shown.
	CitedEvidence []string `json:"cited_evidence"`
	SuggestedFix  string   `json:"suggested_fix"`
}

// rawExplanation mirrors the JSON contract exactly. It is a separate type from
// Explanation so a model cannot set fields kubelens computes itself.
type rawExplanation struct {
	Category      string   `json:"category"`
	Confidence    float64  `json:"confidence"`
	Explanation   string   `json:"explanation"`
	CitedEvidence []string `json:"cited_evidence"`
	SuggestedFix  string   `json:"suggested_fix"`
}

// SchemaJSON is the contract shown to the model in the prompt.
const SchemaJSON = `{
  "category": "one of: CrashLoopBackOff | OOMKilled | ImagePullBackOff | ProbeFailure | PendingTimeout | DeploymentFailure",
  "confidence": 0.0,
  "explanation": "two or three sentences in plain English, naming the specific cause",
  "cited_evidence": ["exact log lines or event messages copied verbatim from the context"],
  "suggested_fix": "one concrete action a maintainer can take"
}`

// Validate checks an explanation against the schema's invariants.
func (e Explanation) Validate() error {
	if !e.Category.Valid() {
		return fmt.Errorf("%w: category %q is not one of the six known patterns",
			ErrMalformed, e.Category)
	}
	if e.Confidence < 0 || e.Confidence > 1 {
		return fmt.Errorf("%w: confidence %v is outside [0,1]", ErrMalformed, e.Confidence)
	}
	if strings.TrimSpace(e.Summary) == "" {
		return fmt.Errorf("%w: empty explanation", ErrMalformed)
	}
	return nil
}

// ParseExplanation extracts and validates an explanation from a raw response.
//
// Models wrap JSON in prose or fenced code blocks often enough that demanding a
// bare object would fail constantly, so the object is located inside the
// response rather than assumed to be the whole of it. Anything that still does
// not satisfy the schema is rejected: the caller retries once and then fails
// honestly rather than guessing.
func ParseExplanation(raw string) (Explanation, error) {
	payload, err := extractJSONObject(raw)
	if err != nil {
		return Explanation{}, err
	}

	var parsed rawExplanation
	if err := json.NewDecoder(strings.NewReader(payload)).Decode(&parsed); err != nil {
		return Explanation{}, fmt.Errorf("%w: %w", ErrMalformed, err)
	}

	category, err := detector.ParseCategory(parsed.Category)
	if err != nil {
		return Explanation{}, fmt.Errorf("%w: %w", ErrMalformed, err)
	}

	explanation := Explanation{
		Category:      category,
		Confidence:    parsed.Confidence,
		Summary:       strings.TrimSpace(parsed.Explanation),
		CitedEvidence: cleanCitations(parsed.CitedEvidence),
		SuggestedFix:  strings.TrimSpace(parsed.SuggestedFix),
	}

	if err := explanation.Validate(); err != nil {
		return Explanation{}, err
	}
	return explanation, nil
}

// cleanCitations trims whitespace and drops empties, without altering the text
// itself — a citation has to stay byte-comparable to the line it claims to
// quote, or the verification step becomes a fuzzy match that proves nothing.
func cleanCitations(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// extractJSONObject finds the first balanced top-level JSON object in s,
// tolerating markdown fences and surrounding commentary.
func extractJSONObject(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("%w: empty response", ErrMalformed)
	}

	if idx := strings.Index(s, "```"); idx >= 0 {
		rest := s[idx+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if end := strings.Index(rest, "```"); end >= 0 {
			rest = rest[:end]
		}
		s = strings.TrimSpace(rest)
	}

	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", fmt.Errorf("%w: no JSON object found", ErrMalformed)
	}

	depth := 0
	inString := false
	escaped := false

	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\' && inString:
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
			// structural characters inside strings are just text
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1], nil
			}
		}
	}

	return "", fmt.Errorf("%w: unbalanced JSON object", ErrMalformed)
}
