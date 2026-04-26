package minimax

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
)

const (
	defaultMiniMaxUsageQueryBaseURL      = "https://www.minimaxi.com"
	defaultMiniMaxLowRemainingRatio      = 0.10
	defaultMiniMaxRefreshIntervalSeconds = 60
	minMiniMaxRefreshIntervalSeconds     = 15
)

type RuntimeKeyConfig struct {
	TokenPlanKey           string  `json:"token_plan_key"`
	APIKey                 string  `json:"api_key,omitempty"`
	FallbackAPIKey         string  `json:"fallback_api_key,omitempty"`
	FallbackBaseURL        string  `json:"fallback_base_url,omitempty"`
	UsageQueryBaseURL      string  `json:"usage_query_base_url,omitempty"`
	GroupID                string  `json:"group_id,omitempty"`
	LowRemainingRatio      float64 `json:"low_remaining_ratio,omitempty"`
	LowRemaining           float64 `json:"low_remaining,omitempty"`
	RefreshIntervalSeconds int     `json:"refresh_interval_seconds,omitempty"`
	DisableAutoSwitch      bool    `json:"disable_auto_switch,omitempty"`
}

type UsageMetric struct {
	Name      string  `json:"name"`
	Path      string  `json:"path"`
	Total     float64 `json:"total,omitempty"`
	Used      float64 `json:"used,omitempty"`
	Remaining float64 `json:"remaining,omitempty"`
}

type UsageSnapshot struct {
	CheckedAt           int64         `json:"checked_at"`
	UpstreamStatus      int           `json:"upstream_status"`
	GroupID             string        `json:"group_id,omitempty"`
	UsageQueryBaseURL   string        `json:"usage_query_base_url,omitempty"`
	FallbackRecommended bool          `json:"fallback_recommended"`
	FallbackReason      string        `json:"fallback_reason,omitempty"`
	MatchedMetric       *UsageMetric  `json:"matched_metric,omitempty"`
	Metrics             []UsageMetric `json:"metrics,omitempty"`
	Raw                 any           `json:"raw,omitempty"`
}

type usageCacheEntry struct {
	expiresAt time.Time
	snapshot  UsageSnapshot
	err       error
}

var usageCache sync.Map

func ParseRuntimeKeyConfig(raw string) (RuntimeKeyConfig, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return RuntimeKeyConfig{}, fmt.Errorf("empty minimax key")
	}
	if !strings.HasPrefix(trimmed, "{") {
		cfg := RuntimeKeyConfig{TokenPlanKey: trimmed}
		return normalizeRuntimeKeyConfig(cfg), nil
	}

	var cfg RuntimeKeyConfig
	if err := common.UnmarshalJsonStr(trimmed, &cfg); err != nil {
		return RuntimeKeyConfig{}, fmt.Errorf("invalid minimax key json: %w", err)
	}
	if strings.TrimSpace(cfg.TokenPlanKey) == "" {
		cfg.TokenPlanKey = strings.TrimSpace(cfg.APIKey)
	}
	cfg = normalizeRuntimeKeyConfig(cfg)
	if cfg.TokenPlanKey == "" {
		return RuntimeKeyConfig{}, fmt.Errorf("minimax token_plan_key is required")
	}
	return cfg, nil
}

func normalizeRuntimeKeyConfig(cfg RuntimeKeyConfig) RuntimeKeyConfig {
	cfg.TokenPlanKey = strings.TrimSpace(cfg.TokenPlanKey)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.FallbackAPIKey = strings.TrimSpace(cfg.FallbackAPIKey)
	cfg.FallbackBaseURL = strings.TrimRight(strings.TrimSpace(cfg.FallbackBaseURL), "/")
	cfg.UsageQueryBaseURL = strings.TrimRight(strings.TrimSpace(cfg.UsageQueryBaseURL), "/")
	cfg.GroupID = strings.TrimSpace(cfg.GroupID)
	if cfg.TokenPlanKey == "" {
		cfg.TokenPlanKey = cfg.APIKey
	}
	if cfg.GroupID == "" {
		if groupID, ok := extractJWTGroupID(cfg.FallbackAPIKey); ok {
			cfg.GroupID = groupID
		} else if groupID, ok := extractJWTGroupID(cfg.TokenPlanKey); ok {
			cfg.GroupID = groupID
		}
	}
	if cfg.UsageQueryBaseURL == "" {
		cfg.UsageQueryBaseURL = defaultMiniMaxUsageQueryBaseURL
	}
	if cfg.LowRemainingRatio <= 0 {
		cfg.LowRemainingRatio = defaultMiniMaxLowRemainingRatio
	} else if cfg.LowRemainingRatio > 1 && cfg.LowRemainingRatio <= 100 {
		cfg.LowRemainingRatio = cfg.LowRemainingRatio / 100
	}
	if cfg.RefreshIntervalSeconds <= 0 {
		cfg.RefreshIntervalSeconds = defaultMiniMaxRefreshIntervalSeconds
	}
	if cfg.RefreshIntervalSeconds < minMiniMaxRefreshIntervalSeconds {
		cfg.RefreshIntervalSeconds = minMiniMaxRefreshIntervalSeconds
	}
	return cfg
}

