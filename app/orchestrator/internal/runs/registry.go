// Package runs 是 orchestrator 的 live 运行注册表(M7-2)。
//
// 设计纪律(见 docs 方案 M7 总体设计):
//   - 单写者:只有 orchestrator(本包调用方)迁移状态;pyagent 只上报事件。
//   - live 与 durable 分离:本包纯内存(TTL+上限),持久化归 TraceStore。
//   - 终态单调:RUNNING 之后进入任一终态即不可再迁,违规迁移打日志忽略(不 panic)。
//   - 单实例假设:注册表在单个 orchestrator 进程内;进程崩溃即清零,
//     崩溃孤儿由 durable 侧读时判定 ORPHANED(viewer),不在本包职责内。
package runs

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	orchestratorv1 "github.com/osije/ai-os/api/gen/go/orchestrator/v1"
)

const taskPreviewMaxRunes = 80

// Run 是一个 run 的内存快照(内部表示;对外转 orchestratorv1.RunInfo)。
type Run struct {
	TraceID     string
	TaskPreview string
	Route       string
	CurrentNode string
	State       orchestratorv1.RunState
	StartedAt   time.Time
	LastEventAt time.Time
	FinishedAt  time.Time // 零值 = 仍在运行

	// M7-3 取消支持(不进 RunInfo 序列化)。
	cancel          context.CancelFunc // 在飞执行的取消钩子;可为 nil
	cancelRequested bool               // 用户发起过 CancelRun(区分其他 Canceled 来源)
}

// Registry 是线程安全的 live 注册表。
type Registry struct {
	mu      sync.RWMutex
	runs    map[string]*Run
	ttl     time.Duration // 终态保留时长(供 live 面板短暂展示)
	maxSize int
	// onActiveChange 在 active(RUNNING)数量变化后被调用(持锁外),用于 metrics gauge;可为 nil。
	onActiveChange func(active int)
	now            func() time.Time // 可注入,测试用
}

// New 创建注册表。ttl<=0 用默认 5min,maxSize<=0 用默认 1000。
func New(ttl time.Duration, maxSize int, onActiveChange func(int)) *Registry {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if maxSize <= 0 {
		maxSize = 1000
	}
	return &Registry{
		runs:           make(map[string]*Run),
		ttl:            ttl,
		maxSize:        maxSize,
		onActiveChange: onActiveChange,
		now:            time.Now,
	}
}

func truncatePreview(task string) string {
	if utf8.RuneCountInString(task) <= taskPreviewMaxRunes {
		return task
	}
	r := []rune(task)
	return string(r[:taskPreviewMaxRunes]) + "…"
}

func isTerminal(s orchestratorv1.RunState) bool {
	switch s {
	case orchestratorv1.RunState_RUN_STATE_DONE,
		orchestratorv1.RunState_RUN_STATE_FAILED,
		orchestratorv1.RunState_RUN_STATE_DENIED,
		orchestratorv1.RunState_RUN_STATE_CANCELLED,
		orchestratorv1.RunState_RUN_STATE_ORPHANED,
		orchestratorv1.RunState_RUN_STATE_BUDGET_EXCEEDED:
		return true
	}
	return false
}

// Register 登记一个新 run(RUNNING)。cancel 是该 run 在飞执行的取消钩子(可为 nil,
// 如测试);重复登记打日志忽略。
func (r *Registry) Register(traceID, task string, cancel context.CancelFunc) {
	r.mu.Lock()
	if _, dup := r.runs[traceID]; dup {
		r.mu.Unlock()
		log.Printf("[runs] duplicate register ignored: %s", traceID)
		return
	}
	r.evictExpiredLocked()
	r.evictForCapacityLocked()
	now := r.now()
	r.runs[traceID] = &Run{
		TraceID:     traceID,
		TaskPreview: truncatePreview(task),
		State:       orchestratorv1.RunState_RUN_STATE_RUNNING,
		StartedAt:   now,
		LastEventAt: now,
		cancel:      cancel,
	}
	active := r.activeLocked()
	r.mu.Unlock()
	r.notify(active)
}

// SetRoute 填充路由(路由确定后调用;终态后调用则忽略)。
func (r *Registry) SetRoute(traceID, route string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if run, ok := r.runs[traceID]; ok && !isTerminal(run.State) {
		run.Route = route
		run.LastEventAt = r.now()
	}
}

// MarkNode 刷新当前节点(带内心跳:同时刷新 last_event)。
func (r *Registry) MarkNode(traceID, node string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if run, ok := r.runs[traceID]; ok && !isTerminal(run.State) {
		run.CurrentNode = node
		run.LastEventAt = r.now()
	}
}

// Touch 只刷新 last_event(token 事件等高频心跳)。
func (r *Registry) Touch(traceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if run, ok := r.runs[traceID]; ok && !isTerminal(run.State) {
		run.LastEventAt = r.now()
	}
}

