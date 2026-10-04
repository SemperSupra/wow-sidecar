package node

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/capability"
	"github.com/SemperSupra/wow-sidecar/go/control"
	"github.com/SemperSupra/wow-sidecar/go/controlcap"
	"github.com/SemperSupra/wow-sidecar/go/githubapp"
	"github.com/SemperSupra/wow-sidecar/go/protocol"
)

const defaultControlPollInterval = 15 * time.Second

var controlAuthorityRevisionRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type ControlConfig struct {
	Repository        string
	Profile           string
	CapabilityID      string
	AuthorityRecord   string
	AuthorityRevision string
	PollInterval      time.Duration
}

type ControlStatus struct {
	Enabled          bool   `json:"enabled"`
	State            string `json:"state"`
	LastAttemptAt    string `json:"last_attempt_at,omitempty"`
	LastSuccessAt    string `json:"last_success_at,omitempty"`
	LastOutcomeState string `json:"last_outcome_state,omitempty"`
	LastResult       string `json:"last_result,omitempty"`
	Reason           string `json:"reason,omitempty"`
}

type ControlWorker struct {
	runtime        *Runtime
	config         ControlConfig
	client         *githubapp.Client
	store          *githubapp.ControlStore
	handlers       map[string]control.Handler
	sourceRevision string

	mu     sync.RWMutex
	status ControlStatus
}

func controlConfigFromEnv() (*ControlConfig, error) {
	values := map[string]string{
		"WOW_CONTROL_REPOSITORY":         os.Getenv("WOW_CONTROL_REPOSITORY"),
		"WOW_CONTROL_PROFILE":            os.Getenv("WOW_CONTROL_PROFILE"),
		"WOW_CONTROL_CAPABILITY":         os.Getenv("WOW_CONTROL_CAPABILITY"),
		"WOW_CONTROL_AUTHORITY_RECORD":   os.Getenv("WOW_CONTROL_AUTHORITY_RECORD"),
		"WOW_CONTROL_AUTHORITY_REVISION": os.Getenv("WOW_CONTROL_AUTHORITY_REVISION"),
	}
	pollRaw := os.Getenv("WOW_CONTROL_POLL_SECONDS")

	enabled := pollRaw != ""
	for _, value := range values {
		if value != "" {
			enabled = true
			break
		}
	}
	if !enabled {
		return nil, nil
	}

	for name, value := range values {
		if value == "" {
			return nil, fmt.Errorf("%s is required when durable control is enabled", name)
		}
		if strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("%s must be normalized", name)
		}
	}
	if strings.Count(values["WOW_CONTROL_REPOSITORY"], "/") != 1 {
		return nil, fmt.Errorf("WOW_CONTROL_REPOSITORY must be owner/name")
	}
	if !controlAuthorityRevisionRE.MatchString(values["WOW_CONTROL_AUTHORITY_REVISION"]) {
		return nil, fmt.Errorf("WOW_CONTROL_AUTHORITY_REVISION must be sha256:<64-hex>")
	}

	poll := defaultControlPollInterval
	if pollRaw != "" {
		if strings.TrimSpace(pollRaw) != pollRaw {
			return nil, fmt.Errorf("WOW_CONTROL_POLL_SECONDS must be normalized")
		}
		seconds, err := strconv.Atoi(pollRaw)
		if err != nil || seconds < 5 || seconds > 3600 {
			return nil, fmt.Errorf("WOW_CONTROL_POLL_SECONDS must be between 5 and 3600")
		}
		poll = time.Duration(seconds) * time.Second
	}

	return &ControlConfig{
		Repository:        values["WOW_CONTROL_REPOSITORY"],
		Profile:           values["WOW_CONTROL_PROFILE"],
		CapabilityID:      values["WOW_CONTROL_CAPABILITY"],
		AuthorityRecord:   values["WOW_CONTROL_AUTHORITY_RECORD"],
		AuthorityRevision: values["WOW_CONTROL_AUTHORITY_REVISION"],
		PollInterval:      poll,
	}, nil
}

