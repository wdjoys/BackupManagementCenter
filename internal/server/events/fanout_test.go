package events

import (
	"sync"
	"testing"
	"time"
)

// 慢订阅者必须被丢弃而不是阻塞生产者：缓冲区满后通道关闭，其余事件照常发布。
func TestSlowSubscriberIsDroppedWithoutBlockingPublisher(t *testing.T) {
	bus := New()
	ch, cancel := bus.Subscribe("run-1")
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 300 {
			bus.Publish("run-1", Event{Type: Log})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a non-reading subscriber")
	}

	n := 0
	for range ch {
		n++
	}
	if n != 256 {
		t.Fatalf("dropped subscriber received %d events, want 256 (buffer size) before close", n)
	}
	// 丢弃必须有可观测信号：没有计数就只能从"界面不再更新"反推慢消费者。
	if got := DroppedSubscribers(); got < 1 {
		t.Fatalf("DroppedSubscribers() = %d, want >= 1 after dropping a slow subscriber", got)
	}
}

// 订阅者取消（WS 断开时的 defer cancel）与生产者并发时，Publish 绝不能向已关闭的
// 通道发送——那会 panic: send on closed channel，直接打挂整个 server 进程。
func TestPublishConcurrentWithCancelDoesNotPanic(t *testing.T) {
	for range 5000 {
		bus := New()
		_, cancel := bus.Subscribe("run-1")
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 5 {
				bus.Publish("run-1", Event{Type: Log})
			}
		}()
		go func() {
			defer wg.Done()
			cancel()
		}()
		wg.Wait()
	}
}
