package minimax

import "testing"

func TestParseRuntimeKeyConfigFromJSON(t *testing.T) {
	t.Parallel()

	raw := `{
		"token_plan_key":"sk-token",
		"fallback_api_key":"eyJhbGciOiJSUzI1NiJ9.eyJHcm91cElEIjoiZ3JvdXAtMTIzIn0.signature",
		"low_remaining_ratio": 10,
		"refresh_interval_seconds": 5
	}`

	cfg, err := ParseRuntimeKeyConfig(raw)
	if err != nil {
		t.Fatalf("ParseRuntimeKeyConfig returned error: %v", err)
	}
	if cfg.TokenPlanKey != "sk-token" {
		t.Fatalf("TokenPlanKey = %q, want %q", cfg.TokenPlanKey, "sk-token")
	}
	if cfg.GroupID != "group-123" {
		t.Fatalf("GroupID = %q, want %q", cfg.GroupID, "group-123")
	}
	if cfg.LowRemainingRatio != 0.1 {
		t.Fatalf("LowRemainingRatio = %v, want 0.1", cfg.LowRemainingRatio)
	}
	if cfg.RefreshIntervalSeconds != minMiniMaxRefreshIntervalSeconds {
		t.Fatalf("RefreshIntervalSeconds = %d, want %d", cfg.RefreshIntervalSeconds, minMiniMaxRefreshIntervalSeconds)
	}
}

func TestShouldUseFallbackByRemainingRatio(t *testing.T) {
	t.Parallel()

	cfg := normalizeRuntimeKeyConfig(RuntimeKeyConfig{
		TokenPlanKey:      "sk-token",
		FallbackAPIKey:    "eyJhbGciOiJIUzI1NiJ9.eyJHcm91cElEIjoiZ3JvdXAtMTIzIn0.sig",
		LowRemainingRatio: 0.1,
	})
	metric := &UsageMetric{
		Name:      "MiniMax-M2.7",
		Total:     1500,
		Used:      1400,
		Remaining: 100,
	}

	shouldFallback, reason := shouldUseFallback(cfg, metric)
	if !shouldFallback {
		t.Fatalf("shouldUseFallback = false, want true")
	}
	if reason == "" {
		t.Fatalf("reason is empty")
	}
}
