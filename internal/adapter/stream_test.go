package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// emptyObjects is a JSON array of empty objects, just under n bytes.
func emptyObjects(n int) []byte {
	b := make([]byte, 0, n)
	b = append(b, '[')
	for len(b) < n-4 {
		b = append(b, "{},"...)
	}
	return append(b, "{}]"...)
}

func TestDecodeArrayStopsAtCap(t *testing.T) {
	body := emptyObjects(4 << 20)
	var got int
	allocs := testing.AllocsPerRun(5, func() {
		got = 0
		capped, err := DecodeArray(json.NewDecoder(bytes.NewReader(body)), 50, func(struct{}) { got++ })
		if err != nil || !capped {
			t.Fatalf("capped = %v, err = %v; want capped, no error", capped, err)
		}
	})
	if got != 50 {
		t.Fatalf("got %d elements, want 50", got)
	}
	t.Logf("%v allocations per run", allocs)
	// Reading all of the ~1.4 million elements would take millions of allocations.
	if allocs > 300 {
		t.Fatalf("%v allocations per run: the array was read past the cap", allocs)
	}
}

func TestDecodeArray(t *testing.T) {
	for _, c := range []struct {
		in     string
		want   []int
		capped bool
		ok     bool
	}{
		{`[1,2]`, []int{1, 2}, false, true},
		{`null`, nil, false, true},
		{`[1,2,3,4]`, []int{1, 2, 3}, true, true},
		{`{"a":1}`, nil, false, false},
		{`[1,"x"]`, []int{1}, false, false},
		{`[1,2`, []int{1, 2}, false, false},
	} {
		var got []int
		capped, err := DecodeArray(json.NewDecoder(strings.NewReader(c.in)), 3, func(v int) { got = append(got, v) })
		if !slices.Equal(got, c.want) || capped != c.capped || (err == nil) != c.ok {
			t.Errorf("%s: got %v, capped %v, err %v", c.in, got, capped, err)
		}
	}
}

func TestStreamJSONStopsReadingAtCap(t *testing.T) {
	body := emptyObjects(maxBodyBytes)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer ts.Close()
	base, _ := url.Parse(ts.URL)
	tgt := Target{BaseURL: base, HTTP: ts.Client()}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	n := 0
	err := StreamJSON(context.Background(), tgt, "/x", "", func(d *json.Decoder) error {
		_, err := DecodeArray(d, 50, func(struct{}) { n++ })
		return err
	})
	runtime.ReadMemStats(&after)
	if err != nil || n != 50 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	alloc := after.TotalAlloc - before.TotalAlloc
	t.Logf("%d bytes allocated", alloc)
	if alloc > 1<<20 {
		t.Fatalf("allocated %d bytes for a 4 MiB answer cut at 50 elements", alloc)
	}
}

func TestStreamJSONErrors(t *testing.T) {
	for _, c := range []struct {
		body string
		want error
	}{
		{`"` + strings.Repeat("a", maxBodyBytes) + `"`, ErrTooLarge},
		{`[1,2] x`, ErrNotJSON},
		{`<html>`, ErrNotJSON},
	} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(c.body)) }))
		base, _ := url.Parse(ts.URL)
		var v any
		err := GetJSON(context.Background(), Target{BaseURL: base, HTTP: ts.Client()}, "/x", "", &v)
		ts.Close()
		if !errors.Is(err, c.want) {
			t.Errorf("body %.20q: err = %v, want %v", c.body, err, c.want)
		}
	}
}

func TestStreamJSONLimit(t *testing.T) {
	body := emptyObjects(6 << 20) // over StreamJSON's 4 MiB
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer ts.Close()
	base, _ := url.Parse(ts.URL)
	tgt := Target{BaseURL: base, HTTP: ts.Client()}
	var n int
	count := func(d *json.Decoder) error {
		n = 0
		_, err := DecodeArray(d, 1<<30, func(struct{}) { n++ })
		return err
	}

	if err := StreamJSON(context.Background(), tgt, "/x", "", count); !errors.Is(err, ErrTooLarge) || n == 0 {
		t.Fatalf("StreamJSON on 6 MiB: n = %d, err = %v; want some counted, then ErrTooLarge", n, err)
	}
	if err := StreamJSONLimit(context.Background(), tgt, "/x", "", 8<<20, count); err != nil || n != (len(body)-2)/3+1 {
		t.Fatalf("StreamJSONLimit(8 MiB) on 6 MiB: n = %d, err = %v", n, err)
	}
	if err := StreamJSONLimit(context.Background(), tgt, "/x", "", 1<<40, count); err != nil {
		t.Fatalf("a limit above MaxStreamBytes is cut to it, not refused: %v", err)
	}
	if err := PostJSON(context.Background(), tgt, "/x", []byte(`{}`), nil, count); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("PostJSON on 6 MiB: err = %v, want ErrTooLarge", err)
	}
	if err := PostJSONLimit(context.Background(), tgt, "/x", []byte(`{}`), nil, 8<<20, count); err != nil ||
		n != (len(body)-2)/3+1 {
		t.Fatalf("PostJSONLimit(8 MiB) on 6 MiB: n = %d, err = %v", n, err)
	}
}
