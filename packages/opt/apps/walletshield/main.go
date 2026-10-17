// SPDX-FileCopyrightText: © 2023 David Stainton
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/carlmjohnson/versioninfo"
	"github.com/charmbracelet/log"
	"github.com/fxamacker/cbor/v2"

	"github.com/katzenpost/hpqc/hash"
	"github.com/katzenpost/hpqc/rand"

	"github.com/katzenpost/katzenpost/client"
	"github.com/katzenpost/katzenpost/client/config"
	"github.com/katzenpost/katzenpost/client/thin"
	ttransport "github.com/katzenpost/katzenpost/client/thin/transport"
	ctransport "github.com/katzenpost/katzenpost/client/transport"
	sConstants "github.com/katzenpost/katzenpost/core/sphinx/constants"

	"github.com/ZeroKnowledgeNetwork/opt/common"
	"github.com/ZeroKnowledgeNetwork/opt/server_plugins/cbor_plugins/http_proxy"
)

var (
	timeout          = 20 // (default) context timeout
	ProxyHTTPService = "http_proxy"

	// Note: UserForwardPayloadLength should match the same value passed to genconfig.
	UserForwardPayloadLength = 30000

	// errTooLarge marks a request that does not fit one mixnet payload.
	errTooLarge = errors.New("payload too large")
)

// healthPath is answered locally, without a mixnet round trip, so a client can
// tell an absent WalletShield from an unreachable mixnet.
const healthPath = "/_/health"

// failureStatus is the HTTP status of each reply WalletShield originates
// instead of relaying from the upstream.
var failureStatus = map[string]int{
	http_proxy.ReasonMixTimeout:          http.StatusGatewayTimeout,
	http_proxy.ReasonExitRefused:         http.StatusBadGateway,
	http_proxy.ReasonUpstreamUnreachable: http.StatusBadGateway,
	http_proxy.ReasonTooLarge:            http.StatusRequestEntityTooLarge,
	http_proxy.ReasonBadReply:            http.StatusBadGateway,
	http_proxy.ReasonExitBusy:            http.StatusServiceUnavailable,
}

// legacyErrorReasons classifies, by prefix, the Error strings that an exit
// predating Response.Reason sends after it has contacted the upstream. Any
// other Error from such an exit is ReasonExitRefused.
var legacyErrorReasons = map[string]string{
	"HTTP request failed":          http_proxy.ReasonUpstreamUnreachable,
	"io.ReadAll(resp.Body) failed": http_proxy.ReasonUpstreamUnreachable,
	"Failed to compress response":  http_proxy.ReasonUpstreamUnreachable,
	"HTTP response is too big":     http_proxy.ReasonTooLarge,
}

func sendRequest(thin *thin.ThinClient, httpRequestBytes []byte) ([]byte, error) {
	// Compress the HTTP request
	compressedPayload, err := common.CompressData(httpRequestBytes)
	if err != nil {
		return nil, fmt.Errorf("common.CompressData failed: %w", err)
	}

	// Create the request wrapper
	request := &http_proxy.Request{
		Payload: compressedPayload,
	}

	// Marshal to CBOR
	blob, err := cbor.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("cbor.Marshal failed: %w", err)
	}

	// Validate payload size
	if len(blob) > UserForwardPayloadLength {
		return nil, fmt.Errorf("%w: payload size %d exceeds maximum %d bytes", errTooLarge, len(blob), UserForwardPayloadLength)
	}

	surbID := &[sConstants.SURBIDLength]byte{}
	_, err = rand.Reader.Read(surbID[:])
	if err != nil {
		panic(err)
	}

	// Select a target service node and compute the DestinationIdHash
	target, err := thin.GetService(ProxyHTTPService)
	if err != nil {
		return nil, fmt.Errorf("thin.GetService failed: %w", err)
	}
	nodeId := hash.Sum256(target.MixDescriptor.IdentityKey)

	timeoutCtx, cancel := context.WithTimeout(context.TODO(), time.Duration(timeout)*time.Second)
	defer cancel()
	return thin.BlockingSendMessage(timeoutCtx, blob, &nodeId, target.RecipientQueueID)
}

type Server struct {
	log    *log.Logger
	daemon *client.Daemon
	thin   *thin.ThinClient

	// send carries one serialized HTTP request over the mixnet and returns the
	// raw reply.
	send func(httpRequestBytes []byte) ([]byte, error)
}

// setListenAddress overrides the address of the client daemon's configured
// thin-client listen transport. The transport flavor is chosen by the config
// file's [Listen] subtable; only the address is overridable here.
func setListenAddress(c *ctransport.ListenConfig, addr string) error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("cannot override client listen address: %w", err)
	}
	switch {
	case c.Unix != nil:
		c.Unix.Address = addr
		c.Unix.Addresses = nil
	case c.Tcp != nil:
		c.Tcp.Address = addr
	case c.Ws != nil:
		c.Ws.Address = addr
	}
	return nil
}

