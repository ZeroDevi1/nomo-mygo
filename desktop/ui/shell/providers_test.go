package shell

import (
	"github.com/egoist/mygo/ui/editor"
	"os"
	"path/filepath"
	"testing"
)

func TestProviderSettingsDetachAndRestore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paper.tex")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	s, tt := newTestShell(t, path)
	tab := s.activeTab()
	st := tab.Source()
	buf := st.Buffer
	buf.Edit(editor.Edit{Start: buf.Len(), End: buf.Len(), Text: " edit"})
	st.Select(0, 3)
	check := func(syntax, preview bool) {
		t.Helper()
		tt.Frame()
		if (st.OnCompletion != nil) != syntax || (buf.Language() != nil) != syntax || tt.HasText(s.tr("semanticEditing")) != preview {
			t.Fatal("capability mismatch", tt.Texts())
		}
		if st != tab.Source() || buf != st.Buffer || st.Cursor() != 3 || buf.String() != "old edit" || !tab.Dirty() {
			t.Fatal("source state changed")
		}
	}
	check(true, true)
	s.prefs.ProviderSyntax = false
	s.setPref("providerSyntax")
	check(false, true)
	s.prefs.DisabledProviders = map[string]bool{"latex": true}
	s.setPref("disabledProviders")
	check(false, false)
	prefs := loadPrefs(s.cfg)
	if prefs.ProviderSyntax || !prefs.DisabledProviders["latex"] {
		t.Fatal("settings not persisted")
	}
	s.prefs.DisabledProviders = nil
	s.setPref("disabledProviders")
	s.prefs.ProviderSyntax = true
	s.setPref("providerSyntax")
	s.prefs.ProviderPreview = false
	s.setPref("providerPreview")
	check(true, false)
	s.openTypeset(tab, true)
	if s.compile != nil {
		t.Fatal("disabled preview created session")
	}
}
