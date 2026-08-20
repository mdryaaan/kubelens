// Package detector turns raw cluster changes into named incidents.
package detector

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mdryaaan/kubelens/internal/watcher"
)

// Category names a Kubernetes failure pattern.
//
// The set is closed and small on purpose. Every category here has a distinct
// cause and a distinct fix, which is what makes the label worth showing to a
// human — a taxonomy where two entries lead to the same action is a taxonomy
// with one entry too many.
type Category string

// The six patterns kubelens detects.
const (
	CrashLoopBackOff  Category = "CrashLoopBackOff"
	OOMKilled         Category = "OOMKilled"
	ImagePullBackOff  Category = "ImagePullBackOff"
	ProbeFailure      Category = "ProbeFailure"
	PendingTimeout    Category = "PendingTimeout"
	DeploymentFailure Category = "DeploymentFailure"
)

// AllCategories lists every category in report order.
func AllCategories() []Category {
	return []Category{
		CrashLoopBackOff, OOMKilled, ImagePullBackOff,
		ProbeFailure, PendingTimeout, DeploymentFailure,
	}
}

// Valid reports whether c is a known category.
func (c Category) Valid() bool {
	for _, known := range AllCategories() {
		if c == known {
			return true
		}
	}
	return false
}

func (c Category) String() string { return string(c) }

// ParseCategory converts a string to a Category, tolerating case and spacing
// because this parses both CLI flags and model output.
func ParseCategory(in string) (Category, error) {
	normalised := strings.ToLower(strings.TrimSpace(in))
	normalised = strings.NewReplacer("_", "", "-", "", " ", "").Replace(normalised)

	for _, known := range AllCategories() {
		if strings.ToLower(string(known)) == normalised {
			return known, nil
		}
	}
	return "", fmt.Errorf("unknown category %q", in)
}

// Severity is how urgently an incident needs attention.
type Severity string

// The severity ladder.
const (
	Critical Severity = "critical"
	Warning  Severity = "warning"
	Info     Severity = "info"
)

// Rank orders severities; higher is worse.
func (s Severity) Rank() int {
	switch s {
	case Critical:
		return 3
	case Warning:
		return 2
	case Info:
		return 1
	}
	return 0
}

// Valid reports whether s is a known severity.
func (s Severity) Valid() bool { return s.Rank() > 0 }

// Incident is one detected problem, before any explanation is attached.
type Incident struct {
	ID        string   `json:"id"`
	Category  Category `json:"category"`
	Severity  Severity `json:"severity"`
	Namespace string   `json:"namespace"`
	// Resource is the object that is unhealthy, e.g. "pod/payment-api-7d9f".
	Resource  string `json:"resource"`
	Container string `json:"container,omitempty"`
	// Title is a one-line summary safe to show in a list.
	Title string `json:"title"`
	// Detail is what the rule actually observed, in the rule's own words. It is
	// never model output — an explanation is attached separately so a reader can
	// always tell the deterministic finding from the generated prose.
	Detail     string    `json:"detail"`
	DetectedAt time.Time `json:"detected_at"`
	// FirstSeen is when the underlying condition started, which can be well
	// before kubelens noticed it.
	FirstSeen time.Time `json:"first_seen"`
	Count     int       `json:"count"`
	Resolved  bool      `json:"resolved"`
	// PreExisting marks a condition that was already true when kubelens started
	// watching. Detection latency is meaningless for those — the delay measures
	// when the process launched, not how fast it noticed — so they are excluded
	// from the mean rather than inflating it.
	PreExisting bool `json:"pre_existing"`
	// Fingerprint identifies the underlying condition across repeats, so the
	// same crash loop is one incident with a rising count rather than four
	// hundred rows.
	Fingerprint string `json:"fingerprint"`

	// lastEmitted drives the cooldown. Unexported so it never reaches the API
	// or the store — it is bookkeeping about kubelens, not about the cluster.
	lastEmitted time.Time
}

// Fingerprint builds a stable identity for a condition.
func Fingerprint(category Category, namespace, resource, container string) string {
	sum := sha256.Sum256([]byte(strings.Join(
		[]string{string(category), namespace, resource, container}, "|")))
	return hex.EncodeToString(sum[:])[:16]
}

// Rule inspects one watch event and reports an incident if it matches.
//
// Returning nil is the overwhelmingly common case — most events in a healthy
// cluster are uninteresting — so rules are written to bail out early and cheaply.
type Rule interface {
	// Category is the failure pattern this rule detects.
	Category() Category
	// Describe explains what the rule looks for, for the docs and the CLI.
	Describe() string
	// Detect returns an incident, or nil when the event does not match.
	Detect(event watcher.WatchEvent) *Incident
}

