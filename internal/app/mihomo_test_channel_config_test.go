package app

import (
	"testing"
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
