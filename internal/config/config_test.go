package config

import (
	"os"
	"path/filepath"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c, err := FromEnv(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9898" || c.DataDir != "/data" || !c.SecureCookies || c.ProxyUserHeader != "Remote-User" || len(c.TrustedProxies) != 0 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestTrustedProxies(t *testing.T) {
	c, err := FromEnv(env(map[string]string{"OBSERVARR_TRUSTED_PROXIES": "172.20.0.0/16, 10.0.0.5 ,::1"}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"172.20.0.0/16", "10.0.0.5/32", "::1/128"}
	if len(c.TrustedProxies) != len(want) {
		t.Fatalf("got %v", c.TrustedProxies)
	}
	for i, p := range c.TrustedProxies {
		if p.String() != want[i] {
			t.Errorf("proxy %d = %s, want %s", i, p, want[i])
		}
	}
}

func TestRejectsInvalidProxy(t *testing.T) {
	if _, err := FromEnv(env(map[string]string{"OBSERVARR_TRUSTED_PROXIES": "not-an-ip"})); err == nil {
		t.Fatal("expected an error for an invalid proxy address")
	}
}

func TestInsecureCookies(t *testing.T) {
	c, _ := FromEnv(env(map[string]string{"OBSERVARR_INSECURE_COOKIES": "1"}))
	if c.SecureCookies {
		t.Fatal("OBSERVARR_INSECURE_COOKIES=1 must turn secure cookies off")
	}
}

func TestAppConfigs(t *testing.T) {
	dir := t.TempDir()
	c, err := FromEnv(env(map[string]string{"OBSERVARR_APP_CONFIGS": dir}))
	if err != nil || c.AppConfigs != dir {
		t.Fatalf("AppConfigs = %q, %v", c.AppConfigs, err)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{filepath.Join(dir, "missing"), file} {
		if _, err := FromEnv(env(map[string]string{"OBSERVARR_APP_CONFIGS": bad})); err == nil {
			t.Errorf("OBSERVARR_APP_CONFIGS=%s: want an error", bad)
		}
	}
}

func TestHostSources(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := FromEnv(env(map[string]string{
		"OBSERVARR_HOST_PROC": dir, "OBSERVARR_HOST_CGROUP": dir,
		"OBSERVARR_HOST_DISKS": dir + ", ," + file, "OBSERVARR_DOCKER_HOST": "tcp://socket-proxy:2375",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.HostProc != dir || c.HostCgroup != dir || len(c.HostDisks) != 2 || c.HostDisks[1] != file || c.DockerHost != "tcp://socket-proxy:2375" {
		t.Fatalf("got %+v", c)
	}
	if c, _ := FromEnv(env(nil)); c.HostProc != "" || c.HostCgroup != "" || c.HostDisks != nil || c.DockerHost != "" {
		t.Fatalf("unset must mean unavailable: %+v", c)
	}
	for k, v := range map[string]string{
		"OBSERVARR_HOST_PROC":   file,
		"OBSERVARR_HOST_CGROUP": filepath.Join(dir, "missing"),
		"OBSERVARR_HOST_DISKS":  dir + "," + filepath.Join(dir, "missing"),
		"OBSERVARR_DOCKER_HOST": "http://socket-proxy:2375",
	} {
		if _, err := FromEnv(env(map[string]string{k: v})); err == nil {
			t.Errorf("%s=%s: want an error", k, v)
		}
	}
	_, err = FromEnv(env(map[string]string{"OBSERVARR_HOST_CGROUP": dir}))
	if err == nil || err.Error() != "OBSERVARR_HOST_CGROUP needs OBSERVARR_HOST_PROC" {
		t.Errorf("cgroup without proc: err = %v", err)
	}
}

func TestSwitches(t *testing.T) {
	c, err := FromEnv(env(map[string]string{"OBSERVARR_RESET_DB": "1", "OBSERVARR_RESET_KEYS": "0",
		"OBSERVARR_PROXY_AUTH": "1", "OBSERVARR_TRUSTED_PROXIES": "10.0.0.1"}))
	if err != nil || !c.ResetDB || c.ResetKeys || !c.ProxyAuth {
		t.Fatalf("got %+v, %v", c, err)
	}
	for _, name := range []string{"OBSERVARR_RESET_DB", "OBSERVARR_RESET_KEYS", "OBSERVARR_INSECURE_COOKIES", "OBSERVARR_PROXY_AUTH"} {
		if _, err := FromEnv(env(map[string]string{name: "true", "OBSERVARR_TRUSTED_PROXIES": "10.0.0.1"})); err == nil {
			t.Errorf("%s=true: want an error (only 0 or 1)", name)
		}
	}
}

func TestProxyAuthNeedsTrustedProxies(t *testing.T) {
	if _, err := FromEnv(env(map[string]string{"OBSERVARR_PROXY_AUTH": "1"})); err == nil {
		t.Fatal("OBSERVARR_PROXY_AUTH without OBSERVARR_TRUSTED_PROXIES must be refused")
	}
	c, err := FromEnv(env(map[string]string{"OBSERVARR_TRUSTED_PROXIES": "10.0.0.1"}))
	if err != nil || c.ProxyAuth {
		t.Fatalf("trusted proxies alone must not turn on proxy login: %+v, %v", c, err)
	}
}

func TestSecretKeyFile(t *testing.T) {
	if c, _ := FromEnv(env(map[string]string{"OBSERVARR_DATA": "/srv/obs"})); c.SecretKeyFile != filepath.Join("/srv/obs", "secret.key") {
		t.Fatalf("default key file = %q", c.SecretKeyFile)
	}
	if c, _ := FromEnv(env(map[string]string{"OBSERVARR_SECRET_KEY_FILE": "/run/secrets/key"})); c.SecretKeyFile != "/run/secrets/key" {
		t.Fatalf("key file = %q", c.SecretKeyFile)
	}
}
