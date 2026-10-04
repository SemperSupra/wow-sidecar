package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/capability"
	"github.com/SemperSupra/wow-sidecar/go/control"
	"github.com/SemperSupra/wow-sidecar/go/controlcap"
	"github.com/SemperSupra/wow-sidecar/go/githubapp"
	"github.com/SemperSupra/wow-sidecar/go/protocol"
)

type ControlConfig struct {
	Repository        string
	Profile           string
	CapabilityID      string
	AuthorityRecord   string
	AuthorityRevision string
	AuthorityState    string
	PollInterval      time.Duration
	GitHubApp         githubapp.Config
}

type controlWorker struct {
	store         control.Store
	handlers      map[string]control.Handler
	localRevision string
	verify        control.AuthorityVerifier
	pollInterval  time.Duration
}

func controlConfigFromEnv() (*ControlConfig, error) {
	raw := map[string]string{
		"repository":         strings.TrimSpace(os.Getenv("WOW_CONTROL_REPOSITORY")),
		"profile":            strings.TrimSpace(os.Getenv("WOW_CONTROL_PROFILE")),
		"capability_id":      strings.TrimSpace(os.Getenv("WOW_CONTROL_CAPABILITY_ID")),
		"authority_record":   strings.TrimSpace(os.Getenv("WOW_CONTROL_AUTHORITY_RECORD")),
		"authority_revision": strings.TrimSpace(os.Getenv("WOW_CONTROL_AUTHORITY_REVISION")),
		"authority_state":    strings.TrimSpace(os.Getenv("WOW_CONTROL_AUTHORITY_STATE")),
		"poll_seconds":       strings.TrimSpace(os.Getenv("WOW_CONTROL_POLL_SECONDS")),
	}
	enabled := false
	for _, value := range raw {
		if value != "" {
			enabled = true
			break
		}
	}
	if !enabled {
		return nil, nil
	}
	for key, value := range raw {
		if value == "" {
			return nil, fmt.Errorf("incomplete WOW control configuration: %s is required", key)
		}
	}
	if strings.Count(raw["repository"], "/") != 1 {
		return nil, fmt.Errorf("WOW_CONTROL_REPOSITORY must be owner/name")
	}
	owner, repo, _ := strings.Cut(raw["repository"], "/")
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("WOW_CONTROL_REPOSITORY must be owner/name")
	}
	if err := githubapp.ValidateIssueCommentAuthorityExpectation(
		raw["authority_record"],
		raw["authority_revision"],
		raw["authority_state"],
	); err != nil {
		return nil, fmt.Errorf("invalid WOW control authority: %w", err)
	}
	seconds, err := strconv.Atoi(raw["poll_seconds"])
	if err != nil || seconds < 5 || seconds > 3600 {
		return nil, fmt.Errorf("WOW_CONTROL_POLL_SECONDS must be between 5 and 3600")
	}
	keyFile := strings.TrimSpace(os.Getenv("GITHUB_APP_PRIVATE_KEY_FILE"))
	if keyFile == "" {
		return nil, fmt.Errorf("WOW control requires GITHUB_APP_PRIVATE_KEY_FILE")
	}
	if !filepath.IsAbs(keyFile) {
		return nil, fmt.Errorf("GITHUB_APP_PRIVATE_KEY_FILE must be absolute for WOW control")
	}
	appConfig, err := githubapp.ConfigFromEnv()
	if err != nil {
		return nil, fmt.Errorf("invalid WOW control GitHub App configuration: %w", err)
	}
	return &ControlConfig{
		Repository:        raw["repository"],
		Profile:           raw["profile"],
		CapabilityID:      raw["capability_id"],
		AuthorityRecord:   raw["authority_record"],
		AuthorityRevision: raw["authority_revision"],
		AuthorityState:    raw["authority_state"],
		PollInterval:      time.Duration(seconds) * time.Second,
		GitHubApp:         appConfig,
	}, nil
}

