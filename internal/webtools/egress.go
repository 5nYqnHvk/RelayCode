package webtools

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
)

type EgressPolicy struct {
	AllowedSchemes       map[string]bool
	AllowPrivateNetworks bool
}

type EgressViolation struct{ Message string }

func (e EgressViolation) Error() string { return e.Message }

func NewEgressPolicy(schemes string, allowPrivate bool) EgressPolicy {
	allowed := map[string]bool{}
	for _, scheme := range strings.Split(schemes, ",") {
		scheme = strings.ToLower(strings.TrimSpace(scheme))
		if scheme != "" {
			allowed[scheme] = true
		}
	}
	if len(allowed) == 0 {
		allowed["http"] = true
		allowed["https"] = true
	}
	return EgressPolicy{AllowedSchemes: allowed, AllowPrivateNetworks: allowPrivate}
}

func (p EgressPolicy) ValidateURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, EgressViolation{Message: "web_fetch URL is invalid"}
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, EgressViolation{Message: "web_fetch URL must include scheme and host"}
	}
	if !p.AllowedSchemes[strings.ToLower(parsed.Scheme)] {
		return nil, EgressViolation{Message: fmt.Sprintf("web_fetch scheme %q is not allowed", parsed.Scheme)}
	}
	return parsed, nil
}

func (p EgressPolicy) ValidateHost(host string) error {
	if host == "" {
		return EgressViolation{Message: "web_fetch host is empty"}
	}
	ips, err := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	if err != nil {
		return err
	}
	if len(ips) == 0 {
		return EgressViolation{Message: "web_fetch host did not resolve"}
	}
	for _, addr := range ips {
		if err := p.ValidateIP(addr.IP); err != nil {
			return err
		}
	}
	return nil
}

func (p EgressPolicy) ValidateIP(ip net.IP) error {
	if p.AllowPrivateNetworks {
		return nil
	}
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return EgressViolation{Message: "web_fetch resolved to a disallowed network address"}
	}
	return nil
}
