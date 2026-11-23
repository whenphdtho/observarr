// Package config reads Observarr's process settings from environment variables.
// Everything else (services, alerts, users) lives in the database.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"observarr/internal/collect/docker"
)

// Config holds the process settings.
type Config struct {
	Listen          string         // OBSERVARR_LISTEN, default ":9898"
	DataDir         string         // OBSERVARR_DATA, default "/data"
	SecretKeyFile   string         // OBSERVARR_SECRET_KEY_FILE: key sealing saved API keys, default "<data>/secret.key"
	SecureCookies   bool           // false only with OBSERVARR_INSECURE_COOKIES=1 (plain-HTTP LAN setups)
	TrustedProxies  []netip.Prefix // OBSERVARR_TRUSTED_PROXIES: peers whose X-Forwarded-For/-Proto are believed
	ProxyAuth       bool           // OBSERVARR_PROXY_AUTH=1: trusted proxies also assert the logged-in user
	ProxyUserHeader string         // OBSERVARR_PROXY_USER_HEADER, default "Remote-User"
	ResetDB         bool           // OBSERVARR_RESET_DB=1: move a corrupt database aside and start empty
	ResetKeys       bool           // OBSERVARR_RESET_KEYS=1: drop saved API keys when the secret key is missing
	AppConfigs      string         // OBSERVARR_APP_CONFIGS: folder holding app config folders; empty = not used
	HostProc        string         // OBSERVARR_HOST_PROC: host /proc mounted read-only; empty = no host figures
	HostCgroup      string         // OBSERVARR_HOST_CGROUP: host /sys/fs/cgroup mounted read-only; empty = no container usage
	HostDisks       []string       // OBSERVARR_HOST_DISKS: comma-separated paths whose file systems are measured
	DockerHost      string         // OBSERVARR_DOCKER_HOST: socket proxy, tcp://host:port or unix:///path; empty = no Docker
}

// FromEnv builds a Config using getenv (os.Getenv in production).
func FromEnv(getenv func(string) string) (Config, error) {
	c := Config{
		Listen:          valueOr(getenv("OBSERVARR_LISTEN"), ":9898"),
		DataDir:         valueOr(getenv("OBSERVARR_DATA"), "/data"),
		ProxyUserHeader: valueOr(getenv("OBSERVARR_PROXY_USER_HEADER"), "Remote-User"),
	}
	c.SecretKeyFile = valueOr(getenv("OBSERVARR_SECRET_KEY_FILE"), filepath.Join(c.DataDir, "secret.key"))
	var insecure bool
	for _, f := range []struct {
		name string
		dst  *bool
	}{{"OBSERVARR_INSECURE_COOKIES", &insecure}, {"OBSERVARR_PROXY_AUTH", &c.ProxyAuth},
		{"OBSERVARR_RESET_DB", &c.ResetDB}, {"OBSERVARR_RESET_KEYS", &c.ResetKeys}} {
		switch v := getenv(f.name); v {
		case "", "0":
		case "1":
			*f.dst = true
		default:
			return Config{}, fmt.Errorf("%s: %q is not 0 or 1", f.name, v)
		}
	}
	c.SecureCookies = !insecure
	for _, field := range strings.Split(getenv("OBSERVARR_TRUSTED_PROXIES"), ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		p, err := parsePrefix(field)
		if err != nil {
			return Config{}, fmt.Errorf("OBSERVARR_TRUSTED_PROXIES: %q is not an IP address or CIDR", field)
		}
		c.TrustedProxies = append(c.TrustedProxies, p)
	}
	if c.ProxyAuth && len(c.TrustedProxies) == 0 {
		return Config{}, errors.New("OBSERVARR_PROXY_AUTH needs OBSERVARR_TRUSTED_PROXIES (the proxies allowed to assert the user)")
	}
	for _, d := range []struct {
		name string
		dst  *string
	}{{"OBSERVARR_APP_CONFIGS", &c.AppConfigs}, {"OBSERVARR_HOST_PROC", &c.HostProc}, {"OBSERVARR_HOST_CGROUP", &c.HostCgroup}} {
		if *d.dst = getenv(d.name); *d.dst != "" {
			if fi, err := os.Stat(*d.dst); err != nil || !fi.IsDir() {
				return Config{}, fmt.Errorf("%s: %q is not an existing directory", d.name, *d.dst)
			}
		}
	}
	if c.HostCgroup != "" && c.HostProc == "" { // each container's cgroup path and network counters come from /proc
		return Config{}, errors.New("OBSERVARR_HOST_CGROUP needs OBSERVARR_HOST_PROC")
	}
	for _, field := range strings.Split(getenv("OBSERVARR_HOST_DISKS"), ",") {
		if field = strings.TrimSpace(field); field == "" {
			continue
		}
		if _, err := os.Stat(field); err != nil {
			return Config{}, fmt.Errorf("OBSERVARR_HOST_DISKS: %q does not exist", field)
		}
		c.HostDisks = append(c.HostDisks, field)
	}
	if c.DockerHost = getenv("OBSERVARR_DOCKER_HOST"); c.DockerHost != "" {
		if _, _, err := docker.ParseHost(c.DockerHost); err != nil {
			return Config{}, fmt.Errorf("OBSERVARR_DOCKER_HOST: %w", err)
		}
	}
	return c, nil
}

// parsePrefix accepts a CIDR or a single address (which becomes a /32 or /128).
func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		return p.Masked(), err
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
