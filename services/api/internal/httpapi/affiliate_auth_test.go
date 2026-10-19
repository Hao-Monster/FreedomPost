package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/domain"
	"github.com/fenghaoyun-monster/freedompost/services/api/internal/session"
	"golang.org/x/crypto/bcrypt"
)

type affiliateAuthRepository struct {
	domain.Repository
	affiliate            *domain.Affiliate
	lookups, creates     int
	lookupErr, createErr error
	raceWinner           *domain.Affiliate
}

func (r *affiliateAuthRepository) GetAffiliateByWechatID(context.Context, string) (*domain.Affiliate, error) {
	r.lookups++
	return r.affiliate, r.lookupErr
}
func (r *affiliateAuthRepository) CreateAffiliate(_ context.Context, wechatID, hash string) (*domain.Affiliate, error) {
	r.creates++
	if r.createErr != nil {
		r.affiliate = r.raceWinner
		return nil, r.createErr
	}
	r.affiliate = &domain.Affiliate{ID: "qa-affiliate", WechatID: wechatID, PasswordHash: hash, Status: domain.AffiliateStatusActive, CredentialVersion: 1}
	return r.affiliate, nil
}
func (r *affiliateAuthRepository) GetAffiliateDashboard(context.Context, string) (*domain.AffiliateDashboard, error) {
	return &domain.AffiliateDashboard{Affiliate: *r.affiliate}, nil
}

func affiliateAccessRequest(s *Server, intent, password, token, ip string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"wechatId": "QA_Account", "intent": intent, "password": password, "turnstileToken": token})
	r := httptest.NewRequest(http.MethodPost, "https://shop.example/api/affiliate/access", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://shop.example")
	r.RemoteAddr = net.JoinHostPort(ip, "12345")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func acceptAffiliateChallenge(s *Server) {
	s.publicChallenge = affiliateChallengeFunc(func(context.Context, string, string, string) error { return nil })
}

func TestAffiliateIntentChallengeAndPasswordChecksPrecedeLookup(t *testing.T) {
	for _, tc := range []struct {
		name, intent, password, token string
		configured                    bool
		err                           error
		want                          int
	}{
		{"intent required", "", "", "qa-token", true, nil, 400},
		{"unconfigured", "register", "", "qa-token", false, nil, 503},
		{"registration requires token", "register", "", "", true, nil, 403},
		{"login requires token", "login", "qa-password", "", true, nil, 403},
		{"wrong action or token", "register", "", "qa-token", true, ErrChallengeRejected, 403},
		{"upstream failed", "login", "qa-password", "qa-token", true, ErrChallengeUnavailable, 503},
		{"missing password", "login", "", "qa-token", true, nil, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &affiliateAuthRepository{}
			s := handoffTestServer(repo)
			if tc.configured {
				s.publicChallenge = affiliateChallengeFunc(func(_ context.Context, token, ip, action string) error {
					if action != "affiliate_"+tc.intent || ip != "192.0.2.1" {
						t.Fatalf("action=%s ip=%s", action, ip)
					}
					return tc.err
				})
			}
			w := affiliateAccessRequest(s, tc.intent, tc.password, tc.token, "192.0.2.1")
			if w.Code != tc.want || repo.lookups != 0 || repo.creates != 0 {
				t.Fatalf("status=%d lookup=%d create=%d body=%s", w.Code, repo.lookups, repo.creates, w.Body.String())
			}
		})
	}
}

func TestAffiliateLoginNeverRegistersAndUsesUniformFailure(t *testing.T) {
	hash, err := bcryptHashReal("qa-correct-password")
	if err != nil {
		t.Fatal(err)
	}
	want := ""
	for _, affiliate := range []*domain.Affiliate{nil, {Status: domain.AffiliateStatusDisabled, PasswordHash: hash}, {Status: domain.AffiliateStatusActive, PasswordHash: hash}} {
		repo := &affiliateAuthRepository{affiliate: affiliate}
		s := handoffTestServer(repo)
		acceptAffiliateChallenge(s)
		w := affiliateAccessRequest(s, "login", "qa-wrong-password", "qa-token", "192.0.2.1")
		if w.Code != 401 || repo.creates != 0 {
			t.Fatalf("status=%d creates=%d body=%s", w.Code, repo.creates, w.Body.String())
		}
		if want == "" {
			want = w.Body.String()
		} else if w.Body.String() != want {
			t.Fatal("unknown, disabled and incorrect password responses differ")
		}
		if len(s.affiliateAuthSlots) != 0 {
			t.Fatal("failure leaked authentication slot")
		}
	}
	cost, err := bcrypt.Cost([]byte(affiliateDummyPasswordHash))
	if err != nil || cost != bcryptCost {
		t.Fatalf("dummy cost=%d: %v", cost, err)
	}
	if err = bcryptVerifyReal(affiliateDummyPasswordHash, "qa-wrong-password"); !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		t.Fatalf("dummy hash did not perform a valid comparison: %v", err)
	}
}

