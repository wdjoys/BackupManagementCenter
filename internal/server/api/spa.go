package api

import (
	"context"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"backupmanagementcenter/internal/server/webui"
)

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// nextFireFor computes the next fire time for a plan directly from its
// schedule expression and timezone (schedule cursors are scheduler-internal).
func (s *Server) nextFireFor(ctx context.Context, planID string) (time.Time, error) {
	p, err := s.ST.GetPlan(ctx, planID)
	if err != nil {
		return time.Time{}, err
	}
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	sched, err := cronParser.Parse(p.Schedule)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(time.Now().In(loc)).UTC(), nil
}

// spaHandler serves the embedded Vue app with index.html fallback.
func spaHandler() http.Handler {
	sub, err := fs.Sub(webui.Dist, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil {
			// SPA fallback
			r2 := new(http.Request)
			*r2 = *r
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
