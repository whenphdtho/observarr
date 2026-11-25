package adapter

import (
	"errors"
	"net/http"
	"testing"
)

func TestBasicAuth(t *testing.T) {
	if authorize, err := BasicAuth(""); authorize != nil || err != nil {
		t.Errorf("empty key: %v, %v; want no credentials", authorize != nil, err)
	}
	for _, key := range []string{"no-colon", ":password"} {
		if _, err := BasicAuth(key); !errors.Is(err, ErrCredentials) {
			t.Errorf("%q: err = %v, want ErrCredentials", key, err)
		}
	}
	authorize, err := BasicAuth("user:pa:ss")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequest(http.MethodPost, "http://h/", nil)
	authorize(r)
	if u, p, ok := r.BasicAuth(); !ok || u != "user" || p != "pa:ss" {
		t.Errorf("basic auth = %q, %q, %v", u, p, ok)
	}
}
