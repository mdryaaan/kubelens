package explanation

import (
	"sort"
	"strings"
)

// Citation is one claim the explanation makes, resolved against the evidence.
type Citation struct {
	// Text is what the model quoted, exactly as it wrote it.
	Text string `json:"text"`
	// LineNumber is the log line it resolved to, or 0 when it matched an event
	// message instead. The dashboard uses this to highlight the source line.
	LineNumber int `json:"line_number,omitempty"`
	// Source says which evidence section the quote came from.
	Source string `json:"source"`
}

// Citation sources.
const (
	SourceLog   = "log"
	SourceEvent = "event"
)

// Evidence is the set of lines a citation may legitimately quote.
//
// It is built from the same IncidentContext the model was shown, so the
// allowlist and the prompt can never disagree about what was available.
type Evidence struct {
	// Logs are the numbered container lines, in order.
	Logs []string
	// Events are the Kubernetes event messages.
	Events []string
}

// minCitationRunes is the shortest quote treated as evidence.
//
// A model that cites "error" has technically quoted the log, and has told the
// reader nothing. Short fragments also match almost anything, which would turn
// verification into a formality that passes everything.
const minCitationRunes = 12

// Verify splits claimed citations into the ones the evidence supports and the
// ones it does not.
//
// This is the mechanism the whole product's credibility rests on. An
// explanation that quotes a log line reads as authoritative, so a fabricated
// quote manufactures confidence in an analysis nothing supports — worse than no
// quote at all. The prompt asks the model not to invent; this is what makes it
// true regardless of whether the model complied.
//
// A quote counts as verified when it appears literally inside an evidence line.
// Substring rather than equality, because quoting the meaningful fragment of a
// 300-character log line is legitimate and still fully checkable. Everything is
// compared after trimming outer whitespace only — no case folding, no
// normalisation — because a check that is lenient about the characters is a
// check that proves nothing about the content.
func Verify(claimed []string, evidence Evidence) (verified []Citation, rejected []string) {
	seen := make(map[string]bool, len(claimed))

	for _, raw := range claimed {
		quote := strings.TrimSpace(raw)
		if quote == "" || seen[quote] {
			continue
		}
		seen[quote] = true

		if len([]rune(quote)) < minCitationRunes {
			rejected = append(rejected, raw)
			continue
		}

		if number, ok := matchLine(quote, evidence.Logs); ok {
			verified = append(verified, Citation{
				Text: quote, LineNumber: number, Source: SourceLog,
			})
			continue
		}
		if _, ok := matchLine(quote, evidence.Events); ok {
			verified = append(verified, Citation{Text: quote, Source: SourceEvent})
			continue
		}

		rejected = append(rejected, raw)
	}

	sort.SliceStable(verified, func(i, j int) bool {
		return verified[i].LineNumber < verified[j].LineNumber
	})
	return verified, rejected
}

// matchLine finds the 1-based index of the first line containing the quote.
func matchLine(quote string, lines []string) (int, bool) {
	for i, line := range lines {
		if strings.Contains(strings.TrimSpace(line), quote) {
			return i + 1, true
		}
	}
	return 0, false
}

// Accuracy is the share of claimed citations that the evidence supported.
//
// Reported alongside every explanation rather than aggregated away, because a
// model that cites well on average can still fabricate on the one incident
// somebody is reading right now.
func Accuracy(verified []Citation, rejected []string) float64 {
	total := len(verified) + len(rejected)
	if total == 0 {
		return 0
	}
	return float64(len(verified)) / float64(total)
}

// LineNumbers returns the log lines the verified citations resolved to, for the
// dashboard to highlight.
func LineNumbers(citations []Citation) []int {
	out := make([]int, 0, len(citations))
	seen := make(map[int]bool, len(citations))

	for _, citation := range citations {
		if citation.LineNumber <= 0 || seen[citation.LineNumber] {
			continue
		}
		seen[citation.LineNumber] = true
		out = append(out, citation.LineNumber)
	}

	sort.Ints(out)
	return out
}
