package auth

import (
	"github.com/bitofbytes-io/carma/internal/repository"
	"strings"
	"testing"
	"time"
)

func TestSessionsAreOpaqueAndStoredHashed(t *testing.T) {
	m := repository.NewMemory()
	s := NewService(m, 90*24*time.Hour)
	u, token, e := s.DevLogin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	if len(token) < 40 || strings.Contains(token, u.Email) {
		t.Fatalf("bad token %q", token)
	}
	if found, e := s.Validate(t.Context(), token); e != nil || found == nil || found.ID != u.ID {
		t.Fatalf("found=%v err=%v", found, e)
	}
	if sess, _, _ := m.FindSession(t.Context(), token); sess != nil {
		t.Fatal("raw token was stored")
	}
}

func TestEmailAndDomainAllowlistIsCaseInsensitive(t *testing.T) {
	g := &GoogleOIDC{emails: map[string]struct{}{"person@example.com": {}}, domains: map[string]struct{}{"family.test": {}}}
	for _, email := range []string{"Person@Example.com", "anyone@FAMILY.TEST"} {
		if !g.Allowed(email) {
			t.Fatalf("expected %s allowed", email)
		}
	}
	if g.Allowed("stranger@elsewhere.test") {
		t.Fatal("non-allowlisted email accepted")
	}
}
