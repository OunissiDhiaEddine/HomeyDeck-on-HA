package main

// The agent keeps one request open to Home Assistant, which answers as soon as
// there's a command, or after 25 s with nothing. So commands arrive right away
// and an idle PC sends about two small requests a minute. It never accepts
// inbound connections.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	checkEvery     = 5 * time.Second
	pollTimeout    = 40 * time.Second // longer than POLL_HOLD_SECONDS in const.py
	requestTimeout = 15 * time.Second
	minBackoff     = 2 * time.Second
	maxBackoff     = 60 * time.Second
	removedAfter   = 2

	tokenHeader = "X-HomeyDeck-Token"
)

// errNetworkDown means the server's name doesn't resolve yet, which is what
// the first minute after a restart looks like. It's retried quickly without
// growing the backoff.
var errNetworkDown = errors.New("network not ready")

// errRemoved means Home Assistant no longer knows this PC: it was removed from
// the integration. It's only believed after removedAfter answers in a row.
var errRemoved = errors.New("this PC was removed from Home Assistant")

type Client struct {
	cfg  Config
	http *http.Client

	// commands that power the PC down run after the reply has been sent
	afterReply func(action string)
	checkEvery time.Duration

	mu         sync.Mutex
	cancelPoll context.CancelFunc
}

func NewClient(cfg Config) *Client {
	return &Client{cfg: cfg, afterReply: runDeferredAction, checkEvery: checkEvery}
}

