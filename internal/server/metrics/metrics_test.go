package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// bmc_agents_online 此前是一个从未被赋值的普通 Gauge，导出恒为 0（与真实在线数无关）。
// 改为抓取时回调计算后，值必须随回调变化。
func TestAgentsOnlineGaugeIsComputedAtScrapeTime(t *testing.T) {
	m := New()
	online := 0
	m.SetAgentsOnlineFunc(func() float64 { return float64(online) })

	read := func() string {
		rec := httptest.NewRecorder()
		m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		for _, line := range strings.Split(rec.Body.String(), "\n") {
			if strings.HasPrefix(line, "bmc_agents_online ") {
				return strings.TrimSpace(strings.TrimPrefix(line, "bmc_agents_online "))
			}
		}
		return ""
	}
	if got := read(); got != "0" {
		t.Fatalf("initial bmc_agents_online = %q, want 0", got)
	}
	online = 3
	if got := read(); got != "3" {
		t.Fatalf("after change bmc_agents_online = %q, want 3 (must be computed at scrape time)", got)
	}
}

// 仓库最近校验时间与流（重）连计数此前也从未被赋值（指标完全不导出 / 恒为 0）。
func TestRepoCheckAndReconnectsAreExported(t *testing.T) {
	m := New()
	at := time.Unix(1700000000, 0).UTC()
	m.SetRepoCheck("repo-1", at)
	m.IncReconnects()
	m.IncReconnects()

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `bmc_repository_last_check_timestamp{repository_id="repo-1"} 1.7e+09`) {
		t.Fatalf("repository last check metric missing/incorrect:\n%s", grep(body, "bmc_repository_last_check_timestamp"))
	}
	if !strings.Contains(body, "bmc_agent_grpc_reconnects_total 2") {
		t.Fatalf("reconnects counter missing/incorrect:\n%s", grep(body, "bmc_agent_grpc_reconnects_total"))
	}
}

func grep(body, prefix string) string {
	var out []string
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
