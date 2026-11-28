package docker

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// fuzzTransport answers /version with a usable version and every other request with body.
type fuzzTransport []byte

func (b fuzzTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body := []byte(b)
	if req.URL.Path == "/version" {
		body = []byte(`{"ApiVersion":"1.43"}`)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body)),
		Request: req}, nil
}

func FuzzContainers(f *testing.F) {
	f.Add([]byte(listJSON))
	f.Add([]byte(inspectJSON))
	f.Add([]byte(`[{"Id":"` + strings.Repeat("a", 64) + `","State":"running","Ports":[{"PrivatePort":-1}]}]`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, body []byte) {
		c, err := New("tcp://docker:2375")
		if err != nil {
			t.Fatal(err)
		}
		c.http = &http.Client{Transport: fuzzTransport(body)}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		list, _ := c.Containers(ctx)
		if len(list) > MaxContainers {
			t.Fatalf("%d containers", len(list))
		}
		for _, ct := range list {
			if !containerID.MatchString(ct.ID) || ct.PID < 0 {
				t.Fatalf("container %+v", ct)
			}
		}
	})
}
