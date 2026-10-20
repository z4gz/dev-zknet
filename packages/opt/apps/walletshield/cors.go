// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"net/http"
	"strings"

	"github.com/ZeroKnowledgeNetwork/opt/server_plugins/cbor_plugins/rpc_proxy"
)

// exposedHeaders lets a browser read the failure marker and every relayed
// upstream header that is not CORS-safelisted.
var exposedHeaders = strings.Join(append([]string{rpc_proxy.ErrorHeader}, rpc_proxy.RelayedHeaders...), ", ")

func isPreflight(req *http.Request) bool {
	return req.Method == http.MethodOptions &&
		req.Header.Get("Origin") != "" &&
		req.Header.Get("Access-Control-Request-Method") != ""
}

// withCORS lets any browser origin, including the opaque origin "null", call
// WalletShield and read its replies, and answers CORS preflights locally. A
// preflight concerns this local listener, not the upstream, so it is never
// forwarded over the mixnet.
func withCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if isPreflight(req) {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
			if h := req.Header.Get("Access-Control-Request-Headers"); h != "" {
				w.Header().Set("Access-Control-Allow-Headers", h)
			}
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Access-Control-Expose-Headers", exposedHeaders)
		next(w, req)
	}
}