func newControlWorker(
	config *ControlConfig,
	registry *capability.Registry,
	localRevision string,
) (*controlWorker, error) {
	if config == nil {
		return nil, fmt.Errorf("control config is required")
	}
	client, err := githubapp.New(config.GitHubApp)
	if err != nil {
		return nil, err
	}
	store, err := githubapp.NewControlStore(client, config.Repository)
	if err != nil {
		return nil, err
	}

	controlVerify := func(req protocol.Request) (map[string]any, error) {
		if req.Authority.Record != config.AuthorityRecord ||
			req.Authority.Revision != config.AuthorityRevision ||
			req.Authority.State != config.AuthorityState {
			return nil, fmt.Errorf("request authority does not match local control binding")
		}
		return client.VerifyIssueCommentAuthority(
			context.Background(),
			config.AuthorityRecord,
			config.AuthorityRevision,
			config.AuthorityState,
		)
	}
	capabilityVerify := func(_ context.Context, req capability.Request) error {
		if req.AuthorityRef != config.AuthorityRecord ||
			req.AuthorityRevision != config.AuthorityRevision ||
			req.AuthorityState != config.AuthorityState {
			return fmt.Errorf("capability authority does not match local control binding")
		}
		return nil
	}
	return newControlWorkerWithDependencies(
		config,
		store,
		registry,
		localRevision,
		controlVerify,
		capabilityVerify,
	)
}

func newControlWorkerWithDependencies(
	config *ControlConfig,
	store control.Store,
	registry *capability.Registry,
	localRevision string,
	controlVerify control.AuthorityVerifier,
	capabilityVerify capability.AuthorityVerifier,
) (*controlWorker, error) {
	if config == nil || store == nil || registry == nil || controlVerify == nil {
		return nil, fmt.Errorf("control worker dependencies are required")
	}
	if config.PollInterval < 5*time.Second || config.PollInterval > time.Hour {
		return nil, fmt.Errorf("control poll interval must be between 5 seconds and 1 hour")
	}
	handlers, err := controlcap.BuildHandlers(
		registry,
		[]controlcap.Binding{{Profile: config.Profile, CapabilityID: config.CapabilityID}},
		capabilityVerify,
	)
	if err != nil {
		return nil, err
	}
	return &controlWorker{
		store:         store,
		handlers:      handlers,
		localRevision: localRevision,
		verify:        controlVerify,
		pollInterval:  config.PollInterval,
	}, nil
}

func (w *controlWorker) RunOnce(ctx context.Context) ([]control.Outcome, error) {
	if w == nil {
		return nil, fmt.Errorf("control worker is unavailable")
	}
	if contextual, ok := w.store.(interface{ SetContext(context.Context) }); ok {
		contextual.SetContext(ctx)
	}
	outcomes, err := control.ProcessPendingOnce(
		w.store,
		w.handlers,
		w.localRevision,
		w.verify,
	)
	if err != nil || len(outcomes) > 0 {
		return outcomes, err
	}
	outcome, err := observeClaimedWithoutReceipt(w.store)
	if err != nil || outcome == nil {
		return nil, err
	}
	return []control.Outcome{*outcome}, nil
}

func observeClaimedWithoutReceipt(store control.Store) (*control.Outcome, error) {
	requests, err := store.RefMap("requests")
	if err != nil {
		return nil, err
	}
	claims, err := store.RefMap("claims")
	if err != nil {
		return nil, err
	}
	receipts, err := store.RefMap("receipts")
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(requests))
	for requestID := range requests {
		ids = append(ids, requestID)
	}
	sort.Strings(ids)
	for _, requestID := range ids {
		if _, receipted := receipts[requestID]; receipted {
			continue
		}
		claimSHA, claimed := claims[requestID]
		if !claimed {
			continue
		}
		requestSHA := requests[requestID]
		matches := claimSHA == requestSHA
		return &control.Outcome{
			RequestID:           requestID,
			State:               "claimed-without-receipt",
			Result:              control.ResultUnknown,
			RequestCommitSHA:    requestSHA,
			ClaimCommitSHA:      claimSHA,
			ClaimMatchesRequest: &matches,
			Executed:            false,
			RetryAuthorized:     false,
		}, nil
	}
	return nil, nil
}

func (w *controlWorker) Run(ctx context.Context) error {
	if w == nil {
		return fmt.Errorf("control worker is unavailable")
	}
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for {
		_, _ = w.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (r *Runtime) HasControl() bool {
	return r != nil && r.control != nil
}

func (r *Runtime) RunControl(ctx context.Context) error {
	if r == nil || r.control == nil {
		return fmt.Errorf("control worker is unavailable")
	}
	return r.control.Run(ctx)
}
