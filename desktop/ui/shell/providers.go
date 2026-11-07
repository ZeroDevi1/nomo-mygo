package shell

import (
	"nomo/desktop/provider"
	"nomo/desktop/providers"
)

func (s *Shell) enabledProvider(tab *Tab) *provider.Descriptor {
	if tab == nil {
		return nil
	}
	p := providers.ForPath(tab.Path)
	if p == nil || s.prefs.DisabledProviders[p.ID] {
		return nil
	}
	return p
}
func (s *Shell) previewProvider(tab *Tab) *provider.Descriptor {
	if !s.prefs.ProviderPreview {
		return nil
	}
	return s.enabledProvider(tab)
}

// 切换语言只更新原有 rope 的属性，保留撤销、选区与未保存内容。
func (s *Shell) applyProviderSyntax(tab *Tab) {
	if tab.source == nil {
		return
	}
	var want *provider.Descriptor
	if s.prefs.ProviderSyntax {
		want = s.enabledProvider(tab)
	}
	if tab.syntaxProvider == want {
		return
	}
	st := tab.source
	if tab.syntaxProvider != nil || tab.scholarBound {
		st.ShowCompletions(-1, nil)
		st.OnCompletion = nil
		st.OnHover = nil
		st.RenderHover = nil
		st.OnDefinition = nil
		tab.scholarBound = false
		st.OnInput = nil
		tab.afterSourceInput = nil
		st.Buffer.SetLanguage(nil)
	}
	tab.syntaxProvider = want
	if want != nil {
		st.Buffer.SetLanguage(want.Language)
		if want.BindHints != nil {
			tab.afterSourceInput = want.BindHints(st)
		}
	}
}

// 设置窗口在 UI 线程调用。后台标签仅解绑已停用的能力，仍启用的标签在进入前台时按需绑定。
func (s *Shell) releaseDisabledProviders() {
	for _, tab := range s.tabs {
		if tab.syntaxProvider != nil && (!s.prefs.ProviderSyntax || s.enabledProvider(tab) == nil) {
			s.applyProviderSyntax(tab)
		}
	}
	if p := s.compile; p != nil && s.previewProvider(p.tab) == nil {
		s.closeCompile()
	}
}
