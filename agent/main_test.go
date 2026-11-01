package main

import "testing"

func TestParsePairCode(t *testing.T) {
	good := map[string]PairCode{
		"homeydeck-ha:http://192.168.1.10:8123/K7P2XM9Q4T":    {"http://192.168.1.10:8123", "K7P2XM9Q4T"},
		"http://192.168.1.10:8123/k7p2x-m9q4t":                {"http://192.168.1.10:8123", "K7P2XM9Q4T"},
		"192.168.1.10:8123/K7P2XM9Q4T":                        {"http://192.168.1.10:8123", "K7P2XM9Q4T"},
		"http://100.64.0.10:8123/K7P2XM9Q4T":                  {"http://100.64.0.10:8123", "K7P2XM9Q4T"},
		"http://homeassistant.local:8123/K7P2XM9Q4T":          {"http://homeassistant.local:8123", "K7P2XM9Q4T"},
		"home.example.ts.net/K7P2XM9Q4T":                      {"https://home.example.ts.net", "K7P2XM9Q4T"},
		"homeydeck-ha:https://home.example.ts.net/K7P2XM9Q4T": {"https://home.example.ts.net", "K7P2XM9Q4T"},
	}
	for in, want := range good {
		got, err := parsePairCode(in)
		if err != nil || got != want {
			t.Errorf("%q: got %+v, %v", in, got, err)
		}
	}
	bad := []string{
		"",
		"K7P2XM9Q4T",
		"http://192.168.1.10:8123/SHORT",
		"http://example.com/K7P2XM9Q4T", // the token mustn't cross the internet unencrypted
		"http://8.8.8.8:8123/K7P2XM9Q4T",
		"ftp://192.168.1.10/K7P2XM9Q4T",
	}
	for _, in := range bad {
		if got, err := parsePairCode(in); err == nil {
			t.Errorf("%q: expected an error, got %+v", in, got)
		}
	}
}

func TestConfigRoundTrip(t *testing.T) {
	t.Setenv("HOMEYDECK_DATA_DIR", t.TempDir())
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected not paired")
	}
	cfg := Config{ServerURL: "http://192.168.1.10:8123", Token: "t234567890123456789012345678901234567890123"}
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := loadConfig()
	if err != nil || got != cfg {
		t.Fatalf("got %+v, %v", got, err)
	}
}
