package explanation

import (
	"strings"
	"testing"
)

func sampleEvidence() Evidence {
	return Evidence{
		Logs: []string{
			"INFO  starting payment-api 1.4.2",
			"WARN  heap usage at 91% of 512Mi",
			"FATAL java.lang.OutOfMemoryError: Java heap space",
			"        at com.acme.payments.CacheWarmer.load(CacheWarmer.java:64)",
		},
		Events: []string{
			"Back-off restarting failed container api in pod payment-api-7d9f",
		},
	}
}

func TestVerifyAcceptsLiteralQuotes(t *testing.T) {
	verified, rejected := Verify([]string{
		"FATAL java.lang.OutOfMemoryError: Java heap space",
	}, sampleEvidence())

	if len(rejected) != 0 {
		t.Fatalf("a literal quote was rejected: %v", rejected)
	}
	if len(verified) != 1 {
		t.Fatalf("got %d citations, want 1", len(verified))
	}
	if verified[0].LineNumber != 3 {
		t.Errorf("line number = %d, want 3", verified[0].LineNumber)
	}
	if verified[0].Source != SourceLog {
		t.Errorf("source = %q, want log", verified[0].Source)
	}
}

// Quoting the meaningful fragment of a long line is legitimate and still fully
// checkable.
func TestVerifyAcceptsAFragmentOfALine(t *testing.T) {
	verified, rejected := Verify([]string{"java.lang.OutOfMemoryError"}, sampleEvidence())

	if len(rejected) != 0 {
		t.Fatalf("a real fragment was rejected: %v", rejected)
	}
	if len(verified) != 1 || verified[0].LineNumber != 3 {
		t.Errorf("fragment did not resolve to its line: %+v", verified)
	}
}

// This is the case the whole product's credibility rests on.
func TestVerifyRejectsInventedQuotes(t *testing.T) {
	verified, rejected := Verify([]string{
		"FATAL java.lang.OutOfMemoryError: Java heap space",
		"ERROR connection pool exhausted after 30s",
	}, sampleEvidence())

	if len(verified) != 1 {
		t.Fatalf("got %d verified, want 1", len(verified))
	}
	if len(rejected) != 1 {
		t.Fatalf("got %d rejected, want the invented line rejected", len(rejected))
	}
	if !strings.Contains(rejected[0], "connection pool") {
		t.Errorf("the wrong citation was rejected: %q", rejected[0])
	}
}

func TestVerifyResolvesEventMessages(t *testing.T) {
	verified, rejected := Verify([]string{
		"Back-off restarting failed container api",
	}, sampleEvidence())

	if len(rejected) != 0 {
		t.Fatalf("an event quote was rejected: %v", rejected)
	}
	if verified[0].Source != SourceEvent {
		t.Errorf("source = %q, want event", verified[0].Source)
	}
	// Event messages have no line number to highlight.
	if verified[0].LineNumber != 0 {
		t.Errorf("an event citation got line number %d", verified[0].LineNumber)
	}
}

// A model that cites "error" has technically quoted the log and told the reader
// nothing — and a fragment that short matches almost anything, which would turn
// verification into a formality that passes everything.
func TestVerifyRejectsUselesslyShortQuotes(t *testing.T) {
	_, rejected := Verify([]string{"INFO", "heap", "at"}, sampleEvidence())

	if len(rejected) != 3 {
		t.Errorf("got %d rejected, want all three short fragments rejected", len(rejected))
	}
}

// A check that folds case or collapses whitespace is a check that proves
// nothing about the content.
func TestVerifyIsLiteralAboutCharacters(t *testing.T) {
	_, rejected := Verify([]string{
		"fatal java.lang.outofmemoryerror: java heap space",
	}, sampleEvidence())

	if len(rejected) != 1 {
		t.Errorf("a case-folded quote was accepted as literal")
	}
}

// Surrounding whitespace is not part of the claim.
func TestVerifyTrimsOuterWhitespaceOnly(t *testing.T) {
	verified, rejected := Verify([]string{
		"   FATAL java.lang.OutOfMemoryError: Java heap space   ",
	}, sampleEvidence())

	if len(rejected) != 0 || len(verified) != 1 {
		t.Fatalf("padding broke the match: verified=%v rejected=%v", verified, rejected)
	}
}

func TestVerifyDeduplicatesAndSorts(t *testing.T) {
	verified, _ := Verify([]string{
		"at com.acme.payments.CacheWarmer.load",
		"FATAL java.lang.OutOfMemoryError: Java heap space",
		"FATAL java.lang.OutOfMemoryError: Java heap space",
		"WARN  heap usage at 91% of 512Mi",
	}, sampleEvidence())

	if len(verified) != 3 {
		t.Fatalf("got %d citations, want 3 after deduplication", len(verified))
	}
	for i := 1; i < len(verified); i++ {
		if verified[i-1].LineNumber > verified[i].LineNumber {
			t.Errorf("citations are not in line order: %+v", verified)
		}
	}
}

func TestVerifyWithNothingClaimed(t *testing.T) {
	verified, rejected := Verify(nil, sampleEvidence())
	if len(verified) != 0 || len(rejected) != 0 {
		t.Errorf("empty input produced output: %v / %v", verified, rejected)
	}
	if got := Accuracy(verified, rejected); got != 0 {
		t.Errorf("Accuracy() = %v with nothing claimed, want 0", got)
	}
}

// With no evidence at all, nothing can be verified — every quote must be
// rejected rather than passing by default.
func TestVerifyWithNoEvidenceRejectsEverything(t *testing.T) {
	verified, rejected := Verify([]string{
		"FATAL java.lang.OutOfMemoryError: Java heap space",
	}, Evidence{})

	if len(verified) != 0 {
		t.Errorf("a quote was verified against empty evidence: %+v", verified)
	}
	if len(rejected) != 1 {
		t.Errorf("got %d rejected, want 1", len(rejected))
	}
}

func TestAccuracy(t *testing.T) {
	verified := []Citation{{Text: "a"}, {Text: "b"}, {Text: "c"}}
	if got := Accuracy(verified, []string{"d"}); got != 0.75 {
		t.Errorf("Accuracy() = %v, want 0.75", got)
	}
	if got := Accuracy(verified, nil); got != 1 {
		t.Errorf("Accuracy() = %v, want 1", got)
	}
}

func TestLineNumbers(t *testing.T) {
	got := LineNumbers([]Citation{
		{LineNumber: 3}, {LineNumber: 0, Source: SourceEvent}, {LineNumber: 1}, {LineNumber: 3},
	})

	if len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Errorf("LineNumbers() = %v, want [1 3] with events and duplicates dropped", got)
	}
}
