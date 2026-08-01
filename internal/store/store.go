// Package store persists incidents, their explanations, and cluster health.
package store

import (
	"time"

	kcontext "github.com/mdryaaan/kubelens/internal/context"
	"github.com/mdryaaan/kubelens/internal/detector"
	"github.com/mdryaaan/kubelens/internal/explanation"
)

// Record is one incident with everything known about it.
//
// The evidence travels with the incident rather than being re-fetched on
// demand, because the pod it came from will not exist tomorrow. An explanation
// whose citations cannot be resolved is an explanation nobody can check, and
// the whole point of citing was to make checking possible.
type Record struct {
	Incident    detector.Incident        `json:"incident"`
	Explanation *explanation.Explanation `json:"explanation,omitempty"`
	Evidence    Evidence                 `json:"evidence"`
}

// Evidence is the stored copy of what the explanation was allowed to cite.
type Evidence struct {
	Logs   []kcontext.LogLine     `json:"logs"`
	Events []kcontext.EventRecord `json:"events"`
	Spec   kcontext.ResourceSpec  `json:"spec"`
}

// HealthSample is one point on the cluster health chart.
type HealthSample struct {
	SampledAt     time.Time `json:"sampled_at"`
	TotalPods     int       `json:"total_pods"`
	UnhealthyPods int       `json:"unhealthy_pods"`
	OpenIncidents int       `json:"open_incidents"`
	Nodes         int       `json:"nodes"`
}

// Filter narrows an incident query.
type Filter struct {
	Category       string
	Severity       string
	Namespace      string
	UnresolvedOnly bool
	Since          time.Time
	Limit          int
	Offset         int
}

// Stats is the headline summary the overview page renders.
type Stats struct {
	TotalIncidents int            `json:"total_incidents"`
	OpenIncidents  int            `json:"open_incidents"`
	IncidentsToday int            `json:"incidents_today"`
	ByCategory     map[string]int `json:"by_category"`
	BySeverity     map[string]int `json:"by_severity"`
	Explained      int            `json:"explained"`
	// MeanTimeToDetectMS is measured from when the underlying condition
	// started to when kubelens raised it — the honest version of the number,
	// not the time from raising it to writing it down.
	MeanTimeToDetectMS int64 `json:"mean_time_to_detect_ms"`
	// FabricatedCitations counts explanations that quoted something absent from
	// the evidence. Surfaced rather than buried: it is the number that says
	// whether the model can be trusted on this cluster.
	FabricatedCitations int `json:"fabricated_citations"`
}

// Store persists and queries incident history.
type Store interface {
	// SaveIncident inserts or updates an incident and its evidence.
	SaveIncident(record Record) error
	// SaveExplanation attaches a verified explanation to an incident.
	SaveExplanation(exp explanation.Explanation) error
	// Incident returns one record by id.
	Incident(id string) (Record, error)
	// Incidents lists records newest first.
	Incidents(filter Filter) ([]Record, error)
	// Resolve marks an incident closed.
	Resolve(id string, at time.Time) error
	// SaveHealth records one health sample.
	SaveHealth(sample HealthSample) error
	// Health returns samples since a cutoff, oldest first.
	Health(since time.Time, limit int) ([]HealthSample, error)
	// Stats summarises the history.
	Stats(since time.Time) (Stats, error)
	// Close releases the database.
	Close() error
}

// DefaultLimit bounds a query that did not ask for one, so a dashboard bug
// cannot ask for a million rows.
const DefaultLimit = 100

// NormalisedLimit is the limit this filter will actually use, so a response can
// report the page size it applied rather than the one that was asked for.
func (f Filter) NormalisedLimit() int { return f.normalise().Limit }

// normalise applies the defaults a caller left unset.
func (f Filter) normalise() Filter {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = DefaultLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	return f
}
