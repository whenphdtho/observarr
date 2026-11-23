package shape

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestHealthJSONFieldNames(t *testing.T) {
	h := Health{
		Version:   "4.1.0",
		LatencyMS: 42,
		Warnings: []Warning{
			{Level: LevelWarning, Message: "low disk space", Source: "DiskSpaceCheck"},
		},
	}
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, field := range []string{"version", "latencyMs", "warnings"} {
		if _, ok := got[field]; !ok {
			t.Errorf("missing field %q in %s", field, b)
		}
	}

	warnings, ok := got["warnings"].([]any)
	if !ok || len(warnings) != 1 {
		t.Fatalf("warnings: got %v", got["warnings"])
	}
	w, ok := warnings[0].(map[string]any)
	if !ok {
		t.Fatalf("warning[0]: got %v", warnings[0])
	}
	for _, field := range []string{"level", "message", "source"} {
		if _, ok := w[field]; !ok {
			t.Errorf("missing warning field %q in %s", field, b)
		}
	}

	var round Health
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if round.Version != h.Version || round.LatencyMS != h.LatencyMS {
		t.Errorf("round trip mismatch: got %+v, want %+v", round, h)
	}
	if len(round.Warnings) != 1 || round.Warnings[0] != h.Warnings[0] {
		t.Errorf("round trip warnings mismatch: got %+v, want %+v", round.Warnings, h.Warnings)
	}
}

func TestHealthWarningsEmptySliceMarshalsAsArray(t *testing.T) {
	h := Health{Version: "1.0.0", LatencyMS: 1, Warnings: []Warning{}}
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if string(got["warnings"]) != "[]" {
		t.Errorf("warnings: got %s, want []", got["warnings"])
	}
}

func TestQueueJSONFieldNames(t *testing.T) {
	q := Queue{
		Total: 2,
		Items: []QueueItem{
			{
				Title:     "Some.Show.S01E01",
				Status:    "downloading",
				Progress:  0.5,
				SizeBytes: 1000,
				LeftBytes: 500,
				ETA:       "00:10:00",
				Problem:   "",
			},
		},
	}
	b, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, field := range []string{"total", "items"} {
		if _, ok := got[field]; !ok {
			t.Errorf("missing field %q in %s", field, b)
		}
	}

	items, ok := got["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items: got %v", got["items"])
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("items[0]: got %v", items[0])
	}
	for _, field := range []string{"title", "status", "progress", "sizeBytes", "leftBytes", "eta"} {
		if _, ok := item[field]; !ok {
			t.Errorf("missing item field %q in %s", field, b)
		}
	}
	if _, ok := item["problem"]; ok {
		t.Errorf("empty problem should be omitted, got %v", item["problem"])
	}

	var round Queue
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if round.Total != q.Total || len(round.Items) != 1 || round.Items[0] != q.Items[0] {
		t.Errorf("round trip mismatch: got %+v, want %+v", round, q)
	}
}

func TestQueueItemsEmptySliceMarshalsAsArray(t *testing.T) {
	q := Queue{Total: 0, Items: []QueueItem{}}
	b, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if string(got["items"]) != "[]" {
		t.Errorf("items: got %s, want []", got["items"])
	}
}

func TestReadingJSONRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	r := Reading{Data: Health{Version: "1.0.0"}, At: at}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := got["data"]; !ok {
		t.Errorf("missing field %q in %s", "data", b)
	}
	if _, ok := got["at"]; !ok {
		t.Errorf("missing field %q in %s", "at", b)
	}
	if _, ok := got["error"]; ok {
		t.Errorf("empty error should be omitted, got %v", got["error"])
	}
}

func TestClipNoOpUnderLimit(t *testing.T) {
	s := "a short message"
	if got := Clip(s); got != s {
		t.Errorf("Clip(%q) = %q, want it unchanged", s, got)
	}
}

func TestClipAtExactlyMaxTextIsUnchanged(t *testing.T) {
	s := strings.Repeat("a", MaxText)
	if got := Clip(s); got != s {
		t.Errorf("Clip of a string exactly MaxText long changed length %d -> %d", len(s), len(got))
	}
}

func TestClipBoundsA3MBMessage(t *testing.T) {
	big := strings.Repeat("a", 3*1024*1024)
	got := Clip(big)
	if len(got) > MaxText {
		t.Fatalf("len(Clip(...)) = %d, want <= %d", len(got), MaxText)
	}
	if !utf8.ValidString(got) {
		t.Error("Clip produced invalid UTF-8")
	}
}

func TestClipRespectsMultiByteRuneBoundary(t *testing.T) {
	// "世" is 3 bytes long; MaxText (512) is not a multiple of 3, so slicing by byte count
	// alone would cut the rune at the boundary in half.
	s := strings.Repeat("世", 200) // 600 bytes
	got := Clip(s)
	if len(got) > MaxText {
		t.Fatalf("len(Clip(...)) = %d, want <= %d", len(got), MaxText)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("Clip cut a multi-byte rune in half: %q", got)
	}
	for _, r := range got {
		if r != '世' {
			t.Errorf("Clip introduced a broken rune %q in %q", r, got)
		}
	}
	if len(got) == 0 {
		t.Error("Clip returned an empty string for a 600-byte input")
	}
}
