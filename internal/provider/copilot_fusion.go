package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// copilotHasFusion asks the same metadata API as Copilot CLI 1.0.107.
// Standard is an "auto" tier with tier_level "standard", not "fusion".
// High and Max need the client's orchestration runtime and aren't listed.
func copilotHasFusion(ctx context.Context, app copilotApp, s copilotSession) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.apiBase(app.Host)+"/meta", nil)
	if err != nil {
		return false
	}
	for k, v := range s.headers() {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("X-GitHub-Api-Version", "2026-09-21")
	res, err := Keyed(http.DefaultClient).Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	var meta struct {
		Auto struct {
			Tiers []struct {
				ID     string `json:"id"`
				Level  string `json:"tier_level"`
				Status struct {
					Enabled bool  `json:"enabled"`
					Picker  *bool `json:"tier_picker_enabled"`
				} `json:"status"`
			} `json:"tiers"`
		} `json:"auto"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&meta) != nil {
		return false
	}
	for _, tier := range meta.Auto.Tiers {
		if tier.ID == "balance" && tier.Level == "standard" && tier.Status.Enabled && (tier.Status.Picker == nil || *tier.Status.Picker) {
			return true
		}
	}
	return false
}
