package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig(url string) Config {
	return Config{ServerURL: url, Token: "t234567890123456789012345678901234567890123"}
}

func TestPollCarriesOutCommandAndReportsResult(t *testing.T) {
	result := make(chan resultMsg, 1)
	var polls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/homeydeck/poll", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(tokenHeader) == "" {
			t.Error("poll had no token")
		}
		var status statusMsg
		_ = json.NewDecoder(r.Body).Decode(&status)
		if status.AgentVersion == "" {
			t.Error("poll had no version")
		}
		if polls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"commands":[{"id":"1","action":"sleep"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"commands":[]}`))
		time.Sleep(10 * time.Millisecond)
	})
	mux.HandleFunc("/api/homeydeck/result", func(w http.ResponseWriter, r *http.Request) {
		var res resultMsg
		_ = json.NewDecoder(r.Body).Decode(&res)
		result <- res
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var deferred atomic.Value
	c := NewClient(testConfig(srv.URL))
	c.afterReply = func(action string) { deferred.Store(action) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	select {
	case res := <-result:
		if !res.OK || res.Action != "sleep" || res.ID != "1" {
			t.Fatalf("unexpected result %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no result")
	}
	time.Sleep(50 * time.Millisecond)
	if deferred.Load() != "sleep" {
		t.Fatal("sleep wasn't run after the reply")
	}
}

func TestUnknownCommandIsRefused(t *testing.T) {
	if err := runAction("format_c"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestRemovedOnlyAfterTwoAnswers(t *testing.T) {
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"this PC was removed","code":"pc_removed"}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := NewClient(testConfig(srv.URL)).Run(ctx); !errors.Is(err, errRemoved) {
		t.Fatalf("got %v", err)
	}
	if polls.Load() != 2 {
		t.Fatalf("polled %d times", polls.Load())
	}
}

func TestOther401DoesNotUninstall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`401: Unauthorized`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := NewClient(testConfig(srv.URL)).Run(ctx); errors.Is(err, errRemoved) {
		t.Fatal("a plain 401 must not uninstall the agent")
	}
}

func TestSteamChangeIsReportedWithoutDroppingThePoll(t *testing.T) {
	status := make(chan statusMsg, 1)
	release := make(chan struct{})
	var polls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/homeydeck/poll", func(w http.ResponseWriter, r *http.Request) {
		polls.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = w.Write([]byte(`{"commands":[]}`))
	})
	mux.HandleFunc("/api/homeydeck/status", func(w http.ResponseWriter, r *http.Request) {
		var s statusMsg
		_ = json.NewDecoder(r.Body).Decode(&s)
		status <- s
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	defer close(release)

	t.Setenv("HOMEYDECK_FAKE_STEAM", "0")
	c := NewClient(testConfig(srv.URL))
	c.checkEvery = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)
	t.Setenv("HOMEYDECK_FAKE_STEAM", "1")

	select {
	case s := <-status:
		if !s.SteamRunning {
			t.Fatal("expected Steam running")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no status")
	}
	if polls.Load() != 1 {
		t.Fatalf("poll was restarted: %d polls", polls.Load())
	}
}

func TestSunshineChangeIsReported(t *testing.T) {
	status := make(chan statusMsg, 1)
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/homeydeck/poll", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = w.Write([]byte(`{"commands":[]}`))
	})
	mux.HandleFunc("/api/homeydeck/status", func(w http.ResponseWriter, r *http.Request) {
		var s statusMsg
		_ = json.NewDecoder(r.Body).Decode(&s)
		status <- s
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	defer close(release)

	t.Setenv("HOMEYDECK_FAKE_SUNSHINE", "0")
	c := NewClient(testConfig(srv.URL))
	c.checkEvery = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)
	t.Setenv("HOMEYDECK_FAKE_SUNSHINE", "1")

	select {
	case s := <-status:
		if !s.SunshineRunning {
			t.Fatal("expected Sunshine running")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no status")
	}
}

func TestLaunchSunshineSkippedWhenRunning(t *testing.T) {
	t.Setenv("HOMEYDECK_FAKE_SUNSHINE", "1")
	if err := runAction("launch_sunshine"); err != nil {
		t.Fatal(err)
	}
}

func TestBroadcastOf(t *testing.T) {
	_, ipnet, _ := net.ParseCIDR("192.168.100.0/24")
	ipnet.IP = net.ParseIP("192.168.100.24").To4()
	if got := broadcastOf(ipnet); got != "192.168.100.255" {
		t.Fatalf("got %s", got)
	}
}