// setDialAddress overrides the address the thin client dials, mirroring
// setListenAddress for the [Dial] subtable.
func setDialAddress(c *ttransport.DialConfig, addr string) error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("cannot override thin client dial address: %w", err)
	}
	switch {
	case c.Unix != nil:
		c.Unix.Address = addr
	case c.Tcp != nil:
		c.Tcp.Address = addr
	case c.Ws != nil:
		c.Ws.Address = addr
	}
	return nil
}

func main() {
	var logLevel string
	var listenAddr string
	var listenAddrClient string
	var configPath string
	var delayStart int
	var testProbe bool
	var testProbeCount int
	var testProbeResponseDelay int
	var testProbeSendDelay int
	var thinClientOnly bool

	flag.StringVar(&configPath, "config", "", "file path of the client configuration TOML file")
	flag.IntVar(&delayStart, "delay_start", 0, "max random seconds to delay start")
	flag.StringVar(&logLevel, "log_level", "DEBUG", "logging level could be set to: DEBUG, INFO, WARNING, ERROR, CRITICAL")
	flag.StringVar(&listenAddr, "listen", "", "local socket to listen HTTP on")
	flag.StringVar(&listenAddrClient, "listen_client", "", "local network address for the client daemon")
	flag.BoolVar(&testProbe, "probe", false, "send test probes instead of handling requests")
	flag.IntVar(&testProbeCount, "probe_count", 1, "number of test probes to send")
	flag.IntVar(&testProbeResponseDelay, "probe_response_delay", 0, "test probe response deplay")
	flag.IntVar(&testProbeSendDelay, "probe_send_delay", 10, "test probe delay between probes")
	flag.IntVar(&timeout, "timeout", timeout, "seconds to wait for a request")
	flag.BoolVar(&thinClientOnly, "thin", false, "use thin client mode (connect to existing daemon)")
	flag.Parse()

	if listenAddr == "" && !testProbe {
		panic("listen flag must be set")
	}
	if configPath == "" {
		panic("config flag must be set")
	}

	// logging
	level, err := log.ParseLevel(logLevel)
	if err != nil {
		panic(err)
	}
	mylog := log.NewWithOptions(os.Stdout, log.Options{
		Prefix: "walletshield:",
		Level:  level,
	})

	if delayStart > 0 {
		d := rand.NewMath().Intn(delayStart)
		mylog.Infof("Delaying start for %d seconds...", d)
		time.Sleep(time.Duration(d) * time.Second)
	}

	// start client daemon
	var d *client.Daemon
	var cfgThin *thin.Config
	if !thinClientOnly {
		cfg, err := config.LoadFile(configPath)
		if err != nil {
			if _, thinErr := thin.LoadFile(configPath); thinErr == nil {
				err = fmt.Errorf("%w: %s is a thin client config, which requires -thin", err, configPath)
			}
			panic(err)
		}

		if listenAddrClient != "" {
			if err := setListenAddress(cfg.Listen, listenAddrClient); err != nil {
				panic(err)
			}
		}

		d, err = client.NewDaemon(cfg)
		if err != nil {
			panic(err)
		}
		err = d.Start()
		if err != nil {
			panic(err)
		}

		cfgThin = thin.FromConfig(cfg)

		fmt.Println("Sleeping for 3 seconds to let the client daemon startup...")
		time.Sleep(time.Second * 3) // XXX ugly hack but works: FIXME
	} else {
		cfgThin, err = thin.LoadFile(configPath)
		if err != nil {
			panic(fmt.Errorf("failed to open thin client config: %s", err))
		}

		if listenAddrClient != "" {
			if err := setDialAddress(cfgThin.Dial, listenAddrClient); err != nil {
				panic(err)
			}
		}
	}

	logging := &config.Logging{
		Disable: false,
		Level:   level.String(),
	}

	thin := thin.NewThinClient(cfgThin, logging)
	err = thin.Dial()
	if err != nil {
		panic(err)
	}

	// http server
	server := &Server{
		log:    mylog,
		thin:   thin,
		daemon: d,
	}
	server.send = func(b []byte) ([]byte, error) { return sendRequest(thin, b) }

	if testProbe {
		server.SendTestProbes(testProbeSendDelay, testProbeCount, testProbeResponseDelay)
		d.Shutdown()
	} else {
		http.HandleFunc("/", withCORS(server.Handler))
		err := http.ListenAndServe(listenAddr, nil)
		if err != nil {
			// Check if the error is related to the port being in use
			if strings.Contains(err.Error(), "bind: address already in use") {
				mylog.Errorf("Cannot start server: Listen port %s is already in use. Please check if another instance of walletshield is running or use another port.", listenAddr)
			} else {
				mylog.Errorf("Failed to start HTTP server: %s", err)
			}
		}
	}
}

