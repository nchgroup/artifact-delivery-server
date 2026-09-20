package server

import (
	stderrors "errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

func resolveClientAddress(r *http.Request, trustedProxies interface {
	Enabled() bool
	Contains(netip.Addr) bool
}, cloudflareProxies interface {
	Enabled() bool
	Contains(netip.Addr) bool
}) (netip.Addr, error) {
	peer, err := parseRemoteAddress(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, err
	}
	if trustedProxies == nil || !trustedProxies.Enabled() || !trustedProxies.Contains(peer) {
		return peer, nil
	}
	if cloudflareProxies != nil && cloudflareProxies.Enabled() && cloudflareProxies.Contains(peer) {
		if values := r.Header.Values("CF-Connecting-IP"); len(values) > 0 {
			if len(values) != 1 {
				return netip.Addr{}, stderrors.New("CF-Connecting-IP must contain exactly one value")
			}
			address, parseErr := netip.ParseAddr(strings.TrimSpace(values[0]))
			if parseErr != nil {
				return netip.Addr{}, stderrors.New("CF-Connecting-IP contains an invalid address")
			}
			return normalizeAddress(address), nil
		}
	}

	forwardedValues := r.Header.Values("X-Forwarded-For")
	if len(forwardedValues) == 0 {
		return peer, nil
	}
	addresses := make([]netip.Addr, 0, len(forwardedValues)+1)
	for _, value := range forwardedValues {
		for _, rawAddress := range strings.Split(value, ",") {
			if len(addresses) >= 32 {
				return netip.Addr{}, stderrors.New("X-Forwarded-For contains too many addresses")
			}
			address, parseErr := netip.ParseAddr(strings.TrimSpace(rawAddress))
			if parseErr != nil {
				return netip.Addr{}, stderrors.New("X-Forwarded-For contains an invalid address")
			}
			addresses = append(addresses, normalizeAddress(address))
		}
	}
	if len(addresses) == 0 {
		return netip.Addr{}, stderrors.New("X-Forwarded-For is empty")
	}
	for index := len(addresses) - 1; index >= 0; index-- {
		if !trustedProxies.Contains(addresses[index]) {
			return addresses[index], nil
		}
	}
	return addresses[0], nil
}

func parseRemoteAddress(remoteAddress string) (netip.Addr, error) {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		return netip.Addr{}, stderrors.New("remote address does not include a valid host and port")
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, stderrors.New("remote address contains an invalid IP address")
	}
	return normalizeAddress(address), nil
}

func normalizeAddress(address netip.Addr) netip.Addr {
	return address.Unmap().WithZone("")
}

func remoteHost(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	return host
}

func allowedHost(requestHost, configuredHost string) bool {
	requestHost = strings.TrimSpace(requestHost)
	configuredHost = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(configuredHost)), ".")
	if requestHost == "" || configuredHost == "" {
		return false
	}
	host := requestHost
	if parsedHost, _, err := net.SplitHostPort(requestHost); err == nil {
		host = parsedHost
	} else if strings.Count(requestHost, ":") > 0 {
		return false
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	return host == configuredHost
}
