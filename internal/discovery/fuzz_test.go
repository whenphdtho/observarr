package discovery

import "testing"

func FuzzParseTargets(f *testing.F) {
	for _, s := range []string{"", "a", "10.0.0.0/22", "10.0.0.0/21", "fc00::/120", "8.8.8.0/24", "a,b c\nd",
		"10.0.0.1 10.0.0.0/30", "::ffff:10.0.0.1/120", "10.0.0.0/22 10.0.4.0/22", "-a", "a..b", "1.2.3.4/33"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		hosts, err := ParseTargets(s)
		if err != nil {
			return
		}
		if len(hosts) > MaxHosts {
			t.Fatalf("%d hosts", len(hosts))
		}
		seen := map[string]bool{}
		for _, h := range hosts {
			if seen[h] || h == "" {
				t.Fatalf("host %q twice or empty", h)
			}
			seen[h] = true
		}
	})
}
