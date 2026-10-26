package donations

import (
	"sort"
	"sync"
	"time"
)

// Event — нормализованное донат-событие любого провайдера.
type Event struct {
	ID        string    `json:"id"`
	Provider  string    `json:"provider"`
	Type      string    `json:"type"`
	Username  string    `json:"username"`
	Message   string    `json:"message"`
	Amount    float64   `json:"amount"`
	Currency  string    `json:"currency"`
	CreatedAt time.Time `json:"createdAt"`
}

type TopDonator struct {
	Username string  `json:"username"`
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
	Count    int     `json:"count"`
}

type Stats struct {
	TotalCount int                `json:"totalCount"`
	Totals     map[string]float64 `json:"totals"`
	Today      map[string]float64 `json:"today"`
	Top        []TopDonator       `json:"top"`
}

// Bus — поток донатов: ring-buffer последних событий, дедупликация, подписчики.
type Bus struct {
	mu     sync.Mutex
	recent []Event
	max    int
	seen   map[string]struct{}
	subs   map[int]chan Event
	nextID int
}

func NewBus(max int) *Bus {
	if max <= 0 {
		max = 100
	}

	return &Bus{
		recent: make([]Event, 0, max),
		max:    max,
		seen:   make(map[string]struct{}),
		subs:   make(map[int]chan Event),
	}
}

func dedupeKey(event Event) string {
	return event.Provider + ":" + event.ID
}

func (b *Bus) Publish(event Event) {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}

	b.mu.Lock()

	if event.ID != "" {
		key := dedupeKey(event)

		if _, ok := b.seen[key]; ok {
			b.mu.Unlock()

			return
		}

		b.seen[key] = struct{}{}
	}

	b.recent = append(b.recent, event)

	if len(b.recent) > b.max {
		drop := len(b.recent) - b.max

		for _, old := range b.recent[:drop] {
			if old.ID != "" {
				delete(b.seen, dedupeKey(old))
			}
		}

		b.recent = append(b.recent[:0], b.recent[drop:]...)
	}

	channels := make([]chan Event, 0, len(b.subs))

	for _, ch := range b.subs {
		channels = append(channels, ch)
	}

	b.mu.Unlock()

	for _, ch := range channels {
		select {
		case ch <- event:
		default:
		}
	}
}

func (b *Bus) Subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextID
	b.nextID++

	ch := make(chan Event, 32)
	b.subs[id] = ch

	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()

		if existing, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(existing)
		}
	}
}

// Recent возвращает последние limit событий в обратном хронологическом порядке.
func (b *Bus) Recent(limit int) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()

	if limit <= 0 || limit > len(b.recent) {
		limit = len(b.recent)
	}

	result := make([]Event, 0, limit)

	for i := len(b.recent) - 1; i >= 0 && len(result) < limit; i-- {
		result = append(result, b.recent[i])
	}

	return result
}

func (b *Bus) Stats() Stats {
	b.mu.Lock()
	events := append([]Event(nil), b.recent...)
	b.mu.Unlock()

	stats := Stats{
		TotalCount: len(events),
		Totals:     make(map[string]float64),
		Today:      make(map[string]float64),
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)

	type accumulator struct {
		amount   float64
		currency string
		count    int
	}

	donators := make(map[string]*accumulator)

	for _, event := range events {
		currency := event.Currency
		if currency == "" {
			currency = "RUB"
		}

		stats.Totals[currency] += event.Amount

		if event.Amount <= 0 {
			continue
		}

		if event.CreatedAt.UTC().After(today) {
			stats.Today[currency] += event.Amount
		}

		key := event.Username + "|" + currency
		entry, ok := donators[key]

		if !ok {
			entry = &accumulator{currency: currency}
			donators[key] = entry
		}

		entry.amount += event.Amount
		entry.count++
	}

	top := make([]TopDonator, 0, len(donators))

	for key, entry := range donators {
		username := key

		for i := 0; i < len(key); i++ {
			if key[i] == '|' {
				username = key[:i]

				break
			}
		}

		top = append(top, TopDonator{
			Username: username,
			Amount:   entry.amount,
			Currency: entry.currency,
			Count:    entry.count,
		})
	}

	sort.Slice(top, func(i, j int) bool {
		return top[i].Amount > top[j].Amount
	})

	if len(top) > 10 {
		top = top[:10]
	}

	stats.Top = top

	return stats
}
