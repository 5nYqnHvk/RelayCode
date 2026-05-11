package webtools

import (
	"net"
	"testing"
)

func TestEgressRejectsPrivateNetworksByDefault(t *testing.T) {
	policy := NewEgressPolicy("http,https", false)
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "169.254.169.254", "::1"} {
		if err := policy.ValidateIP(net.ParseIP(ip)); err == nil {
			t.Fatalf("ValidateIP(%s) returned nil", ip)
		}
	}
}

func TestEgressAllowsPrivateNetworksWhenEnabled(t *testing.T) {
	policy := NewEgressPolicy("http,https", true)
	if err := policy.ValidateIP(net.ParseIP("127.0.0.1")); err != nil {
		t.Fatalf("ValidateIP returned %v", err)
	}
}

func TestEgressRejectsDisallowedScheme(t *testing.T) {
	policy := NewEgressPolicy("https", false)
	if _, err := policy.ValidateURL("file:///etc/passwd"); err == nil {
		t.Fatal("ValidateURL returned nil for file scheme")
	}
	if _, err := policy.ValidateURL("http://example.com"); err == nil {
		t.Fatal("ValidateURL returned nil for disallowed http scheme")
	}
}