// Run polls until ctx is cancelled, backing off while the server can't be
// reached. It returns errRemoved if the PC was removed from Home Assistant.
func (c *Client) Run(ctx context.Context) error {
	backoff := minBackoff
	quiet := false // already logged that the network is down
	removed := 0
	for ctx.Err() == nil {
		started := time.Now()
		err := c.session(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errRemoved) {
			if removed++; removed >= removedAfter {
				return err
			}
		} else {
			removed = 0
		}
		if time.Since(started) > time.Minute {
			backoff = minBackoff // it was working for a while, retry quickly
		}
		wait := backoff
		if errors.Is(err, errNetworkDown) {
			wait = minBackoff
			if !quiet {
				log.Printf("%v (checking every %s)", err, wait)
			}
			quiet = true
		} else {
			// Jitter keeps a flapping network from producing a perfectly regular storm.
			wait += time.Duration(rand.Int64N(int64(backoff / 2)))
			backoff = min(backoff*2, maxBackoff)
			quiet = false
			log.Printf("lost the server: %v (retrying in %s)", err, wait.Round(time.Second))
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return ctx.Err()
}

type statusMsg struct {
	Hostname        string `json:"hostname"`
	SteamRunning    bool   `json:"steamRunning"`
	SunshineRunning bool   `json:"sunshineRunning"`
	UptimeSeconds   int64  `json:"uptimeSeconds"`
	AgentVersion    string `json:"agentVersion"`
	MAC             string `json:"mac,omitempty"`
	Broadcast       string `json:"broadcast,omitempty"`
}

type resultMsg struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

type pollReply struct {
	Commands []struct {
		ID     string `json:"id"`
		Action string `json:"action"`
	} `json:"commands"`
}

func (c *Client) status() statusMsg {
	hostname, _ := os.Hostname()
	mac, broadcast := networkDetails(c.cfg.ServerURL)
	return statusMsg{
		Hostname:        hostname,
		SteamRunning:    steamRunning(),
		SunshineRunning: sunshineRunning(),
		UptimeSeconds:   int64(uptime().Seconds()),
		AgentVersion:    version,
		MAC:             mac,
		Broadcast:       broadcast,
	}
}

// session polls until a request fails.
func (c *Client) session(ctx context.Context) error {
	if err := c.networkReady(ctx); err != nil {
		return err
	}
	log.Printf("connecting to %s", c.cfg.ServerURL)

	sctx, stop := context.WithCancel(ctx)
	defer stop()
	go c.watch(sctx)

	for {
		pctx, cancel := context.WithTimeout(sctx, pollTimeout)
		c.setCancelPoll(cancel)
		var reply pollReply
		err := c.post(pctx, "/api/homeydeck/poll", c.status(), &reply)
		woken := errors.Is(pctx.Err(), context.Canceled) && sctx.Err() == nil
		cancel()
		if woken {
			continue // the PC woke up, so that request was stale
		}
		if err != nil {
			return err
		}
		for _, cmd := range reply.Commands {
			if err := c.carryOut(sctx, cmd.ID, cmd.Action); err != nil {
				return err
			}
		}
	}
}

func (c *Client) setCancelPoll(cancel context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancelPoll = cancel
}

// watch reports a Steam or Sunshine change right away, and notices when the PC slept: the
// request held open across a sleep is dead, and waiting out its timeout would
// show the PC as offline for no reason.
func (c *Client) watch(ctx context.Context) {
	steam, sunshine := steamRunning(), sunshineRunning()
	last := time.Now().Round(0) // wall clock, which keeps counting during sleep
	tick := time.NewTicker(c.checkEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		now := time.Now().Round(0)
		if now.Sub(last) > 3*c.checkEvery {
			log.Printf("woke from sleep, reconnecting")
			c.mu.Lock()
			if c.cancelPoll != nil {
				c.cancelPoll()
			}
			c.mu.Unlock()
		}
		last = now
		nowSteam, nowSunshine := steamRunning(), sunshineRunning()
		if nowSteam != steam || nowSunshine != sunshine {
			steam, sunshine = nowSteam, nowSunshine
			rctx, cancel := context.WithTimeout(ctx, requestTimeout)
			// A failure here is fine: the next poll carries the same news.
			if err := c.post(rctx, "/api/homeydeck/status", c.status(), nil); err != nil {
				log.Printf("status: %v", err)
			}
			cancel()
		}
	}
}

func (c *Client) carryOut(ctx context.Context, id, action string) error {
	log.Printf("command %s", action)
	res := resultMsg{ID: id, Action: action, OK: true}
	if err := runAction(action); err != nil {
		res.OK, res.Error = false, err.Error()
		log.Printf("command %s failed: %v", action, err)
	}
	rctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if err := c.post(rctx, "/api/homeydeck/result", res, nil); err != nil {
		return err
	}
	if res.OK {
		c.afterReply(action)
	}
	return nil
}

// networkReady checks that the server's name resolves.
func (c *Client) networkReady(ctx context.Context) error {
	u, err := url.Parse(c.cfg.ServerURL)
	if err != nil || u.Hostname() == "" || net.ParseIP(u.Hostname()) != nil || isLocalhost(u.Hostname()) {
		return nil
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := net.DefaultResolver.LookupHost(lctx, u.Hostname()); err != nil {
		return fmt.Errorf("%w: %v", errNetworkDown, err)
	}
	return nil
}

// networkDetails finds the network card used to reach the server, which is the
// one Wake-on-LAN must reach, so nobody has to look up a MAC address. Dialing
// UDP sends nothing; it only asks Windows which route it would use.
func networkDetails(server string) (mac, broadcast string) {
	u, err := url.Parse(server)
	if err != nil {
		return "", ""
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	conn, err := net.Dial("udp4", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return "", ""
	}
	defer conn.Close()
	local := conn.LocalAddr().(*net.UDPAddr).IP

	ifaces, err := net.Interfaces()
	if err != nil {
		return "", ""
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || !ipnet.IP.Equal(local) {
				continue
			}
			if len(iface.HardwareAddr) != 6 {
				return "", "" // loopback or a VPN: nothing that can be woken
			}
			return iface.HardwareAddr.String(), broadcastOf(ipnet)
		}
	}
	return "", ""
}

func broadcastOf(ipnet *net.IPNet) string {
	ip := ipnet.IP.To4()
	mask := ipnet.Mask
	if ip == nil {
		return ""
	}
	if len(mask) == net.IPv6len {
		mask = mask[12:]
	}
	if len(mask) != net.IPv4len {
		return ""
	}
	out := make(net.IP, net.IPv4len)
	for i := range out {
		out[i] = ip[i] | ^mask[i]
	}
	return out.String()
}

// One client reused across polls. Idle connections are dropped after 30 s, so a
// connection that went stale while the PC slept is never reused. Each request
// sets its own deadline.
func (c *Client) httpClient() *http.Client {
	if c.http == nil {
		c.http = &http.Client{Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			IdleConnTimeout:     30 * time.Second,
			MaxIdleConns:        2,
		}}
	}
	return c.http
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.ServerURL+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.Token != "" {
		req.Header.Set(tokenHeader, c.cfg.Token)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		c.httpClient().CloseIdleConnections()
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		if json.Unmarshal(raw, &e) != nil || e.Error == "" {
			e.Error = string(bytes.TrimSpace(raw))
		}
		if resp.StatusCode == http.StatusUnauthorized && e.Code == "pc_removed" {
			return errRemoved
		}
		return &serverError{status: resp.StatusCode, msg: e.Error}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 16<<10))
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(out)
}

type serverError struct {
	status int
	msg    string
}

func (e *serverError) Error() string {
	return fmt.Sprintf("server answered %d: %s", e.status, e.msg)
}

// pair trades a one-time pairing code for this PC's token.
func pair(code PairCode) (Config, error) {
	c := NewClient(Config{ServerURL: code.Server})
	hostname, _ := os.Hostname()
	mac, broadcast := networkDetails(code.Server)
	var reply struct {
		Token string `json:"token"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := c.post(ctx, "/api/homeydeck/pair", map[string]string{
		"code": code.Code, "hostname": hostname, "mac": mac, "broadcast": broadcast,
	}, &reply)
	var se *serverError
	if errors.As(err, &se) {
		return Config{}, errors.New(sentence(se.msg)) // the server's message is written for people
	}
	if err != nil {
		return Config{}, fmt.Errorf("couldn't reach %s: %w", code.host(), err)
	}
	cfg := Config{ServerURL: code.Server, Token: reply.Token}
	return cfg, validateConfig(cfg)
}

func sentence(s string) string {
	if s == "" {
		return s
	}
	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// runAction does the part of a command that must succeed before we answer.
// Sleep happens afterwards (see runDeferredAction) so the reply gets out first.
func runAction(action string) error {
	switch action {
	case "launch_steam":
		if steamRunning() {
			return nil
		}
		return launchSteam()
	case "reboot":
		return reboot() // Windows waits a few seconds before restarting
	case "sleep":
		return canSleep()
	default:
		return errors.New("unknown action")
	}
}

func runDeferredAction(action string) {
	if action != "sleep" {
		return
	}
	go func() {
		time.Sleep(1500 * time.Millisecond)
		if err := sleepNow(); err != nil {
			log.Printf("sleep failed: %v", err)
		}
	}()
}
