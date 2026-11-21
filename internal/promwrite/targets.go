// SPDX-License-Identifier: GPL-3.0-or-later

package promwrite

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// Target is one named remote write receiver. The name ends up in a label
// name (see Label), so it must be one.
type Target struct {
	Name string
	URL  string // base URL, e.g. http://127.0.0.1:8428; /api/v1/write is added
}

var labelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// ParseTarget reads "name=url".
func ParseTarget(s string) (Target, error) {
	name, u, ok := strings.Cut(s, "=")
	if !ok || u == "" {
		return Target{}, fmt.Errorf("push target %q: want name=url", s)
	}
	if !labelName.MatchString(name) {
		return Target{}, fmt.Errorf("push target %q: the name must be letters, digits and _, not starting with a digit", s)
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return Target{}, fmt.Errorf("push target %q: the url must be http:// or https://", s)
	}
	return Target{Name: name, URL: u}, nil
}

// Label is the label that marks a scraped series as also pushed to t:
// pushed_<name>="true". The receiver's own scrape job drops series carrying
// it, so they are stored once; every other scraper keeps them.
func (t Target) Label() string { return "pushed_" + t.Name }

// Targets is a repeatable flag of name=url targets.
type Targets []Target

func (ts *Targets) String() string {
	var s []string
	for _, t := range *ts {
		s = append(s, t.Name+"="+t.URL)
	}
	return strings.Join(s, ",")
}

// Set adds one target; a repeated name is an error.
func (ts *Targets) Set(s string) error {
	t, err := ParseTarget(s)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(*ts, func(o Target) bool { return o.Name == t.Name }) {
		return fmt.Errorf("push target %q: name %s given twice", s, t.Name)
	}
	*ts = append(*ts, t)
	return nil
}

// Labels returns pushed_<name>="true" for every target: the labels to put on
// scraped copies of pushed series. Empty (not nil) with no targets.
func (ts Targets) Labels() map[string]string {
	l := make(map[string]string, len(ts))
	for _, t := range ts {
		l[t.Label()] = "true"
	}
	return l
}

// Push is one target with its client and its own queue, so a target that is
// down keeps its backlog without holding up the others.
type Push struct {
	Target
	Client *Client
	Queue  *Queue
}

// Pushers fans series out to every target.
type Pushers []*Push

// NewPushers makes a client and a queue for each target. hc may be nil.
func NewPushers(ts Targets, hc *http.Client) Pushers {
	var ps Pushers
	for _, t := range ts {
		c := &Client{Base: t.URL, HTTP: hc}
		ps = append(ps, &Push{Target: t, Client: c, Queue: &Queue{W: c}})
	}
	return ps
}

// Targets lists the pushers' targets.
func (ps Pushers) Targets() Targets {
	var ts Targets
	for _, p := range ps {
		ts = append(ts, p.Target)
	}
	return ts
}

// Add queues series for every target.
func (ps Pushers) Add(series ...Series) {
	for _, p := range ps {
		p.Queue.Add(series...)
	}
}

// Flush flushes every target's queue and joins their errors, each naming its
// target.
func (ps Pushers) Flush(ctx context.Context) error {
	var errs []error
	for _, p := range ps {
		if err := p.Queue.Flush(ctx); err != nil {
			errs = append(errs, fmt.Errorf("push to %s: %w", p.Name, err))
		}
	}
	return errors.Join(errs...)
}

// Collectors expose each target's backlog and failed writes, labelled
// target=<name>: <prefix>_push_queued_samples and <prefix>_push_errors_total.
func (ps Pushers) Collectors(prefix string) []prometheus.Collector {
	var cs []prometheus.Collector
	for _, p := range ps {
		q, l := p.Queue, prometheus.Labels{"target": p.Name}
		cs = append(cs,
			prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: prefix + "_push_queued_samples",
				Help: "Samples waiting to be pushed, per target.", ConstLabels: l},
				func() float64 { return float64(q.Len()) }),
			prometheus.NewCounterFunc(prometheus.CounterOpts{Name: prefix + "_push_errors_total",
				Help: "Failed remote writes, per target.", ConstLabels: l},
				func() float64 { return float64(q.Errors()) }))
	}
	return cs
}
