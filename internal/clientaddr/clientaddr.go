// Package clientaddr resolves the address of the client a request came from.
//
// Behind a reverse proxy every request arrives from the proxy, so anything
// keyed on the peer address (the Web API rate limiter, the access log) would
// see one client. A proxy named in the trusted list reports the client in
// X-Forwarded-For; any other peer is the client, and its X-Forwarded-For is
// ignored because the peer wrote it.
package clientaddr

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Resolver turns a request's peer and forwarding header into its client.
type Resolver struct {
	trusted []netip.Prefix
}

// Parse reads a comma-separated list of proxy addresses and CIDR ranges. An
// empty list trusts no proxy.
func Parse(list string) (Resolver, error) {
	var resolver Resolver
	for _, entry := range strings.Split(list, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			address, err := netip.ParseAddr(entry)
			if err != nil {
				return Resolver{}, fmt.Errorf("trusted proxy %q is neither an address nor a CIDR range", entry)
			}
			address = address.Unmap()
			entry = netip.PrefixFrom(address, address.BitLen()).String()
		}
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			return Resolver{}, fmt.Errorf("trusted proxy %q is neither an address nor a CIDR range", entry)
		}
		if prefix.Addr().Is4In6() {
			return Resolver{}, fmt.Errorf("trusted proxy range %q is IPv4-mapped; state the IPv4 range", entry)
		}
		resolver.trusted = append(resolver.trusted, prefix.Masked())
	}
	return resolver, nil
}

func (r Resolver) trusts(address netip.Addr) bool {
	for _, prefix := range r.trusted {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

// Client returns the address of the client behind the request: the rightmost
// X-Forwarded-For entry that is not itself a trusted proxy when the peer is
// one, otherwise the peer.
func (r Resolver) Client(request *http.Request) (netip.Addr, error) {
	peerPort, err := netip.ParseAddrPort(request.RemoteAddr)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("peer address %q is not host:port", request.RemoteAddr)
	}
	peer := peerPort.Addr().Unmap()
	if !r.trusts(peer) {
		return peer, nil
	}
	entries := strings.Split(strings.Join(request.Header.Values("X-Forwarded-For"), ","), ",")
	for index := len(entries) - 1; index >= 0; index-- {
		entry := strings.TrimSpace(entries[index])
		if entry == "" {
			continue
		}
		address, err := netip.ParseAddr(entry)
		if err != nil {
			return netip.Addr{}, fmt.Errorf("trusted proxy %s forwarded %q, which is not an address", peer, entry)
		}
		address = address.Unmap()
		if !r.trusts(address) {
			return address, nil
		}
	}
	return peer, nil
}

// Middleware replaces each request's RemoteAddr with its client, so every
// handler reading RemoteAddr keys on the client. A trusted proxy that forwards
// something other than an address is refused rather than guessed past.
func (r Resolver) Middleware(next http.Handler) http.Handler {
	if len(r.trusted) == 0 {
		return next
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		client, err := r.Client(request)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		forwarded := request.Clone(request.Context())
		forwarded.RemoteAddr = net.JoinHostPort(client.String(), "0")
		next.ServeHTTP(writer, forwarded)
	})
}