func TestAffiliateRegistrationDoesNotLoginExistingOrRaceWinner(t *testing.T) {
	for _, race := range []bool{false, true} {
		existing := &domain.Affiliate{ID: "qa-existing", Status: domain.AffiliateStatusActive}
		repo := &affiliateAuthRepository{affiliate: existing}
		if race {
			repo.affiliate = nil
			repo.createErr = errors.New("synthetic unique conflict")
			repo.raceWinner = existing
		}
		s := handoffTestServer(repo)
		acceptAffiliateChallenge(s)
		w := affiliateAccessRequest(s, "register", "", "qa-token", "192.0.2.1")
		if w.Code != 409 || len(w.Result().Cookies()) != 0 || strings.Contains(w.Body.String(), "generatedPassword") {
			t.Fatalf("registration collision granted credentials: %d %s", w.Code, w.Body.String())
		}
		if len(s.affiliateAuthSlots) != 0 {
			t.Fatal("collision leaked slot")
		}
	}
}

func TestAffiliateIPAccountAndRegistrationBudgets(t *testing.T) {
	t.Run("registration budget", func(t *testing.T) {
		repo := &affiliateAuthRepository{affiliate: &domain.Affiliate{Status: domain.AffiliateStatusActive}}
		s := handoffTestServer(repo)
		acceptAffiliateChallenge(s)
		for i := 0; i < 4; i++ {
			w := affiliateAccessRequest(s, "register", "", "qa-token", "192.0.2.1")
			want := 409
			if i == 3 {
				want = 429
			}
			if w.Code != want {
				t.Fatalf("attempt %d status=%d", i, w.Code)
			}
		}
		if repo.lookups != 3 {
			t.Fatalf("quota reached lookup: %d", repo.lookups)
		}
	})
	for _, sameIP := range []bool{false, true} {
		repo := &affiliateAuthRepository{lookupErr: errors.New("synthetic lookup unavailable")}
		s := handoffTestServer(repo)
		checks := 0
		s.publicChallenge = affiliateChallengeFunc(func(context.Context, string, string, string) error { checks++; return nil })
		for i := 0; i < 13; i++ {
			ip := fmt.Sprintf("192.0.2.%d", i+1)
			if sameIP {
				ip = "192.0.2.1"
			}
			w := affiliateAccessRequest(s, "login", "qa-password", "qa-token", ip)
			want := 500
			if i == 12 {
				want = 429
			}
			if w.Code != want {
				t.Fatalf("sameIP=%v attempt=%d status=%d", sameIP, i, w.Code)
			}
		}
		wantChecks := 13
		if sameIP {
			wantChecks = 12
		}
		if checks != wantChecks || repo.lookups != 12 {
			t.Fatalf("quota did not protect lookup: checks=%d lookup=%d", checks, repo.lookups)
		}
	}
}

func TestAffiliateInvalidChallengeCannotExhaustAnotherUsersAccountBudget(t *testing.T) {
	repo := &affiliateAuthRepository{}
	s := handoffTestServer(repo)
	s.publicChallenge = affiliateChallengeFunc(func(_ context.Context, token, _, _ string) error {
		if token == "qa-invalid" {
			return ErrChallengeRejected
		}
		return nil
	})
	for i := range 12 {
		w := affiliateAccessRequest(s, "login", "qa-password", "qa-invalid", fmt.Sprintf("192.0.2.%d", i+1))
		if w.Code != 403 {
			t.Fatalf("unverified request status=%d", w.Code)
		}
	}
	w := affiliateAccessRequest(s, "login", "qa-password", "qa-valid", "198.51.100.1")
	if w.Code != 401 || repo.lookups != 1 {
		t.Fatalf("unverified traffic locked account: status=%d lookup=%d", w.Code, repo.lookups)
	}
}

