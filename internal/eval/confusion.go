package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/mdryaaan/kubelens/internal/detector"
)

// short abbreviates a category so a confusion matrix fits a terminal column.
func short(category detector.Category) string {
	switch category {
	case detector.CrashLoopBackOff:
		return "crash"
	case detector.OOMKilled:
		return "oom"
	case detector.ImagePullBackOff:
		return "image"
	case detector.ProbeFailure:
		return "probe"
	case detector.PendingTimeout:
		return "pending"
	case detector.DeploymentFailure:
		return "rollout"
	}
	return string(category)
}

// WriteJSON writes the machine-readable result.
func WriteJSON(w io.Writer, result Result) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("encoding eval result: %w", err)
	}
	return nil
}

// WriteText renders the result for a terminal.
func WriteText(w io.Writer, result Result) error {
	var b strings.Builder

	// The disclaimer comes before any number, because everything below it looks
	// like a model's score and is not.
	if result.Disclaimer != "" {
		fmt.Fprintf(&b, "%s\n\n", result.Disclaimer)
	}

	fmt.Fprintf(&b, "corpus    %s (%d cases)\n", result.Corpus, result.Cases)
	fmt.Fprintf(&b, "provider  %s\n", result.Provider)
	fmt.Fprintf(&b, "model     %s\n", result.Model)
	fmt.Fprintf(&b, "elapsed   %s\n\n", result.Duration.Round(1e6))

	s := result.Scores
	fmt.Fprintf(&b, "accuracy              %.1f%% (%d/%d)\n", s.Accuracy*100, s.Correct, s.Cases)
	fmt.Fprintf(&b, "macro F1              %.3f\n", s.MacroF1)
	fmt.Fprintf(&b, "mean confidence       %.2f when right, %.2f when wrong\n",
		s.MeanConfidenceRight, s.MeanConfidenceWrong)
	if s.TotalCitations > 0 {
		fmt.Fprintf(&b, "citations verified    %.1f%% (%d of %d)\n",
			s.CitationAccuracy()*100, s.TotalCitations-s.Fabricated, s.TotalCitations)
	}
	fmt.Fprintf(&b, "fabricated citations  %d\n", s.Fabricated)
	fmt.Fprintf(&b, "ungrounded cases      %d (explained without citing anything)\n", s.Ungrounded)
	if s.MeanLatencyMS > 0 {
		fmt.Fprintf(&b, "mean latency          %dms\n", s.MeanLatencyMS)
	}
	fmt.Fprintln(&b)

	fmt.Fprintf(&b, "%-20s %8s %10s %8s %8s\n", "category", "support", "precision", "recall", "f1")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 58))
	for _, score := range s.PerCategory {
		if score.Support == 0 && score.Predicted == 0 {
			continue
		}
		fmt.Fprintf(&b, "%-20s %8d %10.3f %8.3f %8.3f\n",
			score.Category, score.Support, score.Precision, score.Recall, score.F1)
	}
	fmt.Fprintln(&b)

	writeConfusionText(&b, s)

	if len(s.Failures) > 0 {
		fmt.Fprintf(&b, "\n%d case(s) could not be evaluated:\n", len(s.Failures))
		for _, failure := range s.Failures {
			fmt.Fprintf(&b, "  %s\n", failure)
		}
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// writeConfusionText renders the matrix of labelled category against predicted.
func writeConfusionText(b *strings.Builder, s Scores) {
	categories := detector.AllCategories()

	fmt.Fprintf(b, "confusion matrix (rows = labelled, columns = predicted)\n")
	fmt.Fprintf(b, "%-20s", "")
	for _, column := range categories {
		fmt.Fprintf(b, "%8s", short(column))
	}
	fmt.Fprintln(b)
	fmt.Fprintf(b, "%s\n", strings.Repeat("-", 20+8*len(categories)))

	for _, row := range categories {
		fmt.Fprintf(b, "%-20s", short(row))
		for _, column := range categories {
			count := s.Confusion[row][column]
			if count == 0 {
				fmt.Fprintf(b, "%8s", ".")
				continue
			}
			fmt.Fprintf(b, "%8d", count)
		}
		fmt.Fprintln(b)
	}
}

// WriteMarkdown renders the result as the table that goes in the README.
func WriteMarkdown(w io.Writer, result Result) error {
	var b strings.Builder

	fmt.Fprintf(&b, "## Evaluation\n\n")
	if result.Disclaimer != "" {
		fmt.Fprintf(&b, "> **%s**\n\n", result.Disclaimer)
	}

	fmt.Fprintf(&b, "Corpus `%s` · %d labelled incidents · provider `%s` (`%s`) · %s\n\n",
		result.Corpus, result.Cases, result.Provider, result.Model, result.Duration.Round(1e6))

	s := result.Scores
	fmt.Fprintf(&b, "| Metric | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| Accuracy | **%.1f%%** (%d/%d) |\n", s.Accuracy*100, s.Correct, s.Cases)
	fmt.Fprintf(&b, "| Macro F1 | **%.3f** |\n", s.MacroF1)
	fmt.Fprintf(&b, "| Mean confidence when right | %.2f |\n", s.MeanConfidenceRight)
	fmt.Fprintf(&b, "| Mean confidence when wrong | %.2f |\n", s.MeanConfidenceWrong)
	if s.TotalCitations > 0 {
		fmt.Fprintf(&b, "| Citations verified against the evidence | %.1f%% (%d/%d) |\n",
			s.CitationAccuracy()*100, s.TotalCitations-s.Fabricated, s.TotalCitations)
	}
	fmt.Fprintf(&b, "| Fabricated citations | **%d** |\n", s.Fabricated)
	fmt.Fprintf(&b, "| Explained without citing anything | %d |\n\n", s.Ungrounded)

	fmt.Fprintf(&b, "### Per category\n\n")
	fmt.Fprintf(&b, "| Category | Support | Precision | Recall | F1 |\n|---|---:|---:|---:|---:|\n")
	for _, score := range s.PerCategory {
		if score.Support == 0 && score.Predicted == 0 {
			continue
		}
		fmt.Fprintf(&b, "| `%s` | %d | %.2f | %.2f | %.2f |\n",
			score.Category, score.Support, score.Precision, score.Recall, score.F1)
	}
	fmt.Fprintln(&b)

	categories := detector.AllCategories()
	fmt.Fprintf(&b, "### Confusion matrix\n\nRows are the labelled category, columns what was predicted.\n\n")
	fmt.Fprintf(&b, "| labelled \\ predicted |")
	for _, column := range categories {
		fmt.Fprintf(&b, " %s |", short(column))
	}
	fmt.Fprintf(&b, "\n|---|")
	for range categories {
		fmt.Fprintf(&b, "---|")
	}
	fmt.Fprintln(&b)

	for _, row := range categories {
		fmt.Fprintf(&b, "| **%s** |", short(row))
		for _, column := range categories {
			fmt.Fprintf(&b, " %d |", s.Confusion[row][column])
		}
		fmt.Fprintln(&b)
	}
	fmt.Fprintln(&b)

	_, err := io.WriteString(w, b.String())
	return err
}
