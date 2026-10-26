package donations

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Sink — приёмник нормализованных событий провайдера.
type Sink func(Event)

// ProviderStatus — состояние подключения провайдера для панели.
type ProviderStatus struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}

// Provider — источник донат-событий (websocket, polling или вебхук).
type Provider interface {
	Name() string
	Start(ctx context.Context, sink Sink) error
	Stop() error
	Status() ProviderStatus
}

// Service — реестр провайдеров и общая шина событий.
type Service struct {
	bus       *Bus
	mu        sync.RWMutex
	providers map[string]Provider
	goals     map[string]Goal
	ctx       context.Context
	enabled   bool
}

func NewService(bus *Bus) *Service {
	return &Service{
		bus:       bus,
		providers: make(map[string]Provider),
		goals:     make(map[string]Goal),
		enabled:   true,
	}
}

func (s *Service) Bus() *Bus {
	return s.bus
}

func (s *Service) PublishGoal(goal Goal) {
	if goal.ID == "" {
		return
	}

	if goal.UpdatedAt.IsZero() {
		goal.UpdatedAt = time.Now().UTC()
	}

	s.mu.Lock()
	s.goals[goal.Provider+":"+goal.ID] = goal
	s.mu.Unlock()
}

func (s *Service) Goals() []Goal {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Goal, 0, len(s.goals))

	for _, goal := range s.goals {
		if goal.Active {
			result = append(result, goal)
		}
	}

	return result
}

func (s *Service) Register(provider Provider) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.providers[provider.Name()] = provider
}

// ID/Name/Kind/Enabled/SetEnabled/Status — контракт integrations.Component.
func (s *Service) ID() string {
	return "donations"
}

func (s *Service) Name() string {
	return "Донаты и алерты"
}

func (s *Service) Kind() string {
	return "donations"
}

func (s *Service) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.enabled
}

func (s *Service) SetEnabled(enabled bool) error {
	s.mu.Lock()
	s.enabled = enabled
	ctx := s.ctx
	s.mu.Unlock()

	if enabled {
		if ctx != nil {
			s.StartAll(ctx)
		}

		return nil
	}

	s.StopAll()

	return nil
}

func (s *Service) Status() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return fmt.Sprintf("провайдеров: %d, событий: %d", len(s.providers), len(s.bus.Recent(0)))
}

func (s *Service) StartAll(ctx context.Context) {
	s.mu.Lock()
	s.ctx = ctx
	enabled := s.enabled
	s.mu.Unlock()

	if !enabled {
		return
	}

	s.mu.RLock()
	providers := make([]Provider, 0, len(s.providers))

	for _, provider := range s.providers {
		providers = append(providers, provider)
	}

	s.mu.RUnlock()

	for _, provider := range providers {
		if webhook, ok := provider.(*WebhookProvider); ok {
			webhook.SetEnabled(true)
		}

		if err := provider.Start(ctx, s.bus.Publish); err != nil {
			// Провайдер остаётся в статусе error; панель покажет состояние.
			_ = err
		}
	}
}

func (s *Service) StopAll() {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, provider := range s.providers {
		if webhook, ok := provider.(*WebhookProvider); ok {
			webhook.SetEnabled(false)
		}

		_ = provider.Stop()
	}
}

func (s *Service) Statuses() []ProviderStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	statuses := make([]ProviderStatus, 0, len(s.providers))

	for _, provider := range s.providers {
		statuses = append(statuses, provider.Status())
	}

	return statuses
}
