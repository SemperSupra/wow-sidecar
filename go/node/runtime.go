package node

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/embodiment"
	"github.com/SemperSupra/wow-sidecar/go/protocol"
)

var sourceRevisionRE = regexp.MustCompile(`^[0-9a-f]{40}package node

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/embodiment"
	"github.com/SemperSupra/wow-sidecar/go/protocol"
)

)

const (
	defaultGenerationStateFile = "/var/lib/wow-sidecar/generation.json"
	defaultListenAddr          = "127.0.0.1:8080"
	defaultLeaseTTL            = 60 * time.Second
)

type Config struct {
	NodeID              string
	Locality            string
	GenerationStateFile string
	ListenAddr          string
	LeaseTTL            time.Duration
	PublicEndpoint      string
	EndpointKind        string
	EndpointAuth        string
	Capabilities        []string
}

type Runtime struct {
	config         Config
	generation     uint64
	incarnationID string
	sourceRevision string
	buildVersion   string
	now            func() time.Time
}

func ConfigFromEnv() (Config, error) {
	nodeID := strings.TrimSpace(os.Getenv("WOW_NODE_ID"))
	if nodeID == "" {
		return Config{}, fmt.Errorf("WOW_NODE_ID is required")
	}
	locality := strings.TrimSpace(os.Getenv("WOW_LOCALITY"))
	if locality != "sovereign" && locality != "cloud" {
		return Config{}, fmt.Errorf("WOW_LOCALITY must be sovereign or cloud")
	}
	stateFile := strings.TrimSpace(os.Getenv("WOW_GENERATION_STATE_FILE"))
	if stateFile == "" {
		stateFile = defaultGenerationStateFile
	}
	if !strings.HasPrefix(stateFile, "/") {
		return Config{}, fmt.Errorf("WOW_GENERATION_STATE_FILE must be absolute")
	}

	listenAddr := strings.TrimSpace(os.Getenv("WOW_LISTEN_ADDR"))
	if listenAddr == "" {
		listenAddr = defaultListenAddr
	}
	if _, _, err := net.SplitHostPort(listenAddr); err != nil {
		return Config{}, fmt.Errorf("invalid WOW_LISTEN_ADDR: %w", err)
	}

	leaseTTL := defaultLeaseTTL
	if raw := strings.TrimSpace(os.Getenv("WOW_LEASE_SECONDS")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 10 || seconds > 3600 {
			return Config{}, fmt.Errorf("WOW_LEASE_SECONDS must be between 10 and 3600")
		}
		leaseTTL = time.Duration(seconds) * time.Second
	}

	publicEndpoint := strings.TrimSpace(os.Getenv("WOW_PUBLIC_ENDPOINT"))
	endpointKind := strings.TrimSpace(os.Getenv("WOW_ENDPOINT_KIND"))
	endpointAuth := strings.TrimSpace(os.Getenv("WOW_ENDPOINT_AUTH"))
	if publicEndpoint != "" {
		if _, err := url.ParseRequestURI(publicEndpoint); err != nil {
			return Config{}, fmt.Errorf("invalid WOW_PUBLIC_ENDPOINT")
		}
		if endpointKind == "" {
			endpointKind = "https"
		}
		if endpointAuth == "" {
			endpointAuth = "overlay"
		}
	} else if endpointKind != "" || endpointAuth != "" {
		return Config{}, fmt.Errorf("endpoint kind/auth require WOW_PUBLIC_ENDPOINT")
	}

	return Config{
		NodeID: nodeID,
		Locality: locality,
		GenerationStateFile: stateFile,
		ListenAddr: listenAddr,
		LeaseTTL: leaseTTL,
		PublicEndpoint: publicEndpoint,
		EndpointKind: endpointKind,
		EndpointAuth: endpointAuth,
	}, nil
}

func NewRuntime(config Config, sourceRevision, buildVersion string) (*Runtime, error) {
	return newRuntime(config, sourceRevision, buildVersion, time.Now)
}

func newRuntime(config Config, sourceRevision, buildVersion string, now func() time.Time) (*Runtime, error) {
	if now == nil {
		return nil, fmt.Errorf("clock is required")
	}
	if !sourceRevisionRE.MatchString(sourceRevision) {
		return nil, fmt.Errorf("source revision must be a 40-hex Git SHA")
	}
	if strings.TrimSpace(buildVersion) == "" {
		return nil, fmt.Errorf("build version is required")
	}
	capabilities, err := sortedUnique(config.Capabilities)
	if err != nil {
		return nil, fmt.Errorf("invalid capability set: %w", err)
	}
	config.Capabilities = capabilities

	generation, err := allocateGeneration(config.GenerationStateFile, config.NodeID)
	if err != nil {
		return nil, err
	}
	incarnationID, err := embodiment.NewIncarnationID()
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{
		config: config,
		generation: generation,
		incarnationID: incarnationID,
		sourceRevision: sourceRevision,
		buildVersion: buildVersion,
		now: now,
	}
	if _, err := runtime.Card(); err != nil {
		return nil, err
	}
	return runtime, nil
}

func (r *Runtime) Card() (embodiment.Card, error) {
	if r == nil {
		return embodiment.Card{}, fmt.Errorf("runtime is nil")
	}
	endpoints := []embodiment.Endpoint{}
	if r.config.PublicEndpoint != "" {
		endpoints = append(endpoints, embodiment.Endpoint{
			Kind: r.config.EndpointKind,
			Address: r.config.PublicEndpoint,
			Auth: r.config.EndpointAuth,
		})
	}
	card := embodiment.Card{
		Schema: embodiment.CardSchema,
		Protocol: embodiment.ProtocolVersion,
		NodeID: r.config.NodeID,
		Generation: r.generation,
		IncarnationID: r.incarnationID,
		Locality: r.config.Locality,
		Endpoints: endpoints,
		Capabilities: append([]string(nil), r.config.Capabilities...),
		LeaseExpiresAt: r.now().UTC().Add(r.config.LeaseTTL).Format(time.RFC3339Nano),
	}
	if err := card.Validate(); err != nil {
		return embodiment.Card{}, err
	}
	return card, nil
}

func (r *Runtime) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", r.handleHealth)
	mux.HandleFunc("/readyz", r.handleReady)
	mux.HandleFunc("/v1/card", r.handleCard)
	return mux
}

func requireGET(w http.ResponseWriter, req *http.Request) bool {
	if req.Method == http.MethodGet {
		return true
	}
	w.Header().Set("Allow", http.MethodGet)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	raw, err := protocol.CanonicalJSON(value)
	if err != nil {
		http.Error(w, "serialization failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(raw, '
'))
}

func (r *Runtime) handleHealth(w http.ResponseWriter, req *http.Request) {
	if !requireGET(w, req) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"node_id": r.config.NodeID,
		"generation": r.generation,
		"incarnation_id": r.incarnationID,
		"source_revision": r.sourceRevision,
		"build_version": r.buildVersion,
	})
}

func (r *Runtime) handleReady(w http.ResponseWriter, req *http.Request) {
	if !requireGET(w, req) {
		return
	}
	card, err := r.Card()
	if err != nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	digest, err := card.CanonicalDigest()
	if err != nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ready",
		"node_id": card.NodeID,
		"generation": card.Generation,
		"card_digest": digest,
	})
}

func (r *Runtime) handleCard(w http.ResponseWriter, req *http.Request) {
	if !requireGET(w, req) {
		return
	}
	card, err := r.Card()
	if err != nil {
		http.Error(w, "card unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, card)
}

func (r *Runtime) ListenAddr() string {
	return r.config.ListenAddr
}

func (r *Runtime) VersionDocument() map[string]any {
	return map[string]any{
		"product": "wow-sidecar",
		"source_revision": r.sourceRevision,
		"build_version": r.buildVersion,
		"go_runtime": "static",
	}
}

func DecodeGenerationState(raw []byte) (map[string]any, error) {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}
