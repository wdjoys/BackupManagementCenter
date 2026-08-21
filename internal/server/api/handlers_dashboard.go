package api

import (
	"net/http"
	"time"

	"backupmanagementcenter/internal/server/store"
)
// GET /dashboard — aggregate counters for the landing page.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := map[string]any{}

	agents, err := s.ST.ListAgents(ctx)
	if err == nil {
		online := 0
		for _, a := range agents {
			if a.Status == "online" {
				online++
			}
		}
		out["agents_online"] = online
		out["agents_total"] = len(agents)
	} else {
		out["agents_online"], out["agents_total"] = 0, 0
	}

	runs, err := s.ST.ListRuns(ctx, store.RunFilter{Limit: 200})
	if err == nil {
		succ, fail := 0, 0
		dayAgo := time.Now().UTC().Add(-24 * time.Hour)
		for i := range runs {
			if runs[i].QueuedAt.Before(dayAgo) {
				continue
			}
			switch runs[i].Status {
			case "succeeded":
				succ++
			case "failed":
				fail++
			}
		}
		out["runs_24h_succeeded"] = succ
		out["runs_24h_failed"] = fail
	} else {
		out["runs_24h_succeeded"], out["runs_24h_failed"] = 0, 0
	}

	type nextFire struct {
		PlanID     string `json:"plan_id"`
		PlanName   string `json:"plan_name"`
		NextFireAt string `json:"next_fire_at"`
	}
	plans, err := s.ST.ListPlans(ctx, "")
	if err == nil {
		fires := make([]nextFire, 0, 8)
		for _, p := range plans {
			if !p.Enabled {
				continue
			}
			next, err := s.nextFireFor(ctx, p.ID)
			if err != nil || next.IsZero() {
				continue
			}
			fires = append(fires, nextFire{PlanID: p.ID, PlanName: p.Name, NextFireAt: next.Format(timeRFC3339)})
		}
		// soonest first, cap 8
		for i := 0; i < len(fires); i++ {
			for j := i + 1; j < len(fires); j++ {
				if fires[j].NextFireAt < fires[i].NextFireAt {
					fires[i], fires[j] = fires[j], fires[i]
				}
			}
		}
		if len(fires) > 8 {
			fires = fires[:8]
		}
		out["next_scheduled"] = fires
	}

	repos, err := s.ST.ListRepositoriesNeedingCheck(ctx, time.Now().UTC().Add(-7*24*time.Hour))
	if err == nil {
		type repoCheck struct {
			ID          string  `json:"id"`
			Path        string  `json:"repository_path"`
			LastCheckAt *string `json:"last_check_at,omitempty"`
		}
		list := make([]repoCheck, 0, len(repos))
		for _, rp := range repos {
			rc := repoCheck{ID: rp.ID, Path: rp.RepositoryPath}
			if rp.LastCheckAt != nil {
				s := rp.LastCheckAt.Format(timeRFC3339)
				rc.LastCheckAt = &s
			}
			list = append(list, rc)
		}
		out["repos_needing_check"] = list
	}

	writeJSON(w, http.StatusOK, out)
}