func newControlWorker(runtime *Runtime, client *githubapp.Client) (*ControlWorker, error) {
	if runtime == nil || runtime.config.Control == nil {
		return nil, fmt.Errorf("durable control is not configured")
	}
	if client == nil {
		return nil, fmt.Errorf("GitHub App client is required")
	}
	config := *runtime.config.Control
	store, err := githubapp.NewControlStore(client, config.Repository)
	if err != nil {
		return nil, fmt.Errorf("configure control store: %w", err)
	}

	capabilityVerify := func(ctx context.Context, req capability.Request) error {
		if req.AuthorityRef != config.AuthorityRecord ||
			req.AuthorityRevision != config.AuthorityRevision ||
			req.AuthorityState != "open" {
			return fmt.Errorf("capability authority is outside the admitted control binding")
		}
		_, err := client.VerifyIssueCommentAuthority(
			ctx,
			req.AuthorityRef,
			req.AuthorityRevision,
			req.AuthorityState,
		)
		return err
	}
	handlers, err := controlcap.BuildHandlers(
		runtime.capabilities,
		[]controlcap.Binding{{Profile: config.Profile, CapabilityID: config.CapabilityID}},
		capabilityVerify,
	)
	if err != nil {
		return nil, fmt.Errorf("configure typed control binding: %w", err)
	}

	return &ControlWorker{
		runtime:        runtime,
		config:         config,
		client:         client,
		store:          store,
		handlers:       handlers,
		sourceRevision: runtime.sourceRevision,
		status: ControlStatus{
			Enabled: true,
			State:   "STARTING",
		},
	}, nil
}

func NewControlWorkerFromEnv(runtime *Runtime) (*ControlWorker, error) {
	if runtime == nil || runtime.config.Control == nil {
		return nil, nil
	}
	appConfig, err := githubapp.ConfigFromEnv()
	if err != nil {
		return nil, fmt.Errorf("configure durable-control GitHub App: %w", err)
	}
	client, err := githubapp.New(appConfig)
	if err != nil {
		return nil, fmt.Errorf("create durable-control GitHub App client: %w", err)
	}
	return newControlWorker(runtime, client)
}

func (w *ControlWorker) verifyAuthority(ctx context.Context, req protocol.Request) (map[string]any, error) {
	if req.OperatorProfile != w.config.Profile {
		return nil, fmt.Errorf("request operator profile is outside the admitted control binding")
	}
	if err := protocol.ValidateAuthorityBinding(
		req,
		protocol.AuthorityBinding{
			Record:   w.config.AuthorityRecord,
			Revision: w.config.AuthorityRevision,
			State:    "open",
		},
	); err != nil {
		return nil, err
	}
	return w.client.VerifyIssueCommentAuthority(
		ctx,
		req.Authority.Record,
		req.Authority.Revision,
		req.Authority.State,
	)
}

func (w *ControlWorker) RunOnce(ctx context.Context) ([]control.Outcome, error) {
	if w == nil {
		return nil, fmt.Errorf("control worker is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	attemptedAt := time.Now().UTC()
	store := *w.store
	store.Context = ctx
	outcomes, err := control.ProcessPendingOnce(
		&store,
		w.handlers,
		w.sourceRevision,
		func(req protocol.Request) (map[string]any, error) {
			return w.verifyAuthority(ctx, req)
		},
	)
	if err != nil {
		w.recordFailure(attemptedAt)
		return nil, err
	}
	w.recordSuccess(attemptedAt, outcomes)
	return outcomes, nil
}

func (w *ControlWorker) Run(ctx context.Context) {
	if w == nil {
		return
	}
	for {
		_, _ = w.RunOnce(ctx)
		timer := time.NewTimer(w.config.PollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func (w *ControlWorker) recordFailure(at time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status.Enabled = true
	w.status.State = "DEGRADED"
	w.status.LastAttemptAt = at.Format(time.RFC3339Nano)
	w.status.Reason = "control-backend-unavailable"
}

func (w *ControlWorker) recordSuccess(at time.Time, outcomes []control.Outcome) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status.Enabled = true
	w.status.State = "READY"
	w.status.LastAttemptAt = at.Format(time.RFC3339Nano)
	w.status.LastSuccessAt = at.Format(time.RFC3339Nano)
	w.status.Reason = ""
	w.status.LastOutcomeState = "idle"
	w.status.LastResult = ""
	if len(outcomes) == 1 {
		w.status.LastOutcomeState = outcomes[0].State
		w.status.LastResult = outcomes[0].Result
	}
}

func (w *ControlWorker) Status() ControlStatus {
	if w == nil {
		return ControlStatus{Enabled: false, State: "DISABLED"}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.status
}

func (r *Runtime) ConfigureControlFromEnv() error {
	if r == nil || r.config.Control == nil {
		return nil
	}
	worker, err := NewControlWorkerFromEnv(r)
	if err != nil {
		return err
	}
	r.control = worker
	return nil
}

func (r *Runtime) HasControl() bool {
	return r != nil && r.control != nil
}

func (r *Runtime) RunControl(ctx context.Context) {
	if r != nil && r.control != nil {
		r.control.Run(ctx)
	}
}

func (r *Runtime) ControlStatus() ControlStatus {
	if r == nil || r.control == nil {
		return ControlStatus{Enabled: false, State: "DISABLED"}
	}
	return r.control.Status()
}

func (r *Runtime) handleControl(w http.ResponseWriter, req *http.Request) {
	if !requireGET(w, req) {
		return
	}
	writeJSON(w, http.StatusOK, r.ControlStatus())
}
