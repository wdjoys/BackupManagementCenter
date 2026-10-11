package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/server/store"
)

const planKinds = "filesystem|postgresql|mysql|mongodb|sqlite"

type planView struct {
	ID             string              `json:"id"`
	Name           string              `json:"name"`
	AgentID        string              `json:"agent_id"`
	Kind           string              `json:"kind"`
	Schedule       string              `json:"schedule"`
	Timezone       string              `json:"timezone"`
	Enabled        bool                `json:"enabled"`
	Source         json.RawMessage     `json:"source"`
	Credentials    planCredentialsView `json:"credentials"`
	RepositoryID   string              `json:"repository_id"`
	Retention      model.Retention     `json:"retention"`
	TimeoutSeconds int                 `json:"timeout_seconds"`
	LastRunAt      *string             `json:"last_run_at"`
	CreatedAt      string              `json:"created_at"`
	UpdatedAt      string              `json:"updated_at"`
}

type planCredentialsView struct {
	PasswordSet bool `json:"password_set"`
}

func planToView(p *model.Plan, passwordSet bool) planView {
	var src model.PlanSource
	if err := json.Unmarshal([]byte(p.SourceJSON), &src); err != nil {
		src = p.Source
	}
	clean, _ := json.Marshal(src)
	return planView{
		ID: p.ID, Name: p.Name, AgentID: p.AgentID, Kind: p.Kind,
		Schedule: p.Schedule, Timezone: p.Timezone, Enabled: p.Enabled,
		Source: json.RawMessage(clean), Credentials: planCredentialsView{PasswordSet: passwordSet}, RepositoryID: p.RepositoryID,
		Retention: p.Retention, TimeoutSeconds: p.TimeoutSeconds,
		CreatedAt: p.CreatedAt.Format(timeRFC3339), UpdatedAt: p.UpdatedAt.Format(timeRFC3339),
	}
}

func (s *Server) planView(ctx context.Context, p *model.Plan) planView {
	passwordSet := false
	if ps, ok := s.ST.(interface {
		HasPlanDBPassword(context.Context, string) bool
	}); ok {
		passwordSet = ps.HasPlanDBPassword(ctx, p.ID)
	}
	view := planToView(p, passwordSet)
	runs, err := s.ST.ListRuns(ctx, store.RunFilter{PlanID: p.ID, Operation: model.OpBackup, Limit: 1})
	if err == nil && len(runs) > 0 {
		run := runs[0]
		at := run.FinishedAt
		if at == nil {
			at = run.StartedAt
		}
		if at != nil {
			formatted := at.Format(timeRFC3339)
			view.LastRunAt = &formatted
		}
	}
	return view
}

type planSourceWire struct {
	model.PlanSource
	Password    string `json:"password,omitempty"`
	Credentials *struct {
		Password string `json:"password,omitempty"`
	} `json:"credentials,omitempty"`
}

func decodePlanSource(raw json.RawMessage) (model.PlanSource, string, error) {
	var wire planSourceWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return model.PlanSource{}, "", err
	}
	password := wire.Password
	if password == "" && wire.Credentials != nil {
		password = wire.Credentials.Password
	}
	return wire.PlanSource, password, nil
}

