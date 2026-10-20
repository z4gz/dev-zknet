package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"
	"gopkg.in/op/go-logging.v1"

	"github.com/katzenpost/katzenpost/core/log"
	"github.com/katzenpost/katzenpost/server/cborplugin"

	"github.com/ZeroKnowledgeNetwork/opt/common"
	"github.com/ZeroKnowledgeNetwork/opt/server_plugins/cbor_plugins/rpc_proxy"
)

// DefaultUserForwardPayloadLength is used when the mix server does not give
// the plugin its Sphinx geometry (plugin protocol 1); it should then match
// the value passed to genconfig.
const DefaultUserForwardPayloadLength = 30000

// payloadMargin is kept free of the UserForwardPayloadLength in each reply.
const payloadMargin = 20

func main() {
	var logLevel string
	var logDir string
	var destURL string
	var configPath string

	flag.StringVar(&logDir, "log_dir", "", "logging directory")
	flag.StringVar(&logLevel, "log_level", "DEBUG", "logging level could be set to: DEBUG, INFO, NOTICE, WARNING, ERROR, CRITICAL")
	flag.StringVar(&destURL, "dest_url", "", "destination URL for reverse proxying to")
	flag.StringVar(&configPath, "config", "", "file path to the TOML configuration file")

	flag.Parse()

	if configPath == "" {
		panic("config MUST be specified!")
	}

	cfg, err := rpc_proxy.LoadFile(configPath)
	if err != nil {
		panic(err)
	}

	// Ensure that the log directory exists.
	s, err := os.Stat(logDir)
	if os.IsNotExist(err) {
		fmt.Printf("Log directory '%s' doesn't exist.", logDir)
		os.Exit(1)
	}
	if !s.IsDir() {
		fmt.Println("Log directory must actually be a directory.")
		os.Exit(1)
	}

	// Log to a file.
	logFile := path.Join(logDir, fmt.Sprintf("rpc_proxy.%d.log", os.Getpid()))
	logBackend, err := log.New(logFile, logLevel, false)
	if err != nil {
		panic(err)
	}
	serverLog := logBackend.GetLogger("rpc_proxy")

	// start service
	tmpDir, err := os.MkdirTemp("", "rpc_proxy")
	if err != nil {
		panic(err)
	}
	socketFile, fromHost := cborplugin.HostSocketPath(filepath.Join(tmpDir, fmt.Sprintf("%d.rpc_proxy.socket", os.Getpid())))

	host, err := cborplugin.HostInfo()
	if err != nil {
		panic(err)
	}
	payloadLength := DefaultUserForwardPayloadLength
	if host.UserForwardPayloadLength > 0 {
		payloadLength = host.UserForwardPayloadLength
	}
	serverLog.Noticef("UserForwardPayloadLength %d (from mix server: %v)", payloadLength, host.UserForwardPayloadLength > 0)

	var server *cborplugin.Server

	h := New(cfg, serverLog, payloadLength-payloadMargin)
	server = cborplugin.NewServer(serverLog, socketFile, new(cborplugin.RequestFactory), h)

	// a mix server speaking plugin protocol 2 gave us the socket path;
	// otherwise emit it to stdout, which tells the mix server where to connect
	if !fromHost {
		fmt.Printf("%s\n", socketFile)
	}

	server.Accept()
	server.Wait()

	os.Remove(socketFile)
}

type proxyRequestHandler struct {
	cfg            *rpc_proxy.Config
	log            *logging.Logger
	maxPayloadSize int
	networks       *http.Client
	user           *http.Client

	// slots holds one token per request in flight.
	slots chan struct{}

	write func(cborplugin.Command)
}

func New(cfg *rpc_proxy.Config, log *logging.Logger, maxPayloadSize int) *proxyRequestHandler {
	return &proxyRequestHandler{
		cfg:            cfg,
		log:            log,
		maxPayloadSize: maxPayloadSize,
		networks:       rpc_proxy.NewNetworksClient(),
		user:           rpc_proxy.NewUserClient(cfg.Policy, cfg.Roots),
		slots:          make(chan struct{}, rpc_proxy.ExitConcurrency),
	}
}

