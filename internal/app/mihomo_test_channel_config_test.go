package app

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNewMihomoTestChannelUsesConfiguredSmokeIdentityEndpoints(t *testing.T) {
	channel := NewMihomoTestChannelWithIdentityEndpoints(
		"nodeharbor-core",
		WindowsMihomoBuild,
		"http://127.0.0.1:19001/identity",
		"http://127.0.0.1:19001/identity-v6",
	)
	if channel.ipv4IdentityEndpoint != "http://127.0.0.1:19001/identity" || channel.ipv6IdentityEndpoint != "http://127.0.0.1:19001/identity-v6" {
		t.Fatalf("identity endpoints = %q and %q", channel.ipv4IdentityEndpoint, channel.ipv6IdentityEndpoint)
	}
}

func TestMihomoTestChannelConfigOwnsLoopbackProxyAndControlPorts(t *testing.T) {
	document, err := mihomoTestChannelConfig(ProxyNode{
		Name:   "candidate",
		Config: map[string]any{"type": "ss", "server": "example.test", "port": 443},
	}, 19090, 19091)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		MixedPort          int    `yaml:"mixed-port"`
		BindAddress        string `yaml:"bind-address"`
		ExternalController string `yaml:"external-controller"`
		AllowLAN           bool   `yaml:"allow-lan"`
	}
	if err := yaml.Unmarshal(document, &config); err != nil {
		t.Fatal(err)
	}
	if config.MixedPort != 19090 || config.BindAddress != "127.0.0.1" || config.ExternalController != "127.0.0.1:19091" || config.AllowLAN {
		t.Fatalf("config=%+v, want loopback-only proxy and control surfaces", config)
	}
}

func TestFreeLoopbackPortAvoidsSurfingDefaultsAndExcludedPort(t *testing.T) {
	port, err := freeLoopbackPort(19092)
	if err != nil {
		t.Fatal(err)
	}
	if IsSurfingDefaultPort(port) || port == 19092 {
		t.Fatalf("selected unsafe Test Channel port %d", port)
	}
}
