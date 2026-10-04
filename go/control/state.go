package control

import (
	"errors"
	"fmt"
	"sort"

	"github.com/SemperSupra/wow-sidecar/go/protocol"
)

const (
	ResultEligible = "ELIGIBLE"
	ResultRejected = "REJECTED"
	ResultUnknown  = "UNKNOWN"
)

type Store interface {
	RefMap(kind string) (map[string]string, error)
	ReadRequest(commitSHA string) ([]byte, error)
	CreateClaim(requestID, requestSHA string) error
	CreateReceipt(requestID, requestSHA string, receipt map[string]any) (commitSHA, digest string, err error)
}

type Handler func(protocol.Request) (map[string]any, error)
type AuthorityVerifier func(protocol.Request) (map[string]any, error)

type Outcome struct {
	RequestID           string
	State               string
	Result              string
	RequestCommitSHA    string
	ClaimCommitSHA      string
	ClaimMatchesRequest *bool
	ReceiptCommitSHA    string
	ReceiptSHA256       string
	Executed            bool
	RetryAuthorized     bool
}

func ProcessPendingOnce(store Store, registry map[string]Handler, localRevision string, verify AuthorityVerifier) ([]Outcome, error) {
	if store == nil || verify == nil {
		return nil, errors.New("store and authority verifier are required")
	}
	if !lowerHex(localRevision, 40) {
		return nil, errors.New("local operator revision must be a 40-hex Git SHA")
	}
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

	pending := make([]string, 0)
	for requestID := range requests {
		if _, claimed := claims[requestID]; claimed {
			continue
		}
		if _, receipted := receipts[requestID]; receipted {
			continue
		}
		pending = append(pending, requestID)
	}
	sort.Strings(pending)
	if len(pending) == 0 {
		return nil, nil
	}

	outcome, err := ProcessRequest(store, pending[0], requests[pending[0]], registry, localRevision, verify)
	if err != nil {
		return nil, err
	}
	return []Outcome{outcome}, nil
}