// GET /plans?agent_id=
func (s *Server) handleListPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := s.ST.ListPlans(r.Context(), r.URL.Query().Get("agent_id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	out := make([]planView, 0, len(plans))
	for i := range plans {
		out = append(out, s.planView(r.Context(), &plans[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /plans/{id}
func (s *Server) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	p, err := s.ST.GetPlan(r.Context(), pathParam(r, "id"))
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.planView(r.Context(), p))
}

type planBody struct {
	Name        string          `json:"name"`
	AgentID     string          `json:"agent_id"`
	Kind        string          `json:"kind"`
	Schedule    string          `json:"schedule"`
	Timezone    string          `json:"timezone"`
	Enabled     *bool           `json:"enabled"`
	Source      json.RawMessage `json:"source"`
	Credentials *struct {
		Password string `json:"password,omitempty"`
	} `json:"credentials,omitempty"`
	// Password 只用于拒绝误用：数据库口令只从 source.password 或
	// credentials.password 读取，顶层 password 会被静默丢弃，导致"计划建好了
	// 但口令为空"、备份报 Access denied (using password: NO)。
	Password *string `json:"password,omitempty"`
	// ExtraArgs 同样只用于拒绝误用：extra_args 属于 source，放顶层会被静默丢弃。
	ExtraArgs      *[]string       `json:"extra_args,omitempty"`
	RepositoryID   string          `json:"repository_id"`
	Retention      model.Retention `json:"retention"`
	TimeoutSeconds int             `json:"timeout_seconds"`
}

func (b *planBody) validate() (string, bool) {
	// 顶层 password 不参与读取，必须在 switch 之前拒绝（switch 只执行首个
	// 匹配分支，放在里面会被其它分支跳过）。
	if b.Password != nil {
		return "password must be nested: use credentials.password (or source.password)", false
	}
	if b.ExtraArgs != nil {
		return "extra_args must be nested inside source (source.extra_args)", false
	}
	// 默认值必须在 switch 之前应用：switch 只执行首个匹配分支，而 timezone 为空
	// 时首个匹配的正是 timezone 分支，timeout_seconds 的默认值（3600）会被跳过，
	// 导致「两个字段都没填」的合法请求被误报为 timeout_seconds must be 60..21600。
	if b.Timezone == "" {
		b.Timezone = "UTC"
	}
	if b.TimeoutSeconds == 0 {
		b.TimeoutSeconds = 3600
	}
	switch {
	case len(b.Name) < 1 || len(b.Name) > 200:
		return "name must be 1..200 chars", false
	case !validKind(b.Kind):
		return "kind must be one of " + planKinds, false
	case b.AgentID == "" || b.RepositoryID == "":
		return "agent_id and repository_id are required", false
	case b.Schedule == "":
		return "schedule (5-field cron) is required", false
	}
	if b.TimeoutSeconds < 60 || b.TimeoutSeconds > 21600 {
		return "timeout_seconds must be 60..21600 (six-hour window)", false
	}
	if _, err := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(b.Schedule); err != nil {
		return "schedule is not a valid 5-field cron expression", false
	}
	if _, err := time.LoadLocation(b.Timezone); err != nil {
		return "timezone must be a valid IANA timezone", false
	}
	if err := validateScheduleWindow(b.Schedule, b.Timezone, time.Duration(b.TimeoutSeconds)*time.Second); err != nil {
		return err.Error(), false
	}
	if b.Retention.KeepLast+b.Retention.KeepDaily+b.Retention.KeepWeekly+b.Retention.KeepMonthly == 0 {
		return "retention must keep at least one rule", false
	}
	return "", true
}

func validateScheduleWindow(expr, timezone string, timeout time.Duration) error {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(expr)
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return err
	}
	prev := sched.Next(time.Now().In(loc))
	for i := 0; i < 4; i++ {
		next := sched.Next(prev)
		if next.Sub(prev) < timeout {
			return fmt.Errorf("schedule interval (%s) is shorter than timeout_seconds (%s); backup windows would overlap", next.Sub(prev), timeout)
		}
		prev = next
	}
	return nil
}

func validKind(k string) bool {
	switch k {
	case model.KindFilesystem, model.KindPostgreSQL, model.KindMySQL, model.KindMongoDB, model.KindSQLite:
		return true
	}
	return false
}

func validateDatabaseEstimate(kind string, src model.PlanSource) string {
	switch kind {
	case model.KindPostgreSQL, model.KindMySQL, model.KindMongoDB:
		if src.EstimatedDumpBytes <= 0 {
			return "estimated_dump_bytes must be greater than zero for logical database backups"
		}
	case model.KindSQLite:
		if src.Path == "" || !filepath.IsAbs(src.Path) {
			return "sqlite source path must be absolute"
		}
		// SQLite size is read from the source file by the agent; a user-supplied
		// estimate is still accepted for scratch sizing when present.
	}
	if kind == model.KindMongoDB && src.CaptureOplog {
		// capture_oplog 在本平台内不可能端到端可用，因此在计划创建阶段直接拒绝，
		// 绝不允许产出"能备份、永远不能恢复"的快照：
		//   1. mongodump 的 --oplog 只支持整实例 dump：带 --db 时工具直接拒绝
		//      （bad option: --oplog mode only supported on full dumps），
		//      所以"单库 + 捕获 oplog"这个组合在工具层就不存在；
		//   2. 唯一能捕获 oplog 的整实例（database=all）快照在恢复侧被一律拒绝
		//      （平台只开放单库恢复），而 mongorestore 的 --oplogReplay 明确禁止与
		//      --nsFrom/--nsTo 组合（cannot use --oplogReplay with namespace renames
		//      specified），无法重映射成单库恢复。
		// 详见 deploy/README.md「MongoDB 的 capture_oplog」。
		return "capture_oplog is not supported: mongodump --oplog only works on full instance dumps (database=all), and full instance snapshots cannot be restored by this platform, so such a snapshot could never be restored"
	}
	if src.EstimatedDumpBytes > 100<<30 {
		return "logical backup exceeds 100 GiB; physical_backup_required"
	}
	if err := model.ValidateExtraArgs(kind, src.ExtraArgs); err != nil {
		return err.Error()
	}
	return ""
}

// POST /plans
func (s *Server) handleCreatePlan(w http.ResponseWriter, r *http.Request) {
	var body planBody
	if !readJSON(w, r, &body) {
		return
	}
	if msg, ok := body.validate(); !ok {
		writeErr(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	var src model.PlanSource
	src, password, err := decodePlanSource(body.Source)
	if password == "" && body.Credentials != nil {
		password = body.Credentials.Password
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, "validation_failed", "invalid source: "+err.Error())
		return
	}
	cleanSource, _ := json.Marshal(src)
	if msg := validateDatabaseEstimate(body.Kind, src); msg != "" {
		code := model.ErrInvalidPlan
		if src.EstimatedDumpBytes > 100<<30 {
			code = model.ErrPhysicalBackupRequired
		}
		writeErr(w, http.StatusUnprocessableEntity, code, msg)
		return
	}
	if err := s.Jobs.ValidatePlanSource(r.Context(), body.Kind, src, body.AgentID); err != nil {
		s.jobsErr(w, err)
		return
	}
	repo, err := s.ST.GetRepository(r.Context(), body.RepositoryID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	if repo.AgentID != body.AgentID {
		writeErr(w, http.StatusBadRequest, "validation_failed", "repository does not belong to this agent")
		return
	}
	now := time.Now().UTC()
	retentionJSON, err := json.Marshal(body.Retention)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "failed to encode retention")
		return
	}
	p := &model.Plan{
		ID: newUUID(), Name: body.Name, AgentID: body.AgentID, Kind: body.Kind,
		Schedule: body.Schedule, Timezone: body.Timezone,
		Enabled:    body.Enabled == nil || *body.Enabled,
		SourceJSON: string(cleanSource), RepositoryID: body.RepositoryID,
		Retention: body.Retention, RetentionJSON: string(retentionJSON), TimeoutSeconds: body.TimeoutSeconds,
		CreatedAt: now, UpdatedAt: now,
	}

	if err := s.ST.CreatePlan(r.Context(), p); err != nil {
		if !mapStoreErr(w, err) {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}
	if password != "" {
		if ps, ok := s.ST.(interface {
			SavePlanDBPassword(context.Context, string, string) error
		}); ok {
			if err := ps.SavePlanDBPassword(r.Context(), p.ID, password); err != nil {
				_ = s.ST.DeletePlan(r.Context(), p.ID)
				writeErr(w, http.StatusInternalServerError, "internal", "failed to save database credential")
				return
			}
		}
	}
	s.Jobs.Audit(r.Context(), "admin", actorID(r), "plan.create", "plan", p.ID, marshalDetail(map[string]string{"name": p.Name, "kind": p.Kind}))
	writeJSON(w, http.StatusCreated, s.planView(r.Context(), p))
}

// PUT /plans/{id}
func (s *Server) handleUpdatePlan(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	existing, err := s.ST.GetPlan(r.Context(), id)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	var body planBody
	if !readJSON(w, r, &body) {
		return
	}
	if msg, ok := body.validate(); !ok {
		writeErr(w, http.StatusBadRequest, "validation_failed", msg)
		return
	}
	src, password, err := decodePlanSource(body.Source)
	if password == "" && body.Credentials != nil {
		password = body.Credentials.Password
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, "validation_failed", "invalid source: "+err.Error())
		return
	}
	if err := s.Jobs.ValidatePlanSource(r.Context(), body.Kind, src, body.AgentID); err != nil {
		s.jobsErr(w, err)
		return
	}
	existing.Name = body.Name
	existing.AgentID = body.AgentID
	existing.Kind = body.Kind
	existing.Schedule = body.Schedule
	existing.Timezone = body.Timezone
	if body.Enabled != nil {
		existing.Enabled = *body.Enabled
	}
	cleanSource, _ := json.Marshal(src)
	if msg := validateDatabaseEstimate(body.Kind, src); msg != "" {
		code := model.ErrInvalidPlan
		if src.EstimatedDumpBytes > 100<<30 {
			code = model.ErrPhysicalBackupRequired
		}
		writeErr(w, http.StatusUnprocessableEntity, code, msg)
		return
	}
	existing.SourceJSON = string(cleanSource)
	existing.RepositoryID = body.RepositoryID
	retentionJSON, err := json.Marshal(body.Retention)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "failed to encode retention")
		return
	}
	existing.Retention = body.Retention
	existing.RetentionJSON = string(retentionJSON)
	existing.TimeoutSeconds = body.TimeoutSeconds
	repo, err := s.ST.GetRepository(r.Context(), body.RepositoryID)
	if err != nil {
		mapStoreErr(w, err)
		return
	}
	if repo.AgentID != body.AgentID {
		writeErr(w, http.StatusBadRequest, "validation_failed", "repository does not belong to this agent")
		return
	}
	existing.UpdatedAt = time.Now().UTC()
	if err := s.ST.UpdatePlan(r.Context(), existing); err != nil {
		if !mapStoreErr(w, err) {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}
	if password != "" {
		if ps, ok := s.ST.(interface {
			SavePlanDBPassword(context.Context, string, string) error
		}); ok {
			if err := ps.SavePlanDBPassword(r.Context(), existing.ID, password); err != nil {
				writeErr(w, http.StatusInternalServerError, "internal", "failed to save database credential")
				return
			}
		}
	}
	s.Jobs.Audit(r.Context(), "admin", actorID(r), "plan.update", "plan", id, nil)
	writeJSON(w, http.StatusOK, s.planView(r.Context(), existing))
}

// DELETE /plans/{id} — 有活跃运行或远端快照时拒绝删除。
func (s *Server) handleDeletePlan(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	plan, err := s.ST.GetPlan(r.Context(), id)
	if err != nil {
		if !mapStoreErr(w, err) {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}

	// 仓库不可达（error/离线/远端丢失）时快照列表会直接失败。此时必须仍然允许删除计划：
	// 否则 error 仓库会锁死「计划→仓库→存储目标」三级删除（计划删不掉 → 仓库仍被引用 →
	// 目标删不掉）。清理失败只记日志，不阻塞删除。
	snapshots, _, _, err := s.Jobs.SnapshotsWithOptions(r.Context(), plan.RepositoryID, plan.AgentID, true)
	if err != nil {
		logf("[WARN] plan delete: snapshot cleanup skipped plan=%s repository=%s: %v", id, plan.RepositoryID, err)
	} else {
		for _, snapshot := range snapshots {
			for _, tag := range snapshot.Tags {
				if tag == "plan:"+id {
					writeErr(w, http.StatusConflict, "plan_has_snapshots", "plan still has snapshots")
					return
				}
			}
		}
	}

	if err := s.ST.DeletePlan(r.Context(), id); err != nil {
		if !mapStoreErr(w, err) {
			writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		}
		return
	}
	s.Jobs.Audit(r.Context(), "admin", actorID(r), "plan.delete", "plan", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// POST /plans/{id}/backups/delete — 删除计划标签下的全部历史备份。
func (s *Server) handleDeletePlanBackups(w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if err := s.Jobs.DeletePlanBackups(r.Context(), id); err != nil {
		if !mapStoreErr(w, err) {
			s.jobsErr(w, err)
		}
		return
	}
	s.Jobs.Audit(r.Context(), "admin", actorID(r), "plan.backups.delete", "plan", id, nil)
	w.WriteHeader(http.StatusNoContent)
}

// POST /plans/validate {kind, source, agent_id}
func (s *Server) handleValidatePlan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind    string          `json:"kind"`
		Source  json.RawMessage `json:"source"`
		AgentID string          `json:"agent_id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	var src model.PlanSource
	if err := json.Unmarshal(body.Source, &src); err != nil {
		writeErr(w, http.StatusBadRequest, "validation_failed", "invalid source: "+err.Error())
		return
	}
	if msg := validateDatabaseEstimate(body.Kind, src); msg != "" {
		code := model.ErrInvalidPlan
		if src.EstimatedDumpBytes > 100<<30 {
			code = model.ErrPhysicalBackupRequired
		}
		writeErr(w, http.StatusUnprocessableEntity, code, msg)
		return
	}
	if err := s.Jobs.ValidatePlanSource(r.Context(), body.Kind, src, body.AgentID); err != nil {
		s.jobsErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// POST /plans/{id}/run — manual run.
func (s *Server) handleRunPlan(w http.ResponseWriter, r *http.Request) {
	run, err := s.Jobs.ManualRun(r.Context(), pathParam(r, "id"))
	if err != nil {
		s.jobsErr(w, err)
		return
	}
	s.Met.SetQueueDepth(queueDepthHint(r))
	writeJSON(w, http.StatusAccepted, runView(run))
}

func queueDepthHint(_ *http.Request) int { return 0 } // real gauge set by dispatcher

var _ = store.ErrNotFound
var _ = strings.Contains
