package config

import (
	"strings"
	"testing"
)

// FuzzFromEnv feeds the settings that are read from their text alone with any value: each is taken with a sane
// result or refused with an error naming the setting.
func FuzzFromEnv(f *testing.F) {
	f.Add("1400GB/day", "10.0.0.0/8, ::1", "tcp://docker:2375", "1", "")
	f.Add("0.001 tb / Month", "1.2.3.4/33", "unix:///run/docker.sock", "0", "x")
	f.Add("9999999.999TB/month", ",,", "tcp://a", "true", "1")
	f.Fuzz(func(t *testing.T, budget, proxies, docker, insecure, proxyAuth string) {
		env := map[string]string{
			"OBSERVARR_TRAFFIC_BUDGET": budget, "OBSERVARR_TRUSTED_PROXIES": proxies,
			"OBSERVARR_DOCKER_HOST": docker, "OBSERVARR_INSECURE_COOKIES": insecure, "OBSERVARR_PROXY_AUTH": proxyAuth,
		}
		c, err := FromEnv(func(k string) string { return env[k] })
		if err != nil {
			if !strings.HasPrefix(err.Error(), "OBSERVARR_") {
				t.Fatalf("error does not name the setting: %q", err)
			}
			return
		}
		if b := c.TrafficBudget; b != (Budget{}) && (b.Limit == 0 || (b.Period != "day" && b.Period != "month")) {
			t.Fatalf("budget %q read as %+v", budget, b)
		}
		for _, p := range c.TrustedProxies {
			if !p.IsValid() || p != p.Masked() {
				t.Fatalf("proxy %v from %q", p, proxies)
			}
		}
		if c.ProxyAuth && len(c.TrustedProxies) == 0 {
			t.Fatal("proxy auth without trusted proxies")
		}
	})
}
