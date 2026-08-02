package proxydebugger

import (
	"sort"
	"sync"
	"time"
)

type exchangeStore struct {
	mu          sync.RWMutex
	max         int
	order       []string
	exchanges   map[string]*Exchange
	subscribers map[chan storeEvent]struct{}
}

func newExchangeStore(max int) *exchangeStore {
	return &exchangeStore{
		max:         max,
		exchanges:   make(map[string]*Exchange),
		subscribers: make(map[chan storeEvent]struct{}),
	}
}

func (store *exchangeStore) create(exchange *Exchange) {
	store.mu.Lock()
	store.exchanges[exchange.ID] = exchange
	store.order = append([]string{exchange.ID}, store.order...)
	for len(store.order) > store.max {
		oldest := store.order[len(store.order)-1]
		store.order = store.order[:len(store.order)-1]
		delete(store.exchanges, oldest)
	}
	store.mu.Unlock()
	store.publish(storeEvent{Type: "created", ID: exchange.ID})
}

func (store *exchangeStore) update(id string, apply func(*Exchange)) {
	store.mu.Lock()
	if exchange := store.exchanges[id]; exchange != nil {
		apply(exchange)
	}
	store.mu.Unlock()
	store.publish(storeEvent{Type: "updated", ID: id})
}

func (store *exchangeStore) summaries() []ExchangeSummary {
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]ExchangeSummary, 0, len(store.order))
	for _, id := range store.order {
		if exchange := store.exchanges[id]; exchange != nil {
			result = append(result, exchange.ExchangeSummary)
		}
	}
	return result
}

func (store *exchangeStore) get(id string) (Exchange, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	exchange := store.exchanges[id]
	if exchange == nil {
		return Exchange{}, false
	}
	return cloneExchange(*exchange), true
}

func (store *exchangeStore) clear() {
	store.mu.Lock()
	store.order = nil
	store.exchanges = make(map[string]*Exchange)
	store.mu.Unlock()
	store.publish(storeEvent{Type: "cleared"})
}

func (store *exchangeStore) subscribe() (<-chan storeEvent, func()) {
	updates := make(chan storeEvent, 32)
	store.mu.Lock()
	store.subscribers[updates] = struct{}{}
	store.mu.Unlock()
	return updates, func() {
		store.mu.Lock()
		if _, ok := store.subscribers[updates]; ok {
			delete(store.subscribers, updates)
			close(updates)
		}
		store.mu.Unlock()
	}
}

func (store *exchangeStore) publish(event storeEvent) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	for subscriber := range store.subscribers {
		select {
		case subscriber <- event:
		default:
		}
	}
}

func cloneExchange(exchange Exchange) Exchange {
	exchange.Request = clonePayload(exchange.Request)
	exchange.Response = clonePayload(exchange.Response)
	return exchange
}

func clonePayload(payload Payload) Payload {
	payload.Headers = append([]Header(nil), payload.Headers...)
	payload.Frames = append([]FrameView(nil), payload.Frames...)
	return payload
}

func elapsedMS(startedAt time.Time) int64 {
	if startedAt.IsZero() {
		return 0
	}
	return time.Since(startedAt).Milliseconds()
}

func sortedHeaders(headers map[string][]string) []Header {
	result := make([]Header, 0, len(headers))
	for name, values := range headers {
		value := ""
		for index, item := range values {
			if index > 0 {
				value += ", "
			}
			value += item
		}
		if isSensitiveHeader(name) && value != "" {
			value = "[已隐藏]"
		}
		result = append(result, Header{Name: name, Value: value})
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Name < result[right].Name
	})
	return result
}

func isSensitiveHeader(name string) bool {
	switch httpCanonicalLower(name) {
	case "authorization", "cookie", "set-cookie", "proxy-authorization", "x-api-key":
		return true
	default:
		return false
	}
}

func httpCanonicalLower(value string) string {
	buffer := make([]byte, len(value))
	for index := range value {
		character := value[index]
		if character >= 'A' && character <= 'Z' {
			character += 'a' - 'A'
		}
		buffer[index] = character
	}
	return string(buffer)
}
