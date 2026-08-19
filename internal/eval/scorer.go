package eval

import (
	"fmt"
	"sort"
	"time"

	"github.com/mdryaaan/kubelens/internal/detector"
)

// CategoryScore is precision and recall for one category.
type CategoryScore struct {
	Category  detector.Category `json:"category"`
	Support   int               `json:"support"`
	Predicted int               `json:"predicted"`
	TP        int               `json:"true_positives"`
	FP        int               `json:"false_positives"`
	FN        int               `json:"false_negatives"`
	Precision float64           `json:"precision"`
	Recall    float64           `json:"recall"`
	F1        float64           `json:"f1"`
}

// CaseResult is one scored case.
type CaseResult struct {
	CaseID     string            `json:"case_id"`
	Expected   detector.Category `json:"expected"`
	Predicted  detector.Category `json:"predicted"`
	Correct    bool              `json:"correct"`
	Confidence float64           `json:"confidence"`
	Cited      int               `json:"cited"`
	Fabricated int               `json:"fabricated"`
	LatencyMS  int64             `json:"latency_ms"`
	Error      string            `json:"error,omitempty"`
}

// Scores is the full evaluation result.
type Scores struct {
	Cases    int     `json:"cases"`
	Correct  int     `json:"correct"`
	Accuracy float64 `json:"accuracy"`
	// MacroF1 weights every category equally, so a rare category the provider
	// never gets right cannot be hidden behind a common one it always does.
	MacroF1 float64 `json:"macro_f1"`

	// MeanConfidenceRight and MeanConfidenceWrong measure calibration. A
	// provider equally confident when right and wrong is one whose confidence
	// carries no information, however high its accuracy.
	MeanConfidenceRight float64 `json:"mean_confidence_right"`
	MeanConfidenceWrong float64 `json:"mean_confidence_wrong"`

	// TotalCitations and Fabricated measure whether the analysis is grounded.
	// A correct category supported by an invented quote is not a success.
	TotalCitations int `json:"total_citations"`
	Fabricated     int `json:"fabricated_citations"`
	Ungrounded     int `json:"ungrounded_cases"`

	MeanLatencyMS int64 `json:"mean_latency_ms"`

	PerCategory []CategoryScore                                 `json:"per_category"`
	Confusion   map[detector.Category]map[detector.Category]int `json:"confusion"`
	Results     []CaseResult                                    `json:"results"`
	Failures    []string                                        `json:"failures,omitempty"`
}

