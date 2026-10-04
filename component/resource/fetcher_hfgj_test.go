package resource

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/mihomo/common/utils"
	P "github.com/metacubex/mihomo/constant/provider"
	"github.com/stretchr/testify/require"
)

type hfgjMetadataVehicle struct {
	path     string
	sequence atomic.Int32
}

func (v *hfgjMetadataVehicle) Path() string        { return v.path }
func (v *hfgjMetadataVehicle) Url() string         { return "http://mock.example" }
func (v *hfgjMetadataVehicle) Proxy() string       { return "" }
func (v *hfgjMetadataVehicle) Type() P.VehicleType { return P.HTTP }
func (v *hfgjMetadataVehicle) Write([]byte) error  { return nil }
func (v *hfgjMetadataVehicle) Read(context.Context, utils.HashType) ([]byte, utils.HashType, error) {
	buf := []byte(strconv.Itoa(int(v.sequence.Add(1))))
	return buf, utils.MakeHash(buf), nil
}

func TestHFGJFetcherMetadataDuringPublication(t *testing.T) {
	vehicle := &hfgjMetadataVehicle{path: t.TempDir() + "/cache"}
	var f *Fetcher[string]
	var callbacks atomic.Int32
	// API/rule-provider callbacks must be able to read metadata without deadlock.
	f = NewFetcher("metadata", 0, vehicle, nil, func(buf []byte) (string, error) { return string(buf), nil }, func(string) {
		if !f.UpdatedAt().IsZero() {
			callbacks.Add(1)
		}
	})
	defer f.Close()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, _ = f.Update() }()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("metadata callback deadlocked")
	}
	require.EqualValues(t, 16, callbacks.Load())
	require.False(t, f.UpdatedAt().IsZero())
}
