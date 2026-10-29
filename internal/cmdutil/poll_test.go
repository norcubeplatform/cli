package cmdutil

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPollBacksOffUntilDone(t *testing.T) {
	var waits []time.Duration
	calls := 0
	err := Poll(context.Background(), PollOptions{
		Interval:    time.Second,
		MaxInterval: 2 * time.Second,
		Sleep: func(_ context.Context, d time.Duration) error {
			waits = append(waits, d)
			return nil
		},
	}, func(context.Context) (bool, error) {
		calls++
		return calls == 4, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{time.Second, 1500 * time.Millisecond, 2 * time.Second}
	if len(waits) != len(want) {
		t.Fatalf("waits %v, want %v", waits, want)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("waits %v, want %v", waits, want)
		}
	}
}

func TestPollReturnsCheckErrors(t *testing.T) {
	boom := errors.New("boom")
	err := Poll(context.Background(), PollOptions{}, func(context.Context) (bool, error) { return false, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}

func TestPollTimesOut(t *testing.T) {
	err := Poll(context.Background(), PollOptions{Interval: time.Millisecond, Timeout: 20 * time.Millisecond},
		func(context.Context) (bool, error) { return false, nil })
	if !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("got %v, want ErrWaitTimeout", err)
	}
}

func TestPollStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	err := Poll(ctx, PollOptions{Interval: time.Hour}, func(context.Context) (bool, error) {
		cancel()
		return false, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}
