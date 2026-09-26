package webview

import (
	"sync"
	"testing"
)

// 队列元素 JSON 快速构造：pushItem.json 无格式要求，测试只关心标记与顺序。
func liveJSON(n int) string { return `{"live":` + string(rune('0'+n%10)) + `}` }

// TestPushQueueDropsOldestLive 队列满时丢最旧的 live：新 live 入队、旧 live 出队。
func TestPushQueueDropsOldestLive(t *testing.T) {
	w := &Window{}
	for i := 0; i < pushQueueCap; i++ {
		w.Push(liveJSON(i))
	}
	if len(w.pushQ) != pushQueueCap {
		t.Fatalf("len = %d; want %d", len(w.pushQ), pushQueueCap)
	}
	w.Push(liveJSON(100))
	if len(w.pushQ) != pushQueueCap {
		t.Fatalf("after push len = %d; want %d", len(w.pushQ), pushQueueCap)
	}
	// 被挤出的是最旧的 live（0），最后入队的是 100。
	if w.pushQ[0].json != liveJSON(1) {
		t.Errorf("head = %s; want live 1（最旧的 live 0 应被挤出）", w.pushQ[0].json)
	}
	if w.pushQ[len(w.pushQ)-1].json != liveJSON(100) {
		t.Errorf("tail = %s; want live 100", w.pushQ[len(w.pushQ)-1].json)
	}
}

// TestPushReplyNeverDropped 队列满时 PushReply 的 RPC 回复必须保留，仅牺牲 live。
// RPC 回复数量极少（前端 RPC 一次一两条），允许队列临时超出 cap——宁可多缓冲
// 也绝不丢回复。
func TestPushReplyNeverDropped(t *testing.T) {
	w := &Window{}
	for i := 0; i < pushQueueCap; i++ {
		w.Push(liveJSON(i))
	}
	w.PushReply(`{"id":1,"ok":true}`)
	if len(w.pushQ) != pushQueueCap+1 {
		t.Fatalf("len = %d; want %d（RPC 无条件入队，可临时超过 cap）", len(w.pushQ), pushQueueCap+1)
	}
	// RPC 回复保留且位于队尾。
	last := w.pushQ[len(w.pushQ)-1]
	if last.isLive {
		t.Fatal("RPC 回复被标记为 live")
	}
	if last.json != `{"id":1,"ok":true}` {
		t.Errorf("tail = %s; want RPC 回复", last.json)
	}
	// 头部仍为 live。
	if w.pushQ[0].isLive != true {
		t.Fatalf("head 应为 live，got isLive=%v", w.pushQ[0].isLive)
	}
	// 队列内 live 之外只有那一条 RPC 回复。
	var liveCount, rpcCount int
	for _, it := range w.pushQ {
		if it.isLive {
			liveCount++
		} else {
			rpcCount++
		}
	}
	if liveCount != pushQueueCap || rpcCount != 1 {
		t.Fatalf("live=%d rpc=%d; want live=%d rpc=1", liveCount, rpcCount, pushQueueCap)
	}
	// 队列满时再推 live：仍只丢最旧 live，RPC 回复不受影响。
	w.Push(liveJSON(101))
	if len(w.pushQ) != pushQueueCap+1 {
		t.Fatalf("after live push len = %d; want %d", len(w.pushQ), pushQueueCap+1)
	}
	rpcCount = 0
	for _, it := range w.pushQ {
		if !it.isLive {
			rpcCount++
			if it.json != `{"id":1,"ok":true}` {
				t.Errorf("RPC 回复内容被篡改: %s", it.json)
			}
		}
	}
	if rpcCount != 1 {
		t.Fatalf("RPC 回复被挤出: rpc=%d; want 1", rpcCount)
	}
}

// TestPushLiveWhenQueueAllRPC 队列全为 RPC 回复时 Push live 不挤出任何回复，
// 新 live 自身被丢弃（live 由下一条心跳重推，不影响正确性）。
func TestPushLiveWhenQueueAllRPC(t *testing.T) {
	w := &Window{}
	for i := 0; i < pushQueueCap; i++ {
		w.PushReply(liveJSON(i))
	}
	if len(w.pushQ) != pushQueueCap {
		t.Fatalf("len = %d; want %d", len(w.pushQ), pushQueueCap)
	}
	w.Push(liveJSON(100))
	if len(w.pushQ) != pushQueueCap {
		t.Fatalf("after live push len = %d; want %d（不得挤出 RPC 回复）", len(w.pushQ), pushQueueCap)
	}
	for i, it := range w.pushQ {
		if it.isLive {
			t.Fatalf("item[%d] 应为 RPC 回复，got live %s", i, it.json)
		}
	}
}

// TestPushHwndConcurrentWrite 竞态回归（R2-新1）：Push/PushReply 跨线程读 w.hwnd
// 必须与 w.mu 持有者的写入同步——写方在同一 Window 上并发写 hwnd（模拟 Open 中
// w.hwnd = hwnd 与 cleanup 读的场景），若 pushQPush 仍无锁读 w.hwnd，race detector 报错。
func TestPushHwndConcurrentWrite(t *testing.T) {
	w := New()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			w.mu.Lock()
			w.hwnd = 0x1234
			w.mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			w.Push(liveJSON(i))
			w.PushReply(`{"id":1,"ok":true}`)
		}
	}()
	wg.Wait()
}