// Score compares predictions against the corpus.
func Score(corpus Corpus, predictions []Prediction) Scores {
	byID := make(map[string]Prediction, len(predictions))
	for _, prediction := range predictions {
		byID[prediction.CaseID] = prediction
	}

	scores := Scores{
		Cases:     len(corpus.Cases),
		Confusion: newConfusion(),
	}

	counts := map[detector.Category]*CategoryScore{}
	category := func(id detector.Category) *CategoryScore {
		if _, ok := counts[id]; !ok {
			counts[id] = &CategoryScore{Category: id}
		}
		return counts[id]
	}
	for _, known := range detector.AllCategories() {
		category(known)
	}

	var confidenceRight, confidenceWrong float64
	var rightCount, wrongCount int
	var latencyTotal time.Duration
	var latencyCount int

	for _, tc := range corpus.Cases {
		prediction := byID[tc.ID]

		result := CaseResult{
			CaseID:     tc.ID,
			Expected:   tc.Category,
			Predicted:  prediction.Predicted,
			Confidence: prediction.Confidence,
			Cited:      prediction.Cited,
			Fabricated: prediction.Fabricated,
			LatencyMS:  prediction.Latency.Milliseconds(),
		}

		if prediction.Err != nil {
			result.Error = prediction.Err.Error()
			scores.Failures = append(scores.Failures, fmt.Sprintf("%s: %v", tc.ID, prediction.Err))
			// A case that failed still counts against the score: a provider
			// that errors on a third of the corpus has not earned a clean
			// accuracy number on the rest.
			category(tc.Category).Support++
			category(tc.Category).FN++
			scores.Results = append(scores.Results, result)
			continue
		}

		if prediction.Latency > 0 {
			latencyTotal += prediction.Latency
			latencyCount++
		}

		scores.TotalCitations += prediction.Cited + prediction.Fabricated
		scores.Fabricated += prediction.Fabricated
		if prediction.Cited == 0 {
			scores.Ungrounded++
		}

		result.Correct = prediction.Predicted == tc.Category
		category(tc.Category).Support++
		category(prediction.Predicted).Predicted++

		if result.Correct {
			scores.Correct++
			category(tc.Category).TP++
			confidenceRight += prediction.Confidence
			rightCount++
		} else {
			category(tc.Category).FN++
			category(prediction.Predicted).FP++
			confidenceWrong += prediction.Confidence
			wrongCount++
		}

		if _, ok := scores.Confusion[tc.Category]; ok {
			scores.Confusion[tc.Category][prediction.Predicted]++
		}

		scores.Results = append(scores.Results, result)
	}

	for _, score := range counts {
		score.Precision = ratio(score.TP, score.TP+score.FP)
		score.Recall = ratio(score.TP, score.TP+score.FN)
		score.F1 = harmonic(score.Precision, score.Recall)
		scores.PerCategory = append(scores.PerCategory, *score)
	}
	sort.Slice(scores.PerCategory, func(i, j int) bool {
		return scores.PerCategory[i].Category < scores.PerCategory[j].Category
	})

	if scores.Cases > 0 {
		scores.Accuracy = float64(scores.Correct) / float64(scores.Cases)
	}
	scores.MacroF1 = macroF1(scores.PerCategory)
	scores.MeanConfidenceRight = mean(confidenceRight, rightCount)
	scores.MeanConfidenceWrong = mean(confidenceWrong, wrongCount)
	if latencyCount > 0 {
		scores.MeanLatencyMS = (latencyTotal / time.Duration(latencyCount)).Milliseconds()
	}

	return scores
}

// CitationAccuracy is the share of citations that the evidence supported.
func (s Scores) CitationAccuracy() float64 {
	if s.TotalCitations == 0 {
		return 0
	}
	return float64(s.TotalCitations-s.Fabricated) / float64(s.TotalCitations)
}

// Calibrated reports whether confidence carries information — higher when right
// than when wrong. A provider that is equally sure either way is telling you
// nothing with its confidence field, however good its accuracy.
func (s Scores) Calibrated() bool {
	return s.MeanConfidenceRight > s.MeanConfidenceWrong
}

// Summary renders a one-line result.
func (s Scores) Summary() string {
	return fmt.Sprintf("%d/%d correct (%.1f%%), macro F1 %.3f, %d fabricated citation(s)",
		s.Correct, s.Cases, s.Accuracy*100, s.MacroF1, s.Fabricated)
}

func newConfusion() map[detector.Category]map[detector.Category]int {
	matrix := make(map[detector.Category]map[detector.Category]int)
	for _, row := range detector.AllCategories() {
		matrix[row] = make(map[detector.Category]int)
		for _, column := range detector.AllCategories() {
			matrix[row][column] = 0
		}
	}
	return matrix
}

func macroF1(scores []CategoryScore) float64 {
	var total float64
	var counted int
	for _, score := range scores {
		// Categories with no examples in the corpus would otherwise drag the
		// average towards zero for a reason that says nothing about the
		// provider.
		if score.Support == 0 && score.Predicted == 0 {
			continue
		}
		total += score.F1
		counted++
	}
	if counted == 0 {
		return 0
	}
	return total / float64(counted)
}

func ratio(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}

func harmonic(precision, recall float64) float64 {
	if precision+recall == 0 {
		return 0
	}
	return 2 * precision * recall / (precision + recall)
}

func mean(total float64, count int) float64 {
	if count == 0 {
		return 0
	}
	return total / float64(count)
}