// Options configures the engine.
type Options struct {
	// Cooldown is how long the same fingerprint is suppressed after firing.
	// Without it a crash loop emits an incident on every informer update, which
	// is both useless to a human and expensive if each one costs an inference.
	Cooldown time.Duration
	// PendingThreshold is how long a pod may sit Pending before it counts.
	PendingThreshold time.Duration
	// Now is injectable so tests do not depend on wall-clock time.
	Now func() time.Time
}

// DefaultOptions returns the settings used when nothing overrides them.
func DefaultOptions() Options {
	return Options{
		Cooldown:         5 * time.Minute,
		PendingThreshold: 5 * time.Minute,
		Now:              func() time.Time { return time.Now().UTC() },
	}
}

// Engine runs every rule over the event stream and deduplicates the results.
type Engine struct {
	rules []Rule
	opts  Options

	mu   sync.Mutex
	seen map[string]*Incident
	// startedAt is when this engine began watching, used to tell a condition
	// kubelens saw begin from one it inherited.
	startedAt time.Time
}

// NewEngine builds an engine with the default rule set.
func NewEngine(opts Options) *Engine {
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.Cooldown <= 0 {
		opts.Cooldown = DefaultOptions().Cooldown
	}
	if opts.PendingThreshold <= 0 {
		opts.PendingThreshold = DefaultOptions().PendingThreshold
	}

	return &Engine{
		rules:     DefaultRules(opts),
		seen:      make(map[string]*Incident),
		opts:      opts,
		startedAt: opts.Now(),
	}
}

// NewEngineWith builds an engine from an explicit rule set, used by tests.
func NewEngineWith(opts Options, rules ...Rule) *Engine {
	engine := NewEngine(opts)
	engine.rules = rules
	return engine
}

// DefaultRules returns every rule kubelens ships with.
func DefaultRules(opts Options) []Rule {
	return []Rule{
		NewOOMKilledRule(),
		NewCrashLoopRule(),
		NewImagePullRule(),
		NewProbeFailureRule(),
		NewPendingTimeoutRule(opts.PendingThreshold, opts.Now),
		NewDeploymentFailureRule(),
	}
}

// Rules exposes the configured rules, for the reference table.
func (e *Engine) Rules() []Rule { return e.rules }

// Process runs every rule over one event and returns the incidents worth
// surfacing — at most one per rule, and none that are still in cooldown.
//
// Rules are ordered most-specific first, and the first match wins for a given
// resource. An OOMKilled container is also, a moment later, a CrashLoopBackOff;
// reporting both would be technically true and practically useless, because the
// memory limit is the thing to fix.
func (e *Engine) Process(event watcher.WatchEvent) []Incident {
	e.mu.Lock()
	defer e.mu.Unlock()

	var out []Incident
	for _, rule := range e.rules {
		incident := rule.Detect(event)
		if incident == nil {
			continue
		}

		if incident.Fingerprint == "" {
			incident.Fingerprint = Fingerprint(
				incident.Category, incident.Namespace, incident.Resource, incident.Container)
		}
		now := e.opts.Now()
		if incident.DetectedAt.IsZero() {
			incident.DetectedAt = now
		}
		if incident.FirstSeen.IsZero() {
			incident.FirstSeen = incident.DetectedAt
		}
		if incident.FirstSeen.Before(e.startedAt) {
			incident.PreExisting = true
		}

		previous, repeat := e.seen[incident.Fingerprint]
		if repeat {
			previous.Count++
			previous.DetectedAt = now
			if now.Sub(previous.lastEmitted) < e.opts.Cooldown {
				continue
			}
			previous.lastEmitted = now
			incident.Count = previous.Count
			incident.FirstSeen = previous.FirstSeen
			incident.ID = previous.ID
		} else {
			incident.Count = 1
			incident.ID = incident.Fingerprint
			stored := *incident
			stored.lastEmitted = now
			e.seen[incident.Fingerprint] = &stored
		}

		out = append(out, *incident)

		// One incident per resource per event: the loop stops at the first
		// match rather than reporting every rule that happens to be true.
		break
	}

	return out
}

// Active returns the incidents the engine currently considers open, worst
// first, for the health snapshot the dashboard renders.
func (e *Engine) Active() []Incident {
	e.mu.Lock()
	defer e.mu.Unlock()

	out := make([]Incident, 0, len(e.seen))
	for _, incident := range e.seen {
		if !incident.Resolved {
			out = append(out, *incident)
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity.Rank() != out[j].Severity.Rank() {
			return out[i].Severity.Rank() > out[j].Severity.Rank()
		}
		return out[i].DetectedAt.After(out[j].DetectedAt)
	})
	return out
}

// Resolve marks a fingerprint healthy again, which is what lets the dashboard
// show a cluster recovering rather than only ever accumulating problems.
func (e *Engine) Resolve(fingerprint string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	incident, ok := e.seen[fingerprint]
	if !ok || incident.Resolved {
		return false
	}
	incident.Resolved = true
	return true
}
