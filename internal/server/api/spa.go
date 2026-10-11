package api

import (
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/webui"
)

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// nextFireFromPlan computes the next fire time from the plan's own schedule and
// timezone. 仪表盘与计划列表/详情共用这一处现算逻辑，避免两处各算一套而互相矛盾
// （调度游标是调度器内部状态，见 internal/server/scheduler）。
func nextFireFromPlan(p *model.Plan, now time.Time) (time.Time, error) {
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	sched, err := cronParser.Parse(p.Schedule)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(now.In(loc)).UTC(), nil
}

// spaHandler serves the embedded web UI with index.html fallback.
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
