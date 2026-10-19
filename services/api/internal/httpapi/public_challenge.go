package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/benefit"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/config"
)

var (
	ErrChallengeRejected    = errors.New("challenge rejected")
	ErrChallengeUnavailable = errors.New("challenge unavailable")
)

// PublicChallengeVerifier is independent of benefit campaigns and Opus8.
// Actions are selected by handlers, never accepted from the caller.
type PublicChallengeVerifier interface {
	Verify(ctx context.Context, token, remoteIP, action string) error
}

type publicChallenges map[string]*benefit.TurnstileVerifier

func NewPublicChallenges(cfg *config.Config) (PublicChallengeVerifier, error) {
	if cfg.TurnstileSiteKey == "" {
		return nil, ErrChallengeUnavailable
	}
	verifiers := publicChallenges{}
	for _, action := range []string{"order_create", "affiliate_login", "affiliate_register", "comment_upload"} {
		verifier, err := benefit.NewTurnstileVerifier(cfg.TurnstileSecretKey, cfg.TurnstileExpectedHostname, action, cfg.TurnstileTimeoutMS)
		if err != nil {
			return nil, err
		}
		verifiers[action] = verifier
	}
	return verifiers, nil
}

func (v publicChallenges) Verify(ctx context.Context, token, remoteIP, action string) error {
	verifier := v[action]
	if verifier == nil {
		return ErrChallengeUnavailable
	}
	result := verifier.Verify(ctx, token, remoteIP)
	if result.Valid {
		return nil
	}
	if result.Retryable {
		return ErrChallengeUnavailable
	}
	return ErrChallengeRejected
}

func (s *Server) requirePublicChallenge(w http.ResponseWriter, r *http.Request, token, action string) bool {
	if s.publicChallenge == nil {
		writeError(w, http.StatusServiceUnavailable, "CHALLENGE_UNAVAILABLE", "人机验证暂不可用，请稍后再试")
		return false
	}
	if len(token) == 0 || len(token) > 2048 {
		writeError(w, http.StatusForbidden, "CHALLENGE_REJECTED", "请重新完成人机验证")
		return false
	}
	err := s.publicChallenge.Verify(r.Context(), token, s.remoteIP(r), action)
	if err == nil {
		return true
	}
	if errors.Is(err, ErrChallengeRejected) {
		writeError(w, http.StatusForbidden, "CHALLENGE_REJECTED", "请重新完成人机验证")
	} else {
		writeError(w, http.StatusServiceUnavailable, "CHALLENGE_UNAVAILABLE", "人机验证暂不可用，请稍后再试")
	}
	return false
}

func (s *Server) publicChallengeConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.publicChallenge == nil || s.cfg.TurnstileSiteKey == "" {
		writeError(w, http.StatusServiceUnavailable, "CHALLENGE_UNAVAILABLE", "人机验证暂不可用，请稍后再试")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"siteKey": s.cfg.TurnstileSiteKey})
}

// SetPublicChallenges must be called before accepting traffic.
func (s *Server) SetPublicChallenges(verifier PublicChallengeVerifier) { s.publicChallenge = verifier }