// Finish 迁移到终态。只允许 RUNNING→终态;重复终态/未知 trace 打日志忽略。
// route 非空时一并填充(unary 场景路由在 done 才知道)。
func (r *Registry) Finish(traceID string, state orchestratorv1.RunState, route string) {
	if !isTerminal(state) {
		log.Printf("[runs] Finish with non-terminal state %v ignored: %s", state, traceID)
		return
	}
	r.mu.Lock()
	run, ok := r.runs[traceID]
	if !ok {
		r.mu.Unlock()
		log.Printf("[runs] Finish for unknown trace ignored: %s", traceID)
		return
	}
	if isTerminal(run.State) {
		// 静默幂等:调用方用 defer Finish(FAILED) 兜底提前退出,正常完成后该 defer
		// 必然触发一次"终态→终态",不是违规,不打日志。
		r.mu.Unlock()
		return
	}
	now := r.now()
	run.State = state
	run.FinishedAt = now
	run.LastEventAt = now
	if route != "" {
		run.Route = route
	}
	active := r.activeLocked()
	r.mu.Unlock()
	r.notify(active)
}

// List 返回快照(运行中在前、按开始时间倒序),顺带惰性驱逐过期终态(不做容量驱逐)。
func (r *Registry) List() []*orchestratorv1.RunInfo {
	r.mu.Lock()
	r.evictExpiredLocked()
	now := r.now()
	out := make([]*orchestratorv1.RunInfo, 0, len(r.runs))
	for _, run := range r.runs {
		elapsed := now.Sub(run.StartedAt)
		if isTerminal(run.State) {
			elapsed = run.FinishedAt.Sub(run.StartedAt)
		}
		out = append(out, &orchestratorv1.RunInfo{
			TraceId:       run.TraceID,
			TaskPreview:   run.TaskPreview,
			Route:         run.Route,
			State:         run.State,
			CurrentNode:   run.CurrentNode,
			StartedAtMs:   run.StartedAt.UnixMilli(),
			LastEventAtMs: run.LastEventAt.UnixMilli(),
			ElapsedMs:     elapsed.Milliseconds(),
		})
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		ri := out[i].State == orchestratorv1.RunState_RUN_STATE_RUNNING
		rj := out[j].State == orchestratorv1.RunState_RUN_STATE_RUNNING
		if ri != rj {
			return ri
		}
		return out[i].StartedAtMs > out[j].StartedAtMs
	})
	return out
}

// Cancel 请求取消一个 run(M7-3)。只发信号不迁移状态——CANCELLED 由持有 handler
// 的单写者在观察到取消后经 Finish 写入(单写者原则不破)。
// 返回:found=false 未知 trace;RUNNING → 标记+调 cancel 钩子,state 返回 CANCELLED(预期终态);
// 已终态 → 原样返回当前终态(幂等,无副作用)。
func (r *Registry) Cancel(traceID string) (bool, orchestratorv1.RunState) {
	r.mu.Lock()
	run, ok := r.runs[traceID]
	if !ok {
		r.mu.Unlock()
		return false, orchestratorv1.RunState_RUN_STATE_UNSPECIFIED
	}
	if isTerminal(run.State) {
		st := run.State
		r.mu.Unlock()
		return true, st
	}
	run.cancelRequested = true
	cancel := run.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel() // 持锁外调用,避免与 handler 的 Finish 抢锁
	}
	log.Printf("[runs] cancel requested: %s", traceID)
	return true, orchestratorv1.RunState_RUN_STATE_CANCELLED
}

// CancelRequested 返回该 run 是否被用户请求过取消(handler 用来区分
// "用户取消"与"下游断开/超时"等其他 Canceled 来源)。
func (r *Registry) CancelRequested(traceID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	run, ok := r.runs[traceID]
	return ok && run.cancelRequested
}

// ActiveCount 返回 RUNNING 数量。
func (r *Registry) ActiveCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.activeLocked()
}

// --- internals (调用方需持锁) ---

func (r *Registry) activeLocked() int {
	n := 0
	for _, run := range r.runs {
		if !isTerminal(run.State) {
			n++
		}
	}
	return n
}

// evictExpiredLocked 驱逐超过 TTL 的终态 run。
func (r *Registry) evictExpiredLocked() {
	now := r.now()
	for id, run := range r.runs {
		if isTerminal(run.State) && now.Sub(run.FinishedAt) > r.ttl {
			delete(r.runs, id)
		}
	}
}

// evictForCapacityLocked 为即将插入的新 run 腾位(仅 Register 调用):
// 优先驱逐最老终态;没有终态(全 RUNNING)才驱逐最老 run 并告警。
func (r *Registry) evictForCapacityLocked() {
	for len(r.runs) >= r.maxSize {
		pick := func(terminalOnly bool) string {
			id, oldest := "", time.Time{}
			for k, run := range r.runs {
				if terminalOnly && !isTerminal(run.State) {
					continue
				}
				if id == "" || run.StartedAt.Before(oldest) {
					id, oldest = k, run.StartedAt
				}
			}
			return id
		}
		victim := pick(true)
		if victim == "" {
			victim = pick(false)
			if victim == "" {
				return
			}
			log.Printf("[runs] registry over capacity, evicting RUNNING run %s", victim)
		}
		delete(r.runs, victim)
	}
}

func (r *Registry) notify(active int) {
	if r.onActiveChange != nil {
		r.onActiveChange(active)
	}
}
