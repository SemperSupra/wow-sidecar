package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SemperSupra/wow-sidecar/go/federation"
)

const (
	defaultPeerMaxSkew   = 2 * time.Minute
	defaultPeerReplayTTL = 5 * time.Minute
)

type PeerRendezvousRuntimeConfig struct {
	CredentialsFile string
	MaxSkew         time.Duration
	ReplayTTL       time.Duration
}

func peerRendezvousRuntimeConfigFromEnv() (*PeerRendezvousRuntimeConfig, error) {
	enabledRaw := os.Getenv("WOW_PEER_RENDEZVOUS_ENABLED")
	credentialsFile := os.Getenv("WOW_PEER_CREDENTIALS_FILE")
	maxSkewRaw := os.Getenv("WOW_PEER_MAX_SKEW_SECONDS")
	replayTTLRaw := os.Getenv("WOW_PEER_REPLAY_TTL_SECONDS")

	secondaryConfigured := credentialsFile != "" || maxSkewRaw != "" || replayTTLRaw != ""
	switch enabledRaw {
	case "":
		if secondaryConfigured {
			return nil, fmt.Errorf("WOW_PEER_RENDEZVOUS_ENABLED=true is required when peer rendezvous settings are present")
		}
		return nil, nil
	case "false":
		if secondaryConfigured {
			return nil, fmt.Errorf("peer rendezvous settings must be absent when WOW_PEER_RENDEZVOUS_ENABLED=false")
		}
		return nil, nil
	case "true":
	default:
		return nil, fmt.Errorf("WOW_PEER_RENDEZVOUS_ENABLED must be true or false")
	}

	if credentialsFile == "" || strings.TrimSpace(credentialsFile) != credentialsFile {
		return nil, fmt.Errorf("WOW_PEER_CREDENTIALS_FILE must be normalized and non-empty")
	}
	if !filepath.IsAbs(credentialsFile) || filepath.Clean(credentialsFile) != credentialsFile {
		return nil, fmt.Errorf("WOW_PEER_CREDENTIALS_FILE must be an absolute normalized path")
	}

	maxSkew := defaultPeerMaxSkew
	if maxSkewRaw != "" {
		if strings.TrimSpace(maxSkewRaw) != maxSkewRaw {
			return nil, fmt.Errorf("WOW_PEER_MAX_SKEW_SECONDS must be normalized")
		}
		seconds, err := strconv.Atoi(maxSkewRaw)
		if err != nil || seconds < 1 || seconds > 600 {
			return nil, fmt.Errorf("WOW_PEER_MAX_SKEW_SECONDS must be between 1 and 600")
		}
		maxSkew = time.Duration(seconds) * time.Second
	}

	replayTTL := defaultPeerReplayTTL
	if replayTTLRaw != "" {
		if strings.TrimSpace(replayTTLRaw) != replayTTLRaw {
			return nil, fmt.Errorf("WOW_PEER_REPLAY_TTL_SECONDS must be normalized")
		}
		seconds, err := strconv.Atoi(replayTTLRaw)
		if err != nil || seconds < 1 || seconds > 1800 {
			return nil, fmt.Errorf("WOW_PEER_REPLAY_TTL_SECONDS must be between 1 and 1800")
		}
		replayTTL = time.Duration(seconds) * time.Second
	}

	return &PeerRendezvousRuntimeConfig{
		CredentialsFile: credentialsFile,
		MaxSkew:         maxSkew,
		ReplayTTL:       replayTTL,
	}, nil
}

func (r *Runtime) configurePeerRendezvous(
	config *PeerRendezvousRuntimeConfig,
	credentials *federation.PeerCredentials,
	verifyAuthority PeerAuthorityVerifier,
) error {
	if r == nil {
		return fmt.Errorf("runtime is required")
	}
	if config == nil {
		return nil
	}
	if r.rendezvous == nil {
		return fmt.Errorf("peer rendezvous requires local rendezvous configuration")
	}
	if r.peers == nil || r.peers.Configured() == 0 {
		return fmt.Errorf("peer rendezvous requires at least one configured peer")
	}
	if credentials == nil || credentials.Count() == 0 {
		return fmt.Errorf("peer rendezvous credentials are required")
	}
	if verifyAuthority == nil {
		return fmt.Errorf("peer rendezvous requires canonical authority verification")
	}
	destinationCard, err := r.Card()
	if err != nil {
		return fmt.Errorf("build destination embodiment card: %w", err)
	}
	handler, err := NewPeerRendezvousHandler(PeerRendezvousHandlerConfig{
		DestinationCard: destinationCard,
		CurrentPeer:      r.peers.CurrentCard,
		GuardCurrentPeer: r.peers.WithCurrentCard,
		Credentials:      credentials,
		VerifyAuthority: verifyAuthority,
		Rendezvous:      r.rendezvous,
		Replay:          federation.NewReplayCache(),
		Now:             r.now,
		MaxSkew:         config.MaxSkew,
		ReplayTTL:       config.ReplayTTL,
	})
	if err != nil {
		return fmt.Errorf("configure peer rendezvous handler: %w", err)
	}
	r.peerRendezvous = handler
	return nil
}

func (r *Runtime) ConfigurePeerRendezvousFromEnv() error {
	config, err := peerRendezvousRuntimeConfigFromEnv()
	if err != nil {
		return err
	}
	if config == nil {
		return nil
	}
	if r == nil || r.control == nil {
		return fmt.Errorf("peer rendezvous requires durable control")
	}
	credentials, err := federation.LoadPeerCredentials(config.CredentialsFile)
	if err != nil {
		return fmt.Errorf("load peer rendezvous credentials: %w", err)
	}
	verify := func(ctx context.Context, record, revision, state string) error {
		return r.control.verifyPeerAuthority(ctx, record, revision, state)
	}
	return r.configurePeerRendezvous(config, credentials, verify)
}

func (r *Runtime) HasPeerRendezvous() bool {
	return r != nil && r.peerRendezvous != nil
}
