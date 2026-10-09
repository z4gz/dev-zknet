// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// corsPolicy decides which browser origins may read WalletShield's responses.
//
// The zero value admits no origin, which is how WalletShield behaved before it
// had a policy: a cross-origin page could send it a request but never read the
// reply. Origins are matched exactly; there is no wildcard.
//
// The opaque origin "null" is admitted only when listed. It is what a sandboxed
// iframe and the workers it spawns present, but any page can create such a
// frame, so admitting "null" lets every web page read this proxy's replies.
type corsPolicy struct {
	origins map[string]bool
}

// parseCORSOrigins parses a comma-separated list of exact origins, each either
// "null" or scheme://host[:port] with no path.
func parseCORSOrigins(list string) (corsPolicy, error) {
	p := corsPolicy{origins: map[string]bool{}}
	for _, o := range strings.Split(list, ",") {
		o = strings.TrimSpace(o)
		switch {
		case o == "":
			continue
		case o == "*":
			return corsPolicy{}, errors.New(`cors origin "*" is not supported; list each origin`)
		case o == "null":
		case !strings.HasPrefix(o, "http://") && !strings.HasPrefix(o, "https://"):
			return corsPolicy{}, fmt.Errorf("cors origin %q: must be null or start with http:// or https://", o)
		case strings.Contains(strings.SplitN(o, "://", 2)[1], "/"):
			return corsPolicy{}, fmt.Errorf("cors origin %q: an origin has no path", o)
		}
		p.origins[o] = true
	}
	return p, nil
}

func (p corsPolicy) allows(origin string) bool {
	return origin != "" && p.origins[origin]
}

func isPreflight(req *http.Request) bool {
	return req.Method == http.MethodOptions &&
		req.Header.Get("Origin") != "" &&
		req.Header.Get("Access-Control-Request-Method") != ""
}

// wrap answers CORS preflights locally and marks replies to admitted origins
// readable. A preflight concerns this local listener, not the upstream, so it
// is never forwarded over the mixnet: doing so spends a round trip and hands
// the exit the browser's preflight for an answer that cannot apply here.
func (p corsPolicy) wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		origin := req.Header.Get("Origin")
		allowed := p.allows(origin)
		if origin != "" {
			w.Header().Add("Vary", "Origin")
		}
		if isPreflight(req) {
			if allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				w.Header().Set("Access-Control-Max-Age", "600")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if allowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		next(w, req)
	}
}
