package payment

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/vgate-project/vgate-manager/internal/model"
)

// stubProvider is a minimal Provider used to exercise the Registry without a
// real gateway.
type stubProvider struct{}

func (stubProvider) Platform() string                                   { return "stub" }
func (stubProvider) Mode() string                                       { return ModeQR }
func (stubProvider) PayURL(*model.Order, string) (*PayDirective, error) { return nil, nil }
func (stubProvider) VerifyNotify(context.Context, *http.Request) (string, string, bool, error) {
	return "", "", false, nil
}

// TestListDoesNotDeadlock guards against the reentrant-mutex deadlock that
// previously blocked /api/v1/user/payment-methods: List() held r.mu while
// calling IsConfigured/Get, which re-acquire the same lock.
func TestListDoesNotDeadlock(t *testing.T) {
	r := NewRegistry(func() (map[string]string, error) {
		return map[string]string{}, nil
	})
	r.Register("stub", func(ConfigSource) (Provider, error) {
		return stubProvider{}, nil
	})

	done := make(chan []ChannelInfo, 1)
	go func() {
		done <- r.List()
	}()

	select {
	case out := <-done:
		if len(out) != 1 {
			t.Fatalf("expected 1 channel, got %d", len(out))
		}
		if out[0].Platform != "stub" || out[0].Mode != ModeQR {
			t.Fatalf("unexpected channel: %+v", out[0])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("List() deadlocked (did not return within 2s)")
	}
}
