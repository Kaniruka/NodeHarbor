package app

import (
	"testing"
)

func TestNewMihomoTestChannelUsesConfiguredSmokeIdentityEndpoints(t *testing.T) {
	t.Setenv("NODEHARBOR_TEST_IPV4_IDENTITY_ENDPOINT", "http://127.0.0.1:19001/identity")
	t.Setenv("NODEHARBOR_TEST_IPV6_IDENTITY_ENDPOINT", "http://127.0.0.1:19001/identity-v6")

	channel := NewMihomoTestChannel("nodeharbor-core", WindowsMihomoBuild)
	if channel.ipv4IdentityEndpoint != "http://127.0.0.1:19001/identity" || channel.ipv6IdentityEndpoint != "http://127.0.0.1:19001/identity-v6" {
		t.Fatalf("identity endpoints = %q and %q", channel.ipv4IdentityEndpoint, channel.ipv6IdentityEndpoint)
	}
}
