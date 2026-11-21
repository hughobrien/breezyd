// SPDX-License-Identifier: GPL-3.0-or-later

package promwrite

import (
	"context"
	"errors"
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestTargetsFlag(t *testing.T) {
	var ts Targets
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	fs.Var(&ts, "push", "")
	if err := fs.Parse([]string{"--push", "bigthink=http://127.0.0.1:8428", "--push", "off_site=https://vm.example/"}); err != nil {
		t.Fatal(err)
	}
	if len(ts) != 2 || ts[0] != (Target{"bigthink", "http://127.0.0.1:8428"}) || ts[1].Name != "off_site" {
		t.Fatalf("got %v", ts)
	}
	l := ts.Labels()
	if len(l) != 2 || l["pushed_bigthink"] != "true" || l["pushed_off_site"] != "true" {
		t.Errorf("labels %v", l)
	}
	for _, bad := range []string{"bigthink=http://other", "noequals", "1st=http://x", "a-b=http://x", "a=", "a=ftp://x"} {
		if err := ts.Set(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
	if (Targets{}).Labels() == nil {
		t.Error("Labels with no targets should be empty, not nil")
	}
}

type countWriter struct {
	n   int
	err error
}

func (w *countWriter) Write(context.Context, []Series) error {
	if w.err != nil {
		return w.err
	}
	w.n++
	return nil
}

func TestPushersKeepEachTargetsBacklog(t *testing.T) {
	ps := NewPushers(Targets{{"up", "http://a"}, {"down", "http://b"}}, nil)
	up, down := &countWriter{}, &countWriter{err: errors.New("connection refused")}
	ps[0].Queue.W, ps[1].Queue.W = up, down
	for _, p := range ps {
		p.Queue.Now = func() time.Time { return at(10) }
	}
	ps.Add(one("m", 1, 1))
	err := ps.Flush(context.Background())
	if err == nil || !strings.Contains(err.Error(), "push to down") {
		t.Fatalf("want the down target named in the error, got %v", err)
	}
	if up.n != 1 || ps[0].Queue.Len() != 0 || ps[1].Queue.Len() != 1 {
		t.Fatalf("up should have taken the sample and down kept it: up %d, queued %d/%d", up.n, ps[0].Queue.Len(), ps[1].Queue.Len())
	}
	down.err = nil
	if err := ps.Flush(context.Background()); err != nil || down.n != 1 || ps[1].Queue.Len() != 0 {
		t.Fatalf("down should catch up once back: %v, %d", err, down.n)
	}
}

func TestPushersCollectors(t *testing.T) {
	ps := NewPushers(Targets{{"a", "http://a"}, {"b", "http://b"}}, nil)
	reg := prometheus.NewRegistry()
	reg.MustRegister(ps.Collectors("x")...)
	ps[1].Queue.Add(one("m", 1, 1))
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, f := range fams {
		for _, m := range f.GetMetric() {
			got[f.GetName()+"/"+m.GetLabel()[0].GetValue()] = m.GetGauge().GetValue() + m.GetCounter().GetValue()
		}
	}
	want := map[string]float64{"x_push_queued_samples/a": 0, "x_push_queued_samples/b": 1, "x_push_errors_total/a": 0, "x_push_errors_total/b": 0}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}
