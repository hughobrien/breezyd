// SPDX-License-Identifier: GPL-3.0-or-later

package promwrite

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// unsnappy decodes a snappy block made of literals, the only element
// snappyLiterals writes. Copy elements are rejected.
func unsnappy(t *testing.T, b []byte) []byte {
	t.Helper()
	n, k := protowire.ConsumeVarint(b)
	if k < 0 {
		t.Fatal("bad snappy length")
	}
	b = b[k:]
	var out []byte
	for len(b) > 0 {
		tag := b[0]
		if tag&3 != 0 {
			t.Fatalf("element type %d, want a literal", tag&3)
		}
		var l int
		switch m := int(tag >> 2); {
		case m < 60:
			l, b = m+1, b[1:]
		case m == 61:
			l, b = int(b[1])|int(b[2])<<8+1, b[3:]
		default:
			t.Fatalf("literal length form %d not expected", m)
		}
		out, b = append(out, b[:l]...), b[l:]
	}
	if uint64(len(out)) != n {
		t.Fatalf("decoded %d bytes, header says %d", len(out), n)
	}
	return out
}

type decoded struct {
	labels  [][2]string
	samples []Sample
}

func field(t *testing.T, b []byte) (protowire.Number, protowire.Type, []byte, uint64, []byte) {
	t.Helper()
	num, typ, n := protowire.ConsumeTag(b)
	if n < 0 {
		t.Fatal("bad tag")
	}
	b = b[n:]
	switch typ {
	case protowire.BytesType:
		v, n := protowire.ConsumeBytes(b)
		return num, typ, v, 0, b[n:]
	case protowire.VarintType:
		v, n := protowire.ConsumeVarint(b)
		return num, typ, nil, v, b[n:]
	case protowire.Fixed64Type:
		v, n := protowire.ConsumeFixed64(b)
		return num, typ, nil, v, b[n:]
	}
	t.Fatalf("wire type %d", typ)
	return 0, 0, nil, 0, nil
}

func decode(t *testing.T, b []byte) []decoded {
	var out []decoded
	for len(b) > 0 {
		num, _, ts, _, rest := field(t, b)
		b = rest
		if num != 1 {
			t.Fatalf("WriteRequest field %d", num)
		}
		var d decoded
		for len(ts) > 0 {
			num, _, m, _, rest := field(t, ts)
			ts = rest
			switch num {
			case 1:
				var l [2]string
				for len(m) > 0 {
					n, _, v, _, r := field(t, m)
					m = r
					l[n-1] = string(v)
				}
				d.labels = append(d.labels, l)
			case 2:
				var s Sample
				for len(m) > 0 {
					n, _, _, v, r := field(t, m)
					m = r
					if n == 1 {
						s.V = math.Float64frombits(v)
					} else {
						s.T = time.UnixMilli(int64(v)).UTC()
					}
				}
				d.samples = append(d.samples, s)
			}
		}
		out = append(out, d)
	}
	return out
}

func TestSnappyLiterals(t *testing.T) {
	if got := snappyLiterals([]byte("abc")); !bytes.Equal(got, []byte{3, 61 << 2, 2, 0, 'a', 'b', 'c'}) {
		t.Errorf("abc: % x", got)
	}
	big := bytes.Repeat([]byte("0123456789"), 20000) // three chunks
	if got := unsnappy(t, snappyLiterals(big)); !bytes.Equal(got, big) {
		t.Error("200 kB did not round-trip")
	}
}

func TestWrite(t *testing.T) {
	t0 := time.Date(2026, 11, 5, 9, 1, 0, 0, time.UTC)
	var got []decoded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/write" ||
			r.Header.Get("Content-Encoding") != "snappy" || r.Header.Get("Content-Type") != "application/x-protobuf" ||
			r.Header.Get("X-Prometheus-Remote-Write-Version") != "0.1.0" {
			t.Errorf("request %s %s %v", r.Method, r.URL, r.Header)
		}
		b, _ := io.ReadAll(r.Body)
		got = decode(t, unsnappy(t, b))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL + "/"}
	err := c.Write(context.Background(), []Series{{
		Labels:  map[string]string{"__name__": "x_watts", "channel": "Oven", "a": "1"},
		Samples: []Sample{{t0, 600}, {t0.Add(time.Minute), 0.5}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d series", len(got))
	}
	want := [][2]string{{"__name__", "x_watts"}, {"a", "1"}, {"channel", "Oven"}}
	if len(got[0].labels) != 3 || got[0].labels[0] != want[0] || got[0].labels[1] != want[1] || got[0].labels[2] != want[2] {
		t.Errorf("labels %v, want sorted %v", got[0].labels, want)
	}
	if s := got[0].samples; len(s) != 2 || !s[0].T.Equal(t0) || s[0].V != 600 || !s[1].T.Equal(t0.Add(time.Minute)) || s[1].V != 0.5 {
		t.Errorf("samples %v", s)
	}
}

func TestWriteErrors(t *testing.T) {
	for _, tc := range []struct {
		status  int
		refused bool
	}{{400, true}, {429, false}, {500, false}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "out of bounds", tc.status)
		}))
		err := (&Client{Base: srv.URL}).Write(context.Background(), nil)
		srv.Close()
		var re *RefusedError
		if err == nil || errors.As(err, &re) != tc.refused || !strings.Contains(err.Error(), "out of bounds") {
			t.Errorf("HTTP %d: got %v, refused should be %v", tc.status, err, tc.refused)
		}
	}
}

func TestNewest(t *testing.T) {
	var q string
	reply := `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1762333500,"1762333260"]}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query().Get("query")
		_, _ = io.WriteString(w, reply)
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL}
	at, ok, err := c.Newest(context.Background(), `m{job="x"}`, 8*24*time.Hour)
	if err != nil || !ok || !at.Equal(time.Unix(1762333260, 0)) {
		t.Fatalf("got %v %v %v", at, ok, err)
	}
	if q != `max(max_over_time(timestamp(m{job="x"})[691200s:1m]))` {
		t.Errorf("query %s", q)
	}
	reply = `{"status":"success","data":{"resultType":"vector","result":[]}}`
	if _, ok, err := c.Newest(context.Background(), `m`, time.Hour); ok || err != nil {
		t.Errorf("empty result: ok=%v err=%v", ok, err)
	}
}

func TestOldest(t *testing.T) {
	var q string
	reply := `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1762333500,"1762333200"]}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query().Get("query")
		_, _ = io.WriteString(w, reply)
	}))
	defer srv.Close()
	c := &Client{Base: srv.URL}
	at, ok, err := c.Oldest(context.Background(), `m{job="x"}`, 5*365*24*time.Hour)
	if err != nil || !ok || !at.Equal(time.Unix(1762333200, 0)) {
		t.Fatalf("got %v %v %v", at, ok, err)
	}
	if q != `min(min_over_time(timestamp(m{job="x"})[157680000s:1h]))` {
		t.Errorf("query %s", q)
	}
	reply = `{"status":"success","data":{"resultType":"vector","result":[]}}`
	if _, ok, err := c.Oldest(context.Background(), `m`, time.Hour); ok || err != nil {
		t.Errorf("empty result: ok=%v err=%v", ok, err)
	}
}
