package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestImportServices(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	none := func(int64) ([]byte, error) { return nil, nil }
	if _, err := s.CreateService(ctx, Service{Type: "sonarr", Name: "Old", BaseURL: "http://old"}, none); err != nil {
		t.Fatal(err)
	}
	seal := func(i int, id int64) ([]byte, error) { return []byte(fmt.Sprintf("key %d for %d", i, id)), nil }
	in := []Service{{Type: "sonarr", Name: "A", BaseURL: "http://a"}, {Type: "radarr", Name: "B", BaseURL: "http://b", Container: "b"}}

	added, err := s.ImportServices(ctx, false, in, []string{"sonarr http://x:80"}, seal)
	if err != nil || len(added) != 2 || string(added[1].SealedKey) != fmt.Sprintf("key 1 for %d", added[1].ID) ||
		added[1].Container != "b" {
		t.Fatalf("ImportServices = %+v, %v", added, err)
	}
	if list, _ := s.Services(ctx); len(list) != 3 {
		t.Fatalf("after adding alongside: %d services, want 3", len(list))
	}
	if d, _ := s.Dismissed(ctx); !d["sonarr http://x:80"] {
		t.Fatal("dismissed find not imported")
	}
	if _, err := s.ImportServices(ctx, false, in[:1], nil, seal); !errors.Is(err, ErrExists) {
		t.Fatalf("importing a taken name = %v, want ErrExists", err)
	}

	if _, err := s.ImportServices(ctx, true, in, nil, seal); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.Services(ctx); len(list) != 2 || list[0].Name != "A" {
		t.Fatalf("after replacing: %+v", list)
	}
}

// A failure midway through a replacing import leaves everything as it was.
func TestImportServicesIsOneTransaction(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	before := []Service{{Type: "sonarr", Name: "A", BaseURL: "http://a"}, {Type: "radarr", Name: "B", BaseURL: "http://b"}}
	ok := func(int, int64) ([]byte, error) { return []byte("k"), nil }
	if _, err := s.ImportServices(ctx, false, before, nil, ok); err != nil {
		t.Fatal(err)
	}
	failSecond := func(i int, _ int64) ([]byte, error) {
		if i == 1 {
			return nil, errors.New("boom")
		}
		return []byte("k"), nil
	}
	in := []Service{{Type: "sonarr", Name: "C", BaseURL: "http://c"}, {Type: "radarr", Name: "D", BaseURL: "http://d"}}
	if _, err := s.ImportServices(ctx, true, in, []string{"sonarr http://c:80"}, failSecond); err == nil {
		t.Fatal("a failing seal did not fail the import")
	}
	list, _ := s.Services(ctx)
	if len(list) != 2 || list[0].Name != "A" || list[1].Name != "B" {
		t.Fatalf("after a failed import: %+v", list)
	}
	if d, _ := s.Dismissed(ctx); len(d) != 0 {
		t.Fatalf("a failed import dismissed %v", d)
	}
}
