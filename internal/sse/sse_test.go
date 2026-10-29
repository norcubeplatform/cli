package sse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestReadParsesEvents(t *testing.T) {
	stream := ": keep-alive\n" +
		"data: {\"a\":1}\n\n" +
		"event: done\n" +
		"id: 7\n" +
		"data: line one\n" +
		"data: line two\n\n" +
		"data: no trailing blank line"
	var got []Event
	err := Read(context.Background(), strings.NewReader(stream), func(ev Event) (bool, error) {
		got = append(got, ev)
		return false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Event{
		{Name: "message", Data: `{"a":1}`},
		{Name: "done", Data: "line one\nline two", ID: "7"},
		{Name: "message", Data: "no trailing blank line", ID: "7"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestReadStopsWhenHandlerStops(t *testing.T) {
	n := 0
	err := Read(context.Background(), strings.NewReader("data: 1\n\ndata: 2\n\n"), func(Event) (bool, error) {
		n++
		return true, nil
	})
	if err != nil || n != 1 {
		t.Fatalf("err=%v n=%d, want nil and 1", err, n)
	}
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
}

func noSleep(context.Context, time.Duration) error { return nil }

func TestFollowReconnectsAfterADroppedStream(t *testing.T) {
	bodies := []string{"data: 1\n\n", "data: 2\n\nevent: done\ndata: {}\n\n"}
	opened := 0
	var seen []string
	err := Follow(context.Background(), FollowOptions{Sleep: noSleep},
		func(context.Context) (*http.Response, error) {
			b := bodies[opened]
			opened++
			return response(200, b), nil
		},
		func(ev Event) (bool, error) {
			seen = append(seen, ev.Name+":"+ev.Data)
			return ev.Name == "done", nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if opened != 2 || strings.Join(seen, ",") != "message:1,message:2,done:{}" {
		t.Fatalf("opened=%d seen=%v", opened, seen)
	}
}

func TestFollowGivesUpAfterMaxReconnects(t *testing.T) {
	opened := 0
	err := Follow(context.Background(), FollowOptions{MaxReconnects: 2, Sleep: noSleep},
		func(context.Context) (*http.Response, error) {
			opened++
			return nil, errors.New("connection refused")
		},
		func(Event) (bool, error) { return false, nil })
	if !errors.Is(err, ErrStreamFailed) || opened != 3 {
		t.Fatalf("err=%v opened=%d, want ErrStreamFailed after 3 attempts", err, opened)
	}
}

func TestFollowReturnsStatusErrors(t *testing.T) {
	err := Follow(context.Background(), FollowOptions{Sleep: noSleep},
		func(context.Context) (*http.Response, error) { return response(404, `{"msg":"x"}`), nil },
		func(Event) (bool, error) { return false, nil })
	var se *StatusError
	if !errors.As(err, &se) || se.Response.StatusCode != 404 {
		t.Fatalf("got %v, want a StatusError with 404", err)
	}
}

func TestFollowDoesNotReconnectAfterAHandlerError(t *testing.T) {
	opened := 0
	boom := errors.New("bad json")
	err := Follow(context.Background(), FollowOptions{Sleep: noSleep},
		func(context.Context) (*http.Response, error) {
			opened++
			return response(200, "data: x\n\n"), nil
		},
		func(Event) (bool, error) { return false, HandlerError(boom) })
	if !errors.Is(err, boom) || opened != 1 {
		t.Fatalf("err=%v opened=%d", err, opened)
	}
}

func TestFollowStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Follow(ctx, FollowOptions{},
		func(context.Context) (*http.Response, error) { return nil, errors.New("down") },
		func(Event) (bool, error) { return false, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}
