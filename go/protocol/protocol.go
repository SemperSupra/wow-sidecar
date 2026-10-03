package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const (
	RequestSchema = "agent-dispatch.host-operator-request.v1"
	ReceiptSchema = "agent-dispatch.host-operator-receipt.v1"
	ProfileSchema = "wow-sidecar.operator-profile.v1"
	ClaimRefPrefix = "refs/heads/claims/"
)

var (
	requestIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,79}$`)
	shaRE       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	revisionRE  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type Authority struct {
	Record   string `json:"record"`
	Revision string `json:"revision"`
	State    string `json:"state"`
}

type Request struct {
	Schema           string         `json:"schema"`
	RequestID        string         `json:"request_id"`
	OperatorProfile  string         `json:"operator_profile"`
	OperatorRevision string         `json:"operator_revision"`
	Authority        Authority      `json:"authority"`
	Inputs           map[string]any `json:"inputs"`
	Constraints      map[string]any `json:"constraints"`
}

type AuthorityBinding struct {
	Record   string
	Revision string
	State    string
}

type Operator struct {
	Kind                string            `json:"kind"`
	Repository          string            `json:"repository"`
	Revision            string            `json:"revision"`
	OperatorPath        string            `json:"operator_path"`
	RequireMainRevision bool              `json:"require_main_revision"`
	Environment         map[string]string `json:"environment"`
}

type Profile struct {
	Schema    string    `json:"schema"`
	Profile   string    `json:"profile"`
	Authority Authority `json:"authority"`
	Operator  Operator  `json:"operator"`
}

type ReceiptArgs struct {
	RequestID         string         `json:"request_id"`
	RequestSHA        string         `json:"request_sha"`
	Result            string         `json:"result"`
	Status            string         `json:"status"`
	OperatorProfile   *string        `json:"operator_profile"`
	OperatorRevision  *string        `json:"operator_revision"`
	AuthorityRecord   *string        `json:"authority_record"`
	AuthorityRevision *string        `json:"authority_revision"`
	AuthorityState    *string        `json:"authority_state"`
	MutationState     *bool          `json:"mutation_state"`
	Details           map[string]any `json:"details"`
}

type Receipt struct {
	Schema                     string         `json:"schema"`
	RequestID                  string         `json:"request_id"`
	RequestCommitSHA           string         `json:"request_commit_sha"`
	ClaimRef                   string         `json:"claim_ref"`
	OperatorProfile            *string        `json:"operator_profile"`
	OperatorRevision           *string        `json:"operator_revision"`
	AuthorityRecord            *string        `json:"authority_record"`
	AuthorityRevision          *string        `json:"authority_revision"`
	AuthorityState             *string        `json:"authority_state"`
	Result                     string         `json:"result"`
	Status                     string         `json:"status"`
	WorkspaceMutationPerformed *bool          `json:"workspace_mutation_performed"`
	RetryAuthorized            bool           `json:"retry_authorized"`
	PromotionPerformed         bool           `json:"promotion_performed"`
	ReleasePerformed           bool           `json:"release_performed"`
	Details                    map[string]any `json:"details"`
}

type PreExecutionDecision struct {
	State               string  `json:"state"`
	Result              *string `json:"result,omitempty"`
	RetryAuthorized     bool    `json:"retry_authorized"`
	ExecuteAllowed      bool    `json:"execute_allowed"`
	ClaimRequired       bool    `json:"claim_required"`
	ClaimCommitSHA      string  `json:"claim_commit_sha,omitempty"`
	ReceiptCommitSHA    string  `json:"receipt_commit_sha,omitempty"`
	ClaimMatchesRequest *bool   `json:"claim_matches_request,omitempty"`
}

func exactObject(data []byte, expected ...string) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("object decode failed: %w", err)
	}
	if raw == nil {
		return nil, fmt.Errorf("value must be an object")
	}
	if len(raw) != len(expected) {
		return nil, fmt.Errorf("fields do not match schema")
	}
	for _, key := range expected {
		if _, ok := raw[key]; !ok {
			return nil, fmt.Errorf("fields do not match schema")
		}
	}
	return raw, nil
}

func normalizedNonEmpty(value, name string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be a normalized non-empty string", name)
	}
	return nil
}

func ParseRequest(data []byte) (Request, error) {
	raw, err := exactObject(data,
		"schema", "request_id", "operator_profile", "operator_revision",
		"authority", "inputs", "constraints",
	)
	if err != nil {
		return Request{}, fmt.Errorf("request.json %w", err)
	}
	if _, err := exactObject(raw["authority"], "record", "revision", "state"); err != nil {
		return Request{}, fmt.Errorf("authority fields do not match the v1 schema")
	}

	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return Request{}, fmt.Errorf("request.json decode failed: %w", err)
	}
	if req.Schema != RequestSchema {
		return Request{}, fmt.Errorf("unsupported host-operator request schema")
	}
	if !requestIDRE.MatchString(req.RequestID) {
		return Request{}, fmt.Errorf("invalid host-operator request_id")
	}
	if err := normalizedNonEmpty(req.OperatorProfile, "operator_profile"); err != nil {
		return Request{}, err
	}
	if !shaRE.MatchString(req.OperatorRevision) {
		return Request{}, fmt.Errorf("operator_revision must be a 40-hex Git SHA")
	}
	if strings.TrimSpace(req.Authority.Record) == "" {
		return Request{}, fmt.Errorf("authority.record is required")
	}
	if !revisionRE.MatchString(req.Authority.Revision) {
		return Request{}, fmt.Errorf("authority.revision is invalid")
	}
	if req.Authority.State != "open" {
		return Request{}, fmt.Errorf("authority.state must be open")
	}
	if req.Inputs == nil {
		return Request{}, fmt.Errorf("inputs must be an object")
	}
	if req.Constraints == nil {
		return Request{}, fmt.Errorf("constraints must be an object")
	}
	if v, ok := req.Constraints["no_retry"].(bool); !ok || !v {
		return Request{}, fmt.Errorf("constraint no_retry must be true")
	}
	if v, ok := req.Constraints["promotion_performed"].(bool); !ok || v {
		return Request{}, fmt.Errorf("constraint promotion_performed must be false")
	}
	if v, ok := req.Constraints["release_performed"].(bool); !ok || v {
		return Request{}, fmt.Errorf("constraint release_performed must be false")
	}
	return req, nil
}

func ValidateAuthorityBinding(req Request, binding AuthorityBinding) error {
	if strings.TrimSpace(binding.Record) == "" {
		return fmt.Errorf("authority binding record is required")
	}
	state := binding.State
	if state == "" {
		state = "open"
	}
	if state != "open" {
		return fmt.Errorf("authority binding state must be open")
	}
	if req.Authority.Record != binding.Record {
		return fmt.Errorf("request authority is outside the admitted host-control envelope")
	}
	if binding.Revision != "" {
		if !revisionRE.MatchString(binding.Revision) {
			return fmt.Errorf("authority binding revision is invalid")
		}
		if req.Authority.Revision != binding.Revision {
			return fmt.Errorf("request authority revision is outside the admitted host-control envelope")
		}
	}
	if req.Authority.State != state {
		return fmt.Errorf("request authority state is outside the admitted host-control envelope")
	}
	return nil
}

func ParseProfile(data []byte) (Profile, error) {
	raw, err := exactObject(data, "schema", "profile", "authority", "operator")
	if err != nil {
		return Profile{}, fmt.Errorf("operator profile document %w", err)
	}
	if _, err := exactObject(raw["authority"], "record", "revision", "state"); err != nil {
		return Profile{}, fmt.Errorf("authority fields do not match v1 schema")
	}
	if _, err := exactObject(raw["operator"],
		"kind", "repository", "revision", "operator_path",
		"require_main_revision", "environment",
	); err != nil {
		return Profile{}, fmt.Errorf("operator fields do not match pinned-repository v1 schema")
	}

	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return Profile{}, fmt.Errorf("operator profile decode failed: %w", err)
	}
	if p.Schema != ProfileSchema {
		return Profile{}, fmt.Errorf("unsupported operator profile schema")
	}
	if err := normalizedNonEmpty(p.Profile, "profile"); err != nil {
		return Profile{}, err
	}
	if err := normalizedNonEmpty(p.Authority.Record, "authority.record"); err != nil {
		return Profile{}, err
	}
	if !revisionRE.MatchString(p.Authority.Revision) {
		return Profile{}, fmt.Errorf("authority.revision must be sha256:<64-hex>")
	}
	if p.Authority.State != "open" {
		return Profile{}, fmt.Errorf("authority.state must be open")
	}
	if p.Operator.Kind != "pinned-repository" {
		return Profile{}, fmt.Errorf("unsupported operator kind")
	}
	if err := normalizedNonEmpty(p.Operator.Repository, "operator.repository"); err != nil {
		return Profile{}, err
	}
	if strings.Count(p.Operator.Repository, "/") != 1 {
		return Profile{}, fmt.Errorf("operator repository must be owner/name")
	}
	if !shaRE.MatchString(p.Operator.Revision) {
		return Profile{}, fmt.Errorf("operator.revision must be a 40-hex Git SHA")
	}
	if err := normalizedNonEmpty(p.Operator.OperatorPath, "operator.operator_path"); err != nil {
		return Profile{}, err
	}
	if strings.HasPrefix(p.Operator.OperatorPath, "/") ||
		strings.Contains("/"+p.Operator.OperatorPath+"/", "/../") ||
		strings.Contains("/"+p.Operator.OperatorPath+"/", "/./") {
		return Profile{}, fmt.Errorf("operator path must be a normalized relative path")
	}
	if p.Operator.Environment == nil {
		return Profile{}, fmt.Errorf("operator.environment must be an object")
	}
	for k := range p.Operator.Environment {
		if err := normalizedNonEmpty(k, "operator.environment key"); err != nil {
			return Profile{}, err
		}
	}
	return p, nil
}

func NewReceipt(args ReceiptArgs) (Receipt, error) {
	if !requestIDRE.MatchString(args.RequestID) {
		return Receipt{}, fmt.Errorf("invalid host-operator request_id")
	}
	if !shaRE.MatchString(args.RequestSHA) {
		return Receipt{}, fmt.Errorf("request_sha must be a 40-hex Git SHA")
	}
	if args.Result != "ELIGIBLE" && args.Result != "REJECTED" && args.Result != "UNKNOWN" {
		return Receipt{}, fmt.Errorf("invalid receipt result")
	}
	if strings.TrimSpace(args.Status) == "" || strings.TrimSpace(args.Status) != args.Status {
		return Receipt{}, fmt.Errorf("invalid receipt status")
	}
	details := args.Details
	if details == nil {
		details = map[string]any{}
	}
	return Receipt{
		Schema:                     ReceiptSchema,
		RequestID:                  args.RequestID,
		RequestCommitSHA:           args.RequestSHA,
		ClaimRef:                   ClaimRefPrefix + args.RequestID,
		OperatorProfile:            args.OperatorProfile,
		OperatorRevision:           args.OperatorRevision,
		AuthorityRecord:            args.AuthorityRecord,
		AuthorityRevision:          args.AuthorityRevision,
		AuthorityState:             args.AuthorityState,
		Result:                     args.Result,
		Status:                     args.Status,
		WorkspaceMutationPerformed: args.MutationState,
		RetryAuthorized:            false,
		PromotionPerformed:         false,
		ReleasePerformed:           false,
		Details:                    details,
	}, nil
}

func CanonicalJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func DecidePreExecution(requestID, requestSHA string, claims, receipts map[string]string) (PreExecutionDecision, error) {
	if !requestIDRE.MatchString(requestID) {
		return PreExecutionDecision{}, fmt.Errorf("invalid host-operator request_id")
	}
	if !shaRE.MatchString(requestSHA) {
		return PreExecutionDecision{}, fmt.Errorf("request ref target must be a 40-hex Git SHA")
	}
	if receiptSHA, ok := receipts[requestID]; ok {
		if !shaRE.MatchString(receiptSHA) {
			return PreExecutionDecision{}, fmt.Errorf("receipt ref target must be a 40-hex Git SHA")
		}
		return PreExecutionDecision{
			State:            "already-receipted",
			RetryAuthorized:  false,
			ExecuteAllowed:   false,
			ClaimRequired:    false,
			ReceiptCommitSHA: receiptSHA,
		}, nil
	}
	if claimSHA, ok := claims[requestID]; ok {
		if !shaRE.MatchString(claimSHA) {
			return PreExecutionDecision{}, fmt.Errorf("claim ref target must be a 40-hex Git SHA")
		}
		result := "UNKNOWN"
		matches := claimSHA == requestSHA
		return PreExecutionDecision{
			State:               "claimed-without-receipt",
			Result:              &result,
			RetryAuthorized:     false,
			ExecuteAllowed:      false,
			ClaimRequired:       false,
			ClaimCommitSHA:      claimSHA,
			ClaimMatchesRequest: &matches,
		}, nil
	}
	return PreExecutionDecision{
		State:           "pending-unclaimed",
		RetryAuthorized: false,
		ExecuteAllowed:  false,
		ClaimRequired:   true,
	}, nil
}