// OnCommand takes one command from the mix server. The plugin server calls it
// from a single goroutine, so it never waits on an upstream: each request is
// served on its own goroutine, at most ExitConcurrency at once.
func (s *proxyRequestHandler) OnCommand(cmd cborplugin.Command) error {
	r, ok := cmd.(*cborplugin.Request)
	if !ok {
		errMsg := "invalid Command type"
		s.log.Error(errMsg)
		return errors.New(errMsg)
	}

	// plugin protocol 2: the mix server asks for parameters to publish
	// in its descriptor; we have none
	if r.IsParametersRequest {
		go s.write(cborplugin.NewParametersResponse(nil))
		return nil
	}

	if r.SURB == nil {
		errMsg := "no SURB, cannot reply"
		s.log.Error(errMsg)
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonExitRefused, errMsg)
	}

	select {
	case s.slots <- struct{}{}:
	default:
		s.log.Warningf("busy: %d requests in flight", rpc_proxy.ExitConcurrency)
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonExitBusy, "the exit is serving its maximum number of requests")
	}
	go func() {
		defer func() { <-s.slots }()
		s.serve(r)
	}()
	return nil
}

// serve answers one request. A panic costs that request, never the plugin:
// the mix server does not restart a plugin that dies.
func (s *proxyRequestHandler) serve(r *cborplugin.Request) {
	defer func() {
		if p := recover(); p != nil {
			s.log.Errorf("serve panic: %v", p)
			s.sendError(r.ID, r.SURB, rpc_proxy.ReasonExitRefused, "internal error")
		}
	}()
	if err := s.handle(r); err != nil {
		s.log.Debugf("request %d: %s", r.ID, err)
	}
}

func (s *proxyRequestHandler) handle(r *cborplugin.Request) error {
	// the padding bytes were not stripped because
	// without parsing the start of Payload we wont
	// know how long it is, so we will use a streaming
	// decoder and simply return the first cbor object
	// and then discard the decoder and buffer
	req := &rpc_proxy.Request{}
	dec := cbor.NewDecoder(bytes.NewReader(r.Payload))
	if err := dec.Decode(req); err != nil {
		s.log.Errorf("dec.Decode(req) failed: %s", err)
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonExitRefused, "Error decoding request")
	}

	requestPayload, err := rpc_proxy.DecompressLimited(req.Payload, rpc_proxy.MaxDecompressedRequestBytes)
	if errors.Is(err, rpc_proxy.ErrTooLarge) {
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonTooLarge, "request too large")
	}
	if err != nil {
		s.log.Errorf("decompress failed: %s", err)
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonExitRefused, "Failed to decompress request")
	}
	request, err := rpc_proxy.ReadInnerRequest(requestPayload)
	if err != nil {
		s.log.Errorf("request %d: refused inner request: %s", r.ID, err)
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonExitRefused, fmt.Sprintf("inner request refused: %s", err))
	}

	s.log.Debugf("Request URL Path: %s", request.URL.Path)

	if req.URL != "" {
		return s.serveUserURL(r, req.URL, request)
	}

	if match, _ := regexp.MatchString("^/_/probe.*", request.URL.Path); match {
		segments := strings.Split(request.URL.Path, "/")
		if len(segments) >= 4 {
			responseDelay, err := strconv.Atoi(segments[3])
			if err == nil && responseDelay > 0 {
				delay := min(time.Duration(responseDelay)*time.Second, rpc_proxy.UpstreamTimeout)
				s.log.Infof("probe: delaying response for %s", delay)
				time.Sleep(delay)
			}
		}
		return s.answerProbe(r)
	}

	uri := request.URL.Path
	uri = uri[1:]
	target, ok := s.cfg.Networks[uri]
	if !ok {
		s.log.Error("URI not matched with config Networks")
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonExitRefused, "URI not configured in networks")
	}

	return s.relay(r, s.networks, target, request)
}

// serveUserURL fetches the upstream the user chose, under the destination
// policy. Only the URL's host is logged: a provider URL often embeds an API
// key.
func (s *proxyRequestHandler) serveUserURL(r *cborplugin.Request, rawURL string, request *http.Request) error {
	if !s.cfg.UserURLs.Enable {
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonProviderSelectionOff, "this exit does not serve user-chosen provider URLs")
	}
	target, err := rpc_proxy.CheckUserRequest(rawURL, request.Method, request.Header.Get("Content-Type"))
	if err != nil {
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonExitRefused, err.Error())
	}
	s.log.Infof("request %d: provider host %s", r.ID, target.Host)
	return s.relay(r, s.user, target.String(), request)
}