func (s *Server) Handler(w http.ResponseWriter, req *http.Request) {
	s.log.Infof("Received HTTP request for %s", req.URL)

	if req.Method == http.MethodGet && req.URL.Path == healthPath {
		writeJSON(w, http.StatusOK, map[string]string{"version": versioninfo.Short()})
		return
	}

	myurl, err := url.Parse(req.RequestURI)
	if err != nil {
		s.log.Errorf("url.Parse(req.RequestURI) failed: %s", err)
		return
	}
	req.URL = myurl
	req.RequestURI = ""

	buf := new(bytes.Buffer)
	req.Write(buf)

	s.log.Debugf("RAW HTTP REQUEST:\n%s", string(buf.Bytes()))

	rawReply, err := s.send(buf.Bytes())
	if err != nil {
		s.log.Errorf("Failed to send message: %s", err)
		if errors.Is(err, errTooLarge) {
			writeFailure(w, http_proxy.ReasonTooLarge, err.Error())
		} else {
			writeFailure(w, http_proxy.ReasonMixTimeout, err.Error())
		}
		return
	}

	// use the streaming decoder and simply return the first cbor object
	// and then discard the decoder and buffer
	response := new(http_proxy.Response)
	dec := cbor.NewDecoder(bytes.NewReader(rawReply))
	err = dec.Decode(response)
	if err != nil {
		s.log.Errorf("Failed to decode response: %s", err)
		writeFailure(w, http_proxy.ReasonBadReply, fmt.Sprintf("cannot decode exit reply: %s", err))
		return
	}

	if response.Error != "" {
		s.log.Errorf("Response Error: %s", response.Error)
		writeFailure(w, exitReason(response), response.Error)
		return
	}

	responsePayload, err := common.DecompressData(response.Payload)
	if err != nil {
		s.log.Errorf("common.DecompressData failed: %s", err)
		writeFailure(w, http_proxy.ReasonBadReply, fmt.Sprintf("cannot decompress exit reply: %s", err))
		return
	}

	status := response.Status
	header := http_proxy.FilterHeaders(response.Header)
	if status == 0 {
		// An exit that predates Response.Status relays neither status nor
		// headers; keep the reply that WalletShield gave before.
		status = http.StatusOK
		header.Set("Content-Type", "application/json")
	}
	if status < 200 || status > 599 {
		s.log.Errorf("Invalid upstream status %d", status)
		writeFailure(w, http_proxy.ReasonBadReply, fmt.Sprintf("invalid upstream status %d", status))
		return
	}

	s.log.Infof("Response %d: %s", status, responsePayload)

	for k, v := range header {
		w.Header()[k] = v
	}
	if _, ok := header["Content-Type"]; !ok {
		// Relay the upstream's absent Content-Type instead of sniffing one.
		w.Header()["Content-Type"] = nil
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(responsePayload)))
	w.WriteHeader(status)
	w.Write(responsePayload)
}

// exitReason returns the Reason of an exit error reply, classifying the Error
// string of an exit that predates Response.Reason.
func exitReason(r *http_proxy.Response) string {
	if _, ok := failureStatus[r.Reason]; ok {
		return r.Reason
	}
	for prefix, reason := range legacyErrorReasons {
		if strings.HasPrefix(r.Error, prefix) {
			return reason
		}
	}
	return http_proxy.ReasonExitRefused
}

// writeFailure answers with a reply WalletShield originates, marked by
// ErrorHeader so it is never mistaken for an upstream reply.
func writeFailure(w http.ResponseWriter, reason, message string) {
	type failure struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	w.Header().Set(http_proxy.ErrorHeader, reason)
	writeJSON(w, failureStatus[reason], map[string]failure{"error": {reason, message}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.WriteHeader(status)
	w.Write(body)
}

func (s *Server) SendTestProbes(testProbeSendDelay int, testProbeCount int, testProbeResponseDelay int) {
	url := fmt.Sprintf("http://nowhere/_/probe/%d", testProbeResponseDelay)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		s.log.Errorf("http.NewRequest failed: %s", err)
		return
	}
	buf := new(bytes.Buffer)
	req.Write(buf)
	httpRequestBytes := buf.Bytes()

	var packetsTransmitted, packetsReceived int
	var rttMin, rttMax, rttTotal float64
	rttMin = math.MaxFloat64

	for {
		packetsTransmitted++
		t := time.Now()

		_, err = sendRequest(s.thin, httpRequestBytes)
		elapsed := time.Since(t).Seconds()
		if err != nil {
			s.log.Errorf("Probe failed after %.2fs: %s", elapsed, err)
		} else {
			packetsReceived++
			rttTotal += elapsed
			if elapsed < rttMin {
				rttMin = elapsed
			}
			if elapsed > rttMax {
				rttMax = elapsed
			}
		}

		packetLoss := float64(packetsTransmitted-packetsReceived) / float64(packetsTransmitted) * 100
		rttAvg := rttTotal / float64(packetsReceived)
		if packetsReceived == 0 {
			rttMin = math.NaN()
		}
		s.log.Infof("Probe packet transmitted/received/loss = %d/%d/%.1f%% | rtt min/avg/max = %.2f/%.2f/%.2f s",
			packetsTransmitted, packetsReceived, packetLoss, rttMin, rttAvg, rttMax)

		// probe indefinitely if testProbeCount is 0
		if testProbeCount != 0 && packetsTransmitted >= testProbeCount {
			os.Exit(0)
		}

		time.Sleep(time.Duration(testProbeSendDelay) * time.Second)
	}
}