func TestAffiliateGlobalBudgetRejectsWithoutQueueAndReleasesOnError(t *testing.T) {
	repo := &affiliateAuthRepository{lookupErr: errors.New("synthetic lookup unavailable")}
	s := handoffTestServer(repo)
	acceptAffiliateChallenge(s)
	first := s.reserveAffiliateAuth(httptest.NewRecorder())
	second := s.reserveAffiliateAuth(httptest.NewRecorder())
	if first == nil || second == nil || cap(s.affiliateAuthSlots) != 2 {
		t.Fatal("expected two process-wide slots")
	}
	w := affiliateAccessRequest(s, "register", "", "qa-token", "192.0.2.1")
	if w.Code != 429 || repo.lookups != 0 || !strings.Contains(w.Body.String(), "AUTH_BUSY") {
		t.Fatalf("busy request reached repository: %d %s", w.Code, w.Body.String())
	}
	first()
	second()
	w = affiliateAccessRequest(s, "register", "", "qa-token", "192.0.2.1")
	if w.Code != 500 || len(s.affiliateAuthSlots) != 0 {
		t.Fatalf("failure leaked slot: status=%d slots=%d", w.Code, len(s.affiliateAuthSlots))
	}
}

// This loopback RESP fixture exercises the real session.Store serialization and
// client without an external Redis service. It is not a Redis integration test.
func affiliateSessionFixture(t *testing.T) *session.Store {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	values := map[string]string{}
	var mu sync.Mutex
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "*")))
					if err != nil {
						return
					}
					args := make([]string, n)
					for i := range args {
						line, err = reader.ReadString('\n')
						if err != nil {
							return
						}
						size, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "$")))
						if err != nil || size < 0 {
							return
						}
						b := make([]byte, size+2)
						if _, err = io.ReadFull(reader, b); err != nil {
							return
						}
						args[i] = string(b[:size])
					}
					switch strings.ToLower(args[0]) {
					case "hello":
						fmt.Fprint(conn, "-ERR unknown command 'hello'\r\n")
					case "set":
						mu.Lock()
						values[args[1]] = args[2]
						mu.Unlock()
						fmt.Fprint(conn, "+OK\r\n")
					case "get":
						mu.Lock()
						value, ok := values[args[1]]
						mu.Unlock()
						if ok {
							fmt.Fprintf(conn, "$%d\r\n%s\r\n", len(value), value)
						} else {
							fmt.Fprint(conn, "$-1\r\n")
						}
					default:
						fmt.Fprint(conn, "+OK\r\n")
					}
				}
			}()
		}
	}()
	store, err := session.NewStore("redis://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestAffiliateRegistrationPasswordIsReturnedOnceAndCanLogin(t *testing.T) {
	repo := &affiliateAuthRepository{}
	s := handoffTestServer(repo)
	acceptAffiliateChallenge(s)
	s.sessions = affiliateSessionFixture(t)
	w := affiliateAccessRequest(s, "register", "", "qa-token", "192.0.2.1")
	var created struct {
		Created  bool   `json:"created"`
		Password string `json:"generatedPassword"`
	}
	if json.Unmarshal(w.Body.Bytes(), &created) != nil || w.Code != 200 || !created.Created || len(created.Password) != 12 || len(w.Result().Cookies()) != 1 {
		t.Fatalf("registration result: status=%d (body omitted)", w.Code)
	}
	if err := bcryptVerifyReal(repo.affiliate.PasswordHash, created.Password); err != nil {
		t.Fatal("generated password cannot verify stored hash")
	}
	w = affiliateAccessRequest(s, "login", created.Password, "qa-token", "192.0.2.1")
	var loggedIn struct {
		Created  bool   `json:"created"`
		Password string `json:"generatedPassword"`
	}
	if json.Unmarshal(w.Body.Bytes(), &loggedIn) != nil || w.Code != 200 || loggedIn.Created || loggedIn.Password != "" || repo.creates != 1 {
		t.Fatalf("login re-registered or returned password: status=%d", w.Code)
	}
	if len(s.affiliateAuthSlots) != 0 {
		t.Fatal("successful auth leaked slots")
	}
}
