package api

import (
	"net/url"
	"testing"
)

// repository_id 曾经未映射而被静默忽略：按仓库过滤时拿到的是全局运行列表，排障时
// 容易据此得出错误结论（实测某切片据此误判"该仓库无保留 run"）。
func TestModel2FilterMapsRepositoryID(t *testing.T) {
	q := url.Values{}
	q.Set("repository_id", "repo-1")
	q.Set("agent_id", "agent-1")
	q.Set("plan_id", "plan-1")
	q.Set("operation", "forget")
	q.Set("status", "succeeded")
	q.Set("limit", "7")

	f := model2Filter(q)
	if f.RepositoryID != "repo-1" {
		t.Fatalf("repository_id 必须被映射，得到 %q", f.RepositoryID)
	}
	if f.AgentID != "agent-1" || f.PlanID != "plan-1" || f.Operation != "forget" {
		t.Fatalf("其它过滤字段不应受影响: %+v", f)
	}
	if len(f.Statuses) != 1 || f.Statuses[0] != "succeeded" || f.Limit != 7 {
		t.Fatalf("status/limit 不应受影响: %+v", f)
	}
}
