package views

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/vortex-run/vortex/internal/tui"
)

func TestSecurity_Init(t *testing.T) {
	if NewSecurity(nil).Init() == nil {
		t.Error("Init should return a fetch command")
	}
}

func TestSecurity_ScorePerfect(t *testing.T) {
	m := NewSecurity(nil)
	updated, _ := m.Update(securityData{
		status: &tui.StatusData{TrustDomain: "c1.vortex", TLSProvider: "internal", PolicyDefault: false, AuditCount: 100},
		secrets: &tui.SecretsData{Secrets: []tui.SecretStatusData{
			{Name: "a", Set: true}, {Name: "b", Set: true},
		}},
	})
	sm := updated.(SecurityModel)
	if sm.Score() != 100 {
		t.Errorf("score = %d, want 100 (all controls met)", sm.Score())
	}
	if !strings.Contains(sm.View(), "Score: 100/100") {
		t.Errorf("view should show score:\n%s", sm.View())
	}
}

func TestSecurity_ScorePartial(t *testing.T) {
	m := NewSecurity(nil)
	updated, _ := m.Update(securityData{
		// No mTLS, default policy, unset secrets, audit active, TLS set.
		status:  &tui.StatusData{TrustDomain: "", TLSProvider: "letsencrypt", PolicyDefault: true, AuditCount: 5},
		secrets: &tui.SecretsData{Secrets: []tui.SecretStatusData{{Name: "a", Set: false}}},
	})
	sm := updated.(SecurityModel)
	// +20 TLS, +20 audit = 40 (mTLS off, secrets unset, policy default).
	if sm.Score() != 40 {
		t.Errorf("score = %d, want 40", sm.Score())
	}
}

func TestSecurity_ErrorState(t *testing.T) {
	m := NewSecurity(nil)
	updated, _ := m.Update(securityData{err: errString("x")})
	if !strings.Contains(updated.View(), "Could not load security") {
		t.Errorf("error should render:\n%s", updated.View())
	}
}

func sampleSecrets() []tui.SecretStatusData {
	return []tui.SecretStatusData{
		{Name: "db_password", Set: true},
		{Name: "jwt_secret", Set: false},
	}
}

func TestSecrets_Init(t *testing.T) {
	if NewSecrets(nil).Init() == nil {
		t.Error("Init should return a fetch command")
	}
}

func TestSecrets_ListsWithStatus(t *testing.T) {
	m := NewSecrets(nil)
	updated, _ := m.Update(secretsModelData{secrets: sampleSecrets()})
	out := updated.View()
	if !strings.Contains(out, "db_password") || !strings.Contains(out, "jwt_secret") {
		t.Errorf("view should list secret names:\n%s", out)
	}
	if !strings.Contains(out, "set") || !strings.Contains(out, "not set") {
		t.Errorf("view should show set/not-set status:\n%s", out)
	}
}

func TestSecrets_EchoPassword(t *testing.T) {
	m := NewSecrets(nil)
	if m.input.EchoMode != textinput.EchoPassword {
		t.Error("secret input should use EchoPassword mode")
	}
}

func TestSecrets_SetEntersEditing(t *testing.T) {
	m := NewSecrets(nil)
	loaded, _ := m.Update(secretsModelData{secrets: sampleSecrets()})
	editing, _ := loaded.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if !editing.(SecretsModel).Editing() {
		t.Error("'s' should enter inline editing")
	}
	// Esc cancels.
	cancelled, _ := editing.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cancelled.(SecretsModel).Editing() {
		t.Error("Esc should cancel editing")
	}
}

func TestSecrets_Navigation(t *testing.T) {
	m := NewSecrets(nil)
	loaded, _ := m.Update(secretsModelData{secrets: sampleSecrets()})
	down, _ := loaded.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if down.(SecretsModel).Selected() != 1 {
		t.Errorf("j should select index 1, got %d", down.(SecretsModel).Selected())
	}
}

func TestSecrets_EmptyState(t *testing.T) {
	m := NewSecrets(nil)
	updated, _ := m.Update(secretsModelData{secrets: nil})
	if !strings.Contains(updated.View(), "no declared secrets") {
		t.Errorf("empty secrets should show placeholder:\n%s", updated.View())
	}
}