func ApplyRuntimeTarget(info *relaycommon.RelayInfo) error {
	if info == nil {
		return nil
	}
	cfg, err := ParseRuntimeKeyConfig(info.ApiKey)
	if err != nil {
		return err
	}
	if cfg.DisableAutoSwitch || cfg.FallbackAPIKey == "" || cfg.GroupID == "" {
		info.ApiKey = cfg.TokenPlanKey
		return nil
	}

	snapshot, err := GetUsageSnapshot(info, cfg)
	if err != nil {
		info.ApiKey = cfg.TokenPlanKey
		return nil
	}
	if !snapshot.FallbackRecommended {
		info.ApiKey = cfg.TokenPlanKey
		return nil
	}

	info.ApiKey = cfg.FallbackAPIKey
	if cfg.FallbackBaseURL != "" {
		info.ChannelBaseUrl = cfg.FallbackBaseURL
		if info.ChannelMeta != nil {
			info.ChannelMeta.ChannelBaseUrl = cfg.FallbackBaseURL
		}
	}
	return nil
}

func GetUsageSnapshot(info *relaycommon.RelayInfo, cfg RuntimeKeyConfig) (UsageSnapshot, error) {
	if info == nil {
		return UsageSnapshot{}, fmt.Errorf("nil relay info")
	}
	cacheKey := usageCacheKey(info, cfg.TokenPlanKey)
	if cacheKey != "" {
		if value, ok := usageCache.Load(cacheKey); ok {
			if entry, ok := value.(usageCacheEntry); ok && time.Now().Before(entry.expiresAt) {
				return entry.snapshot, entry.err
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := service.NewProxyHttpClient(info.ChannelSetting.Proxy)
	if err != nil {
		return UsageSnapshot{}, err
	}
	statusCode, body, err := fetchTokenPlanRemains(ctx, client, cfg)
	if err != nil {
		return UsageSnapshot{}, err
	}

	var payload any
	if err = common.Unmarshal(body, &payload); err != nil {
		payload = string(body)
	}

	snapshot := buildUsageSnapshot(payload, statusCode, info.OriginModelName, cfg)
	entry := usageCacheEntry{
		expiresAt: time.Now().Add(time.Duration(cfg.RefreshIntervalSeconds) * time.Second),
		snapshot:  snapshot,
	}
	if cacheKey != "" {
		usageCache.Store(cacheKey, entry)
	}
	return snapshot, nil
}

func fetchTokenPlanRemains(ctx context.Context, client *http.Client, cfg RuntimeKeyConfig) (int, []byte, error) {
	if client == nil {
		return 0, nil, fmt.Errorf("nil http client")
	}
	if cfg.TokenPlanKey == "" {
		return 0, nil, fmt.Errorf("empty token plan key")
	}
	if cfg.GroupID == "" {
		return 0, nil, fmt.Errorf("empty minimax group id")
	}

	baseURL := strings.TrimRight(strings.TrimSpace(cfg.UsageQueryBaseURL), "/")
	if baseURL == "" {
		baseURL = defaultMiniMaxUsageQueryBaseURL
	}
	queryURL := fmt.Sprintf("%s/v1/api/openplatform/coding_plan/remains?GroupId=%s", baseURL, neturl.QueryEscape(cfg.GroupID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, queryURL, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.TokenPlanKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "MiniMax API Client")
	req.Header.Set("Referer", "https://platform.minimaxi.com/")

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

func buildUsageSnapshot(payload any, statusCode int, modelName string, cfg RuntimeKeyConfig) UsageSnapshot {
	metrics := collectUsageMetrics(payload, "$")
	if len(metrics) == 0 {
		metrics = collectMiniMaxModelRemainsMetrics(payload)
	}
	matched := selectUsageMetric(metrics, modelName)
	shouldFallback, reason := shouldUseFallback(cfg, matched)
	return UsageSnapshot{
		CheckedAt:           time.Now().Unix(),
		UpstreamStatus:      statusCode,
		GroupID:             cfg.GroupID,
		UsageQueryBaseURL:   cfg.UsageQueryBaseURL,
		FallbackRecommended: shouldFallback,
		FallbackReason:      reason,
		MatchedMetric:       matched,
		Metrics:             metrics,
		Raw:                 payload,
	}
}

func collectMiniMaxModelRemainsMetrics(payload any) []UsageMetric {
	root, ok := payload.(map[string]any)
	if !ok || root == nil {
		return nil
	}
	rawItems, ok := root["model_remains"]
	if !ok {
		return nil
	}
	items := make([]map[string]any, 0)
	switch typed := rawItems.(type) {
	case []any:
		for _, item := range typed {
			if m, ok := item.(map[string]any); ok && m != nil {
				items = append(items, m)
			}
		}
	case []map[string]any:
		items = append(items, typed...)
	default:
		return nil
	}
	metrics := make([]UsageMetric, 0, len(items))
	for idx, m := range items {
		name := firstStringByKeys(m, "model_name", "name", "model")
		if name == "" {
			name = fmt.Sprintf("model_remains[%d]", idx)
		}
		total, totalOK := firstNumberByKeys(m, "current_interval_total_count")
		used, usedOK := firstNumberByKeys(m, "current_interval_usage_count")
		if !totalOK && !usedOK {
			continue
		}
		remaining := total - used
		metrics = append(metrics, UsageMetric{
			Name:      name,
			Path:      fmt.Sprintf("$.model_remains[%d]", idx),
			Total:     total,
			Used:      used,
			Remaining: remaining,
		})
	}
	return metrics
}

func shouldUseFallback(cfg RuntimeKeyConfig, metric *UsageMetric) (bool, string) {
	if cfg.DisableAutoSwitch || cfg.FallbackAPIKey == "" || metric == nil {
		return false, ""
	}
	if metric.Remaining <= 0 {
		return true, fmt.Sprintf("remaining quota %.0f <= 0", metric.Remaining)
	}
	if cfg.LowRemaining > 0 && metric.Remaining <= cfg.LowRemaining {
		return true, fmt.Sprintf("remaining quota %.0f <= threshold %.0f", metric.Remaining, cfg.LowRemaining)
	}
	if metric.Total > 0 && cfg.LowRemainingRatio > 0 {
		ratio := metric.Remaining / metric.Total
		if ratio <= cfg.LowRemainingRatio {
			return true, fmt.Sprintf("remaining ratio %.4f <= threshold %.4f", ratio, cfg.LowRemainingRatio)
		}
	}
	return false, ""
}

func collectUsageMetrics(node any, path string) []UsageMetric {
	switch value := node.(type) {
	case map[string]any:
		metrics := make([]UsageMetric, 0)
		if metric, ok := buildUsageMetric(value, path); ok {
			metrics = append(metrics, metric)
		}
		for key, child := range value {
			metrics = append(metrics, collectUsageMetrics(child, path+"."+key)...)
		}
		return metrics
	case []any:
		metrics := make([]UsageMetric, 0)
		for idx, child := range value {
			metrics = append(metrics, collectUsageMetrics(child, fmt.Sprintf("%s[%d]", path, idx))...)
		}
		return metrics
	default:
		return nil
	}
}

func buildUsageMetric(m map[string]any, path string) (UsageMetric, bool) {
	total, totalOK := firstNumberByKeys(
		m,
		"total",
		"limit",
		"quota",
		"max",
		"ceiling",
		"total_amount",
		"current_interval_total_count",
		"current_weekly_total_count",
	)
	used, usedOK := firstNumberByKeys(
		m,
		"used",
		"usage",
		"consumed",
		"cost",
		"used_amount",
		"current_interval_usage_count",
		"current_weekly_usage_count",
	)
	remaining, remainingOK := firstNumberByKeys(
		m,
		"remaining",
		"remains",
		"available",
		"rest",
		"left",
		"remain",
	)
	if !remainingOK && totalOK && usedOK {
		remaining = total - used
		remainingOK = true
	}
	if !totalOK && !usedOK && !remainingOK {
		return UsageMetric{}, false
	}
	name := firstStringByKeys(m, "model", "model_name", "name", "type", "scene", "resource", "modality")
	if name == "" {
		name = path
	}
	return UsageMetric{
		Name:      name,
		Path:      path,
		Total:     total,
		Used:      used,
		Remaining: remaining,
	}, true
}

func selectUsageMetric(metrics []UsageMetric, modelName string) *UsageMetric {
	if len(metrics) == 0 {
		return nil
	}
	modelLower := strings.ToLower(strings.TrimSpace(modelName))
	bestIdx := -1
	bestScore := -1
	for idx := range metrics {
		score := scoreUsageMetric(metrics[idx], modelLower)
		if score > bestScore {
			bestScore = score
			bestIdx = idx
		}
	}
	if bestIdx < 0 {
		return nil
	}
	metric := metrics[bestIdx]
	return &metric
}

func scoreUsageMetric(metric UsageMetric, modelLower string) int {
	score := 0
	nameLower := strings.ToLower(metric.Name + " " + metric.Path)
	if modelLower != "" && strings.Contains(nameLower, modelLower) {
		score += 100
	}
	if modelLower != "" && strings.HasPrefix(modelLower, "minimax-m2") && strings.Contains(nameLower, "minimax-m*") {
		score += 90
	}
	if modelLower == "m2-her" && strings.Contains(nameLower, "minimax-m*") {
		score += 90
	}
	switch modelFamily(modelLower) {
	case "text":
		if strings.Contains(nameLower, "text") || strings.Contains(nameLower, "chat") || strings.Contains(nameLower, "m2") {
			score += 40
		}
	case "audio":
		if strings.Contains(nameLower, "speech") || strings.Contains(nameLower, "tts") || strings.Contains(nameLower, "audio") {
			score += 40
		}
	case "image":
		if strings.Contains(nameLower, "image") {
			score += 40
		}
	case "music":
		if strings.Contains(nameLower, "music") {
			score += 40
		}
	case "video":
		if strings.Contains(nameLower, "video") || strings.Contains(nameLower, "hailuo") || strings.Contains(nameLower, "t2v") || strings.Contains(nameLower, "i2v") || strings.Contains(nameLower, "s2v") {
			score += 40
		}
	}
	if metric.Total > 0 {
		score += 10
	}
	if metric.Remaining > 0 {
		score += 5
	}
	return score
}

func modelFamily(modelLower string) string {
	switch {
	case strings.HasPrefix(modelLower, "speech-"):
		return "audio"
	case strings.HasPrefix(modelLower, "image-"):
		return "image"
	case strings.HasPrefix(modelLower, "music-"):
		return "music"
	case strings.Contains(modelLower, "hailuo") || strings.HasPrefix(modelLower, "t2v-") || strings.HasPrefix(modelLower, "i2v-") || strings.HasPrefix(modelLower, "s2v-"):
		return "video"
	case strings.HasPrefix(modelLower, "minimax-m2"):
		return "text"
	default:
		return ""
	}
}

func firstNumberByKeys(m map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		for actual, value := range m {
			if strings.EqualFold(strings.TrimSpace(actual), key) {
				if number, ok := toFloat64(value); ok {
					return number, true
				}
			}
		}
	}
	return 0, false
}

func firstStringByKeys(m map[string]any, keys ...string) string {
	for _, key := range keys {
		for actual, value := range m {
			if strings.EqualFold(strings.TrimSpace(actual), key) {
				if text, ok := value.(string); ok {
					return strings.TrimSpace(text)
				}
			}
		}
	}
	return ""
}

func toFloat64(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case json.Number:
		number, err := v.Float64()
		return number, err == nil
	case string:
		number, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return number, err == nil
	default:
		return 0, false
	}
}

func cacheKeyForToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:8])
}

func usageCacheKey(info *relaycommon.RelayInfo, tokenPlanKey string) string {
	if info != nil && info.ChannelId > 0 {
		return fmt.Sprintf("channel:%d", info.ChannelId)
	}
	if strings.TrimSpace(tokenPlanKey) == "" {
		return ""
	}
	return "token:" + cacheKeyForToken(tokenPlanKey)
}

func extractJWTGroupID(token string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return "", false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	var payload map[string]any
	if err = common.Unmarshal(payloadBytes, &payload); err != nil {
		return "", false
	}
	groupID, _ := payload["GroupID"].(string)
	groupID = strings.TrimSpace(groupID)
	return groupID, groupID != ""
}