func ProcessRequest(store Store, requestID, requestSHA string, registry map[string]Handler, localRevision string, verify AuthorityVerifier) (Outcome, error) {
	if store == nil || verify == nil {
		return Outcome{}, errors.New("store and authority verifier are required")
	}
	if !validRequestID(requestID) {
		return Outcome{}, errors.New("invalid request id")
	}
	if !lowerHex(requestSHA, 40) || !lowerHex(localRevision, 40) {
		return Outcome{}, errors.New("request/local revision must be 40-hex Git SHA")
	}

	receipts, err := store.RefMap("receipts")
	if err != nil {
		return Outcome{}, err
	}
	if receiptSHA, ok := receipts[requestID]; ok {
		return Outcome{
			RequestID:        requestID,
			State:            "already-receipted",
			ReceiptCommitSHA: receiptSHA,
			Executed:         false,
			RetryAuthorized:  false,
		}, nil
	}

	claims, err := store.RefMap("claims")
	if err != nil {
		return Outcome{}, err
	}
	if claimSHA, ok := claims[requestID]; ok {
		return claimedUnknown(requestID, requestSHA, claimSHA), nil
	}

	if err := store.CreateClaim(requestID, requestSHA); err != nil {
		claims, readErr := store.RefMap("claims")
		if readErr != nil {
			return Outcome{}, fmt.Errorf("claim failed and reconciliation failed: %w", readErr)
		}
		if claimSHA, ok := claims[requestID]; ok {
			return claimedUnknown(requestID, requestSHA, claimSHA), nil
		}
		return Outcome{}, err
	}

	var request protocol.Request
	var handlerInvoked bool
	var receipt map[string]any
	raw, executionErr := store.ReadRequest(requestSHA)
	if executionErr == nil {
		request, executionErr = protocol.ParseRequest(raw)
	}
	if executionErr == nil && request.RequestID != requestID {
		executionErr = errors.New("request ref id does not match request.json")
	}
	if executionErr == nil && request.OperatorRevision != localRevision {
		executionErr = errors.New("request operator revision does not match local revision")
	}

	var handler Handler
	if executionErr == nil {
		var ok bool
		handler, ok = registry[request.OperatorProfile]
		if !ok {
			executionErr = errors.New("operator profile is not registered")
		}
	}

	if executionErr == nil {
		authorityEvidence, err := verify(request)
		if err != nil {
			executionErr = err
		} else if authorityEvidence == nil {
			executionErr = errors.New("authority verifier must return an object")
		}
	}

	if executionErr == nil {
		handlerInvoked = true
		details, err := handler(request)
		if err != nil {
			executionErr = err
		} else if details == nil {
			executionErr = errors.New("operator handler must return an object")
		} else {
			result, _ := details["result"].(string)
			if result != ResultEligible && result != ResultRejected && result != ResultUnknown {
				executionErr = errors.New("operator handler returned invalid result")
			} else {
				detailsCopy := clone(details)
				detailsCopy["authority_verified"] = true
				receipt = makeReceipt(
					requestID,
					requestSHA,
					result,
					stringValue(details["status"], "host-operator-complete"),
					&request,
					details["workspace_mutation_performed"],
					detailsCopy,
				)
			}
		}
	}

	if executionErr != nil {
		var requestPtr *protocol.Request
		if request.RequestID != "" {
			copy := request
			requestPtr = &copy
		}
		receipt = makeReceipt(
			requestID,
			requestSHA,
			ResultUnknown,
			"host-operator-failed-closed",
			requestPtr,
			nil,
			map[string]any{
				"phase":             "request-authority-validation-or-execution",
				"reason":            fmt.Sprintf("%T: raw error omitted", executionErr),
				"preserve_evidence": true,
			},
		)
	}

	receiptCommitSHA, receiptDigest, err := store.CreateReceipt(requestID, requestSHA, receipt)
	if err != nil {
		return Outcome{}, err
	}
	result, _ := receipt["result"].(string)
	return Outcome{
		RequestID:        requestID,
		State:            "receipted",
		Result:           result,
		RequestCommitSHA: requestSHA,
		ReceiptCommitSHA: receiptCommitSHA,
		ReceiptSHA256:    receiptDigest,
		Executed:         handlerInvoked,
		RetryAuthorized:  false,
	}, nil
}

func makeReceipt(requestID, requestSHA, result, status string, request *protocol.Request, mutation any, details map[string]any) map[string]any {
	var profile, revision, authorityRecord, authorityRevision, authorityState any
	if request != nil {
		profile = request.OperatorProfile
		revision = request.OperatorRevision
		authorityRecord = request.Authority.Record
		authorityRevision = request.Authority.Revision
		authorityState = request.Authority.State
	}
	return map[string]any{
		"schema":                       protocol.ReceiptSchema,
		"request_id":                   requestID,
		"request_commit_sha":           requestSHA,
		"claim_ref":                    protocol.ClaimRefPrefix + requestID,
		"operator_profile":             profile,
		"operator_revision":            revision,
		"authority_record":             authorityRecord,
		"authority_revision":           authorityRevision,
		"authority_state":              authorityState,
		"result":                       result,
		"status":                       status,
		"workspace_mutation_performed": mutation,
		"retry_authorized":             false,
		"promotion_performed":          false,
		"release_performed":            false,
		"details":                      details,
	}
}

func claimedUnknown(requestID, requestSHA, claimSHA string) Outcome {
	matches := claimSHA == requestSHA
	return Outcome{
		RequestID:           requestID,
		State:               "claimed-without-receipt",
		Result:              ResultUnknown,
		RequestCommitSHA:    requestSHA,
		ClaimCommitSHA:      claimSHA,
		ClaimMatchesRequest: &matches,
		Executed:            false,
		RetryAuthorized:     false,
	}
}

func clone(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func stringValue(value any, fallback string) string {
	if text, ok := value.(string); ok && text != "" {
		return text
	}
	return fallback
}

func validRequestID(value string) bool {
	if len(value) < 3 || len(value) > 80 {
		return false
	}
	for i, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			continue
		}
		if r == '-' && i > 0 {
			continue
		}
		return false
	}
	return true
}

func lowerHex(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, r := range value {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'f' {
			continue
		}
		return false
	}
	return true
}
