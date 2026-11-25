package adapter

import (
	"errors"
	"net/http"
	"strings"
)

// ErrCredentials is returned for a secret that should be of the form "user:password" and is not.
var ErrCredentials = errors.New("credentials: enter them as user:password")

// BasicAuth reads key, a service's secret given as "user:password", into a function that adds it to a request as
// HTTP basic authentication, for PostJSON's authorize. An empty key means no credentials: authorize is then nil.
// The password may hold colons; the user name may not, and must not be empty.
func BasicAuth(key string) (authorize func(*http.Request), err error) {
	if key == "" {
		return nil, nil
	}
	user, pass, found := strings.Cut(key, ":")
	if !found || user == "" {
		return nil, ErrCredentials
	}
	return func(r *http.Request) { r.SetBasicAuth(user, pass) }, nil
}
