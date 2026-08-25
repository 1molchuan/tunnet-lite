// Package engine owns the running xray-core instance so a caller can swap the
// exit or the entry group without restarting the process.
package engine

import (
	"bytes"
	"fmt"
	"sync"
	"time"

	"github.com/xtls/xray-core/core"
	_ "github.com/xtls/xray-core/main/distro/all"
)

// Plan is the human-readable summary of what a configuration will do. It never
// carries credentials, so it is safe to expose over the local API.
type Plan struct {
	HostSlug    string   `json:"host_slug"`
	HostName    string   `json:"host_name"`
	LogicalHost string   `json:"logical_host"`
	RootDomain  string   `json:"root_domain"`
	GroupName   string   `json:"group_name"`
	FrontProxy  string   `json:"front_proxy,omitempty"`
	Entries     []string `json:"entries"`
	UDP         bool     `json:"udp"`
	RouteMode   string   `json:"route_mode"`
	Listen      string   `json:"listen"`
	Port        int      `json:"port"`
}

// Status is the engine's view of itself.
type Status struct {
	Running   bool      `json:"running"`
	Plan      *Plan     `json:"plan,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
	LastError string    `json:"last_error,omitempty"`
}

type Engine struct {
	mu        sync.Mutex
	inst      *core.Instance
	plan      *Plan
	startedAt time.Time
	lastErr   string
}

func New() *Engine { return &Engine{} }

// Apply replaces whatever is running with the given configuration.
//
// The listener has to be released before the replacement can bind it, so the
// old instance is stopped first and there is a brief gap during which the proxy
// refuses connections. If the new configuration fails to start, the engine is
// left stopped rather than silently reverted, and the error is recorded.
func (e *Engine) Apply(plan Plan, configJSON []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.stopLocked()

	cfg, err := core.LoadConfig("json", bytes.NewReader(configJSON))
	if err != nil {
		return e.failLocked(fmt.Errorf("load config: %w", err))
	}
	inst, err := core.New(cfg)
	if err != nil {
		return e.failLocked(fmt.Errorf("create instance: %w", err))
	}
	if err := inst.Start(); err != nil {
		inst.Close()
		return e.failLocked(fmt.Errorf("start instance: %w", err))
	}

	e.inst = inst
	e.plan = &plan
	e.startedAt = time.Now()
	e.lastErr = ""
	return nil
}

func (e *Engine) Stop() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stopLocked()
}

func (e *Engine) stopLocked() error {
	if e.inst == nil {
		return nil
	}
	err := e.inst.Close()
	e.inst = nil
	e.plan = nil
	e.startedAt = time.Time{}
	return err
}

func (e *Engine) failLocked(err error) error {
	e.lastErr = err.Error()
	return err
}

func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return Status{
		Running:   e.inst != nil,
		Plan:      e.plan,
		StartedAt: e.startedAt,
		LastError: e.lastErr,
	}
}
