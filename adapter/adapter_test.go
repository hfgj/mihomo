package adapter

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/metacubex/mihomo/common/atomic"
	"github.com/metacubex/mihomo/common/queue"
	C "github.com/metacubex/mihomo/constant"
)

func TestExtraDelayHistoriesPublishedBeforeFirstRecord(t *testing.T) {
	proxy := &Proxy{}
	state := &internalProxyState{
		alive:   atomic.NewBool(false),
		history: queue.New[C.DelayHistory](defaultHistoriesNum),
	}
	const url = "https://example.com/generate_204"
	proxy.extra.Store(url, state)

	histories := proxy.ExtraDelayHistories()
	if histories[url].History == nil {
		t.Fatal("a published test state must expose an empty history array, not null")
	}
	data, err := json.Marshal(histories[url])
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		History json.RawMessage `json:"history"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded.History) != "[]" {
		t.Fatalf("empty history = %s, want []", decoded.History)
	}

	record := C.DelayHistory{Time: time.Now(), Delay: 42}
	state.history.Put(record)
	history := proxy.ExtraDelayHistories()[url].History
	if len(history) != 1 || history[0] != record {
		t.Fatalf("recorded history = %v, want [%v]", history, record)
	}
}