// relay sends the inner request's body and Content-Type to target and
// answers with the upstream's status, relayed headers and body.
func (s *proxyRequestHandler) relay(r *cborplugin.Request, client *http.Client, target string, request *http.Request) error {
	body, err := rpc_proxy.ReadLimited(request.Body, rpc_proxy.MaxRequestBodyBytes)
	if errors.Is(err, rpc_proxy.ErrTooLarge) {
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonTooLarge, "request body too large")
	}
	if err != nil {
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonExitRefused, "Failed to read request body")
	}

	newRequest, err := http.NewRequest(request.Method, target, bytes.NewReader(body))
	if err != nil {
		s.log.Errorf("http.NewRequest failed: %s", err)
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonExitRefused, "Failed to create HTTP request")
	}
	newRequest.Header.Set("Content-Type", request.Header.Get("Content-Type"))

	resp, err := client.Do(newRequest)
	if errors.Is(err, rpc_proxy.ErrDestinationRefused) {
		// Refused at dial: nothing went upstream.
		s.log.Warningf("request %d: %s", r.ID, rpc_proxy.LogSafe(err))
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonExitRefused, rpc_proxy.LogSafe(err))
	}
	if err != nil {
		s.log.Errorf("upstream request failed: %s", rpc_proxy.LogSafe(err))
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonUpstreamUnreachable, "HTTP request failed")
	}
	defer resp.Body.Close()

	respBody, err := rpc_proxy.ReadLimited(resp.Body, rpc_proxy.MaxResponseBodyBytes)
	if errors.Is(err, rpc_proxy.ErrTooLarge) {
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonTooLarge, "HTTP response is too big")
	}
	if err != nil {
		s.log.Errorf("reading upstream reply failed: %s", rpc_proxy.LogSafe(err))
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonUpstreamUnreachable, "reading the upstream reply failed")
	}

	s.log.Debugf("Reply status %d, %d bytes", resp.StatusCode, len(respBody))

	responsePayload, err := common.CompressData(respBody)
	if err != nil {
		s.log.Errorf("common.CompressData failed: %s", err)
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonUpstreamUnreachable, "Failed to compress response")
	}
	response := &rpc_proxy.Response{
		Payload: responsePayload,
		Status:  resp.StatusCode,
		Header:  rpc_proxy.FilterHeaders(resp.Header),
	}

	payload, err := cbor.Marshal(response)
	if err != nil {
		s.log.Errorf("cbor.Marshal(response) failed: %s", err)
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonUpstreamUnreachable, "Error marshaling response")
	}
	if len(payload) > s.maxPayloadSize {
		s.log.Error("HTTP response exceeds max Sphinx payload")
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonTooLarge, "HTTP response is too big")
	}

	go s.write(&cborplugin.Response{ID: r.ID, SURB: r.SURB, Payload: payload})
	return nil
}

// answerProbe answers a probe, an exit round trip without an upstream, as a
// relayed reply would be: status 200 with an empty body.
func (s *proxyRequestHandler) answerProbe(r *cborplugin.Request) error {
	empty, err := common.CompressData(nil)
	if err != nil {
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonUpstreamUnreachable, "Failed to compress response")
	}
	payload, err := cbor.Marshal(&rpc_proxy.Response{Payload: empty, Status: http.StatusOK})
	if err != nil {
		return s.sendError(r.ID, r.SURB, rpc_proxy.ReasonUpstreamUnreachable, "Error marshaling response")
	}
	go s.write(&cborplugin.Response{ID: r.ID, SURB: r.SURB, Payload: payload})
	return nil
}

// sendError replies with an exit-originated failure. reason is one of the
// rpc_proxy.Reason* values.
func (s *proxyRequestHandler) sendError(id uint64, surb []byte, reason, msg string) error {
	payload, _ := cbor.Marshal(&rpc_proxy.Response{Error: msg, Reason: reason})
	go s.write(&cborplugin.Response{ID: id, SURB: surb, Payload: payload})
	return errors.New(msg)
}

// RegisterConsumer is required by our plugin interface
func (s *proxyRequestHandler) RegisterConsumer(svr *cborplugin.Server) {
	s.write = svr.Write
}
