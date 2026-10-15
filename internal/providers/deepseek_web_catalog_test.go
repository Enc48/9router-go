package providers

import "testing"

// The browser-login providers are distinct deployments reached with a credential
// a browser session produces, not an API key. Each test below pins a contract a
// user depends on: the catalog lists the provider, its aliases resolve to it and
// to nothing else, and its models are its own — not a subset borrowed from the
// API-key provider it shares a name with.

func TestBrowserLoginProvidersInCatalog(t *testing.T) {
	tests := []struct {
		id       string
		baseURL  string
		alias    string
		models   []string
		wantList bool
	}{
		{
			id:      "deepseek-web",
			baseURL: "https://chat.deepseek.com/api/v0/chat/completion",
			alias:   "ds-web",
			models: []string{
				"deepseek-v4-pro", "deepseek-v4-pro-think",
				"deepseek-v4-pro-search", "deepseek-v4-pro-think-search",
				"deepseek-v4-flash", "deepseek-v4-flash-think",
				"deepseek-v4-flash-search", "deepseek-v4-flash-think-search",
				"deepseek-chat", "deepseek-reasoner",
				"DeepSeek-R1", "DeepSeek-R1-Search",
				"DeepSeek-V3.2", "DeepSeek-Search",
			},
		},
		{
			id:      "gemini-web",
			baseURL: "https://gemini.google.com/app",
			alias:   "gweb",
			models:  []string{"gemini-3.1-pro", "gemini-3.7-flash", "gemini-3.1-flash-lite"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			cfg, ok := KnownProviders[tt.id]
			if !ok {
				t.Fatalf("KnownProviders has no entry for %q, so it cannot be selected for a connection", tt.id)
			}
			if cfg.BaseURL != tt.baseURL {
				t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, tt.baseURL)
			}

			if got := ResolveAlias(tt.alias); got != tt.id {
				t.Errorf("ResolveAlias(%q) = %q, want %q", tt.alias, got, tt.id)
			}
			if got := RegistryAliases[tt.id]; got != tt.alias {
				t.Errorf("RegistryAliases[%q] = %q, want %q", tt.id, got, tt.alias)
			}

			models := GetProviderModels(tt.id)
			if len(models) != len(tt.models) {
				t.Fatalf("GetProviderModels(%q) returned %d models, want %d", tt.id, len(models), len(tt.models))
			}
			for _, model := range tt.models {
				if !slicesContains(models, model) {
					t.Errorf("GetProviderModels(%q) is missing %q", tt.id, model)
				}
			}

			// The alias must reach the same catalogue, or a client asking for
			// "ds-web/deepseek-chat" would resolve to a provider with no models.
			if got := GetProviderModels(tt.alias); len(got) != len(tt.models) {
				t.Errorf("GetProviderModels(%q) returned %d models, want %d", tt.alias, len(got), len(tt.models))
			}
		})
	}
}

// A web provider shares a name with an API-key provider, and the two have
// different credentials, different quota and in deepseek-web's case a different
// upstream host. Resolving one alias to the other would send a browser-session
// credential to an API endpoint that answers 401, or worse, route a paid API key
// to the free web site — so the separation is pinned (AGENTS.md section 3.A).
func TestBrowserLoginProvidersAreIsolated(t *testing.T) {
	tests := []struct {
		alias   string
		want    string
		notThis []string
	}{
		{
			alias:   "ds-web",
			want:    "deepseek-web",
			notThis: []string{"deepseek", "ds", "sumopod"},
		},
		{
			alias:   "gweb",
			want:    "gemini-web",
			notThis: []string{"gemini", "gemini-cli", "gc"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.alias, func(t *testing.T) {
			resolved := ResolveAlias(tt.alias)
			if resolved != tt.want {
				t.Errorf("ResolveAlias(%q) = %q, want %q", tt.alias, resolved, tt.want)
			}
			for _, other := range tt.notThis {
				if resolved == other {
					t.Errorf("alias %q resolved to the API-key provider %q", tt.alias, other)
				}
			}
		})
	}

	// The existing short aliases must keep pointing where they always did.
	if got := ResolveAlias("ds"); got != "deepseek" {
		t.Errorf("ResolveAlias(\"ds\") = %q, want deepseek", got)
	}
	if got := ResolveAlias("gc"); got != "gemini-cli" {
		t.Errorf("ResolveAlias(\"gc\") = %q, want gemini-cli", got)
	}

	// And the two hosts must not be shared: a connection override that reached
	// the wrong host would send one site's credential to the other.
	if KnownProviders["deepseek-web"].BaseURL == KnownProviders["deepseek"].BaseURL {
		t.Error("deepseek-web and deepseek share a base URL")
	}
	if KnownProviders["gemini-web"].BaseURL == KnownProviders["gemini"].BaseURL {
		t.Error("gemini-web and gemini share a base URL")
	}
}
