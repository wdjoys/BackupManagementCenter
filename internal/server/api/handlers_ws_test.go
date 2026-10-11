package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"backupmanagementcenter/internal/model"
	"backupmanagementcenter/internal/secrets"
	"backupmanagementcenter/internal/server/auth"
	"backupmanagementcenter/internal/server/events"
)

// 运行日志 WS 只写不读，但控制帧只在读循环里派发：没有读循环时 c.Ping 等不到
// pong，服务端会在 20s ticker + 5s 超时后关闭活跃订阅（>25s 的运行日志流每 25s
// 断连重连一次）。这里用客户端 ping 快速验证服务端在读：若服务端不读，pong 无人
// 应答，本用例在 3s 超时后失败。
func TestRunWSRespondsToPingWithoutDroppingSubscription(t *testing.T) {
	s, st, cleanup := newTestServerWithAdmin(t)
	defer cleanup()
	s.Bus = events.New()

	ctx := context.Background()
	token := "ws-test-token"
	if err := st.CreateSession(ctx, &model.Session{
		IDHash: secrets.HashToken(token), AdminID: "admin-1",
		ExpiresAt: time.Now().UTC().Add(time.Hour),
		CreatedAt: time.Now().UTC(), LastSeenAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	now := time.Now().UTC()
	if err := st.CreateRun(ctx, &model.Run{
		ID: "run-ws", AgentID: "agent-1", Operation: model.OpBackup, Status: model.RunRunning,
		QueuedAt: now, StartedAt: &now,
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	srv := httptest.NewServer(New(s))
	defer srv.Close()

	dctx, dcancel := context.WithTimeout(ctx, 15*time.Second)
	defer dcancel()
	hdr := http.Header{}
	hdr.Set("Cookie", auth.SessionCookie+"="+token)
	hdr.Set("Origin", srv.URL) // same-origin: OriginPatterns 用请求 Host 校验
	c, _, err := websocket.Dial(dctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/runs/run-ws", &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	// 客户端同样必须持续读，否则收不到服务端的 pong。Read 不能并发调用，
	// 因此首帧（回放的 state）也由这个唯一的读循环上报。
	gotState := make(chan string, 1)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			typ, raw, err := c.Read(dctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageText && strings.Contains(string(raw), `"type":"state"`) {
				select {
				case gotState <- string(raw):
				default:
				}
			}
		}
	}()

	// 收到回放的第一帧说明订阅已经建立。
	select {
	case <-gotState:
	case <-time.After(5 * time.Second):
		t.Fatal("no backlog state frame received")
	}

	pctx, pcancel := context.WithTimeout(ctx, 3*time.Second)
	defer pcancel()
	if err := c.Ping(pctx); err != nil {
		t.Fatalf("server did not answer ping (no read loop → subscription dropped every 25s): %v", err)
	}
}
