package security

import (
	"net/http"
	"strings"
	"testing"
)

func TestGatewayMayListen(t *testing.T) {
	cases := []struct {
		addr  string
		share bool
		ok    bool
	}{
		{"127.0.0.1:3425", false, true},
		{"localhost:3425", false, true},
		{"[::1]:3425", true, true},
		{"0.0.0.0:3425", false, false},
		{"0.0.0.0:3425", true, true},
		{"192.0.2.10:3425", false, false},
		{"192.0.2.10:3425", true, true},
		{"not-an-address", true, false},
	}
	for _, tc := range cases {
		if got := GatewayMayListen(tc.addr, tc.share); got != tc.ok {
			t.Errorf("GatewayMayListen(%q, %v) = %v, want %v", tc.addr, tc.share, got, tc.ok)
		}
	}
}

func TestConsoleMayListen(t *testing.T) {
	cases := []struct {
		addr string
		cred bool
		ok   bool
	}{
		{"127.0.0.1:8081", false, true},
		{"localhost:8081", false, true},
		{"[::1]:8081", false, true},
		{"0.0.0.0:8081", false, false},
		{"0.0.0.0:8081", true, true},
		{"192.168.1.10:8081", false, false},
		{"192.168.1.10:8081", true, true},
		{"not-an-address", true, false},
	}
	for _, tc := range cases {
		if got := ConsoleMayListen(tc.addr, tc.cred); got != tc.ok {
			t.Errorf("ConsoleMayListen(%q, %v) = %v, want %v", tc.addr, tc.cred, got, tc.ok)
		}
	}
}

func TestSameListen(t *testing.T) {
	if !SameListen("127.0.0.1:8080", "127.0.0.1:8080") {
		t.Fatal("identical addresses differ")
	}
	if SameListen("127.0.0.1:8080", "127.0.0.1:8081") {
		t.Fatal("different ports matched")
	}
	if SameListen("0.0.0.0:8080", "127.0.0.1:8080") {
		t.Fatal("unspecified matched loopback")
	}
}

func TestEffectiveListenLeavesUserValue(t *testing.T) {
	if got := EffectiveListen("127.0.0.1:9090", DefaultGateway); got != "127.0.0.1:9090" {
		t.Fatalf("user value replaced: %s", got)
	}
	if got := EffectiveListen("", DefaultGateway); got != DefaultGateway {
		t.Fatalf("default = %s", got)
	}
}

func TestMaskHidesSecret(t *testing.T) {
	const secret = "super-secret-value"
	got := Mask(secret)
	if got == "" || strings.Contains(got, secret) || strings.Contains(got, "secret") {
		t.Fatalf("mask = %q", got)
	}
	if Mask("") != "" {
		t.Fatal("empty secret produced a mask")
	}
}

func TestAccessLineOmitsHeader(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "/v1/chat/completions?token=super-secret-value", strings.NewReader(`{"credential":"super-secret-value"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer super-secret-value")
	line := AccessLine(req.Method, req.URL.Path, http.StatusAccepted)
	if strings.Contains(line, "super-secret-value") || strings.Contains(line, "Bearer") || strings.Contains(line, "credential") {
		t.Fatalf("access line leaked request data: %s", line)
	}
	if line != "POST /v1/chat/completions 202" {
		t.Fatalf("line = %s", line)
	}
}

func TestCheckBaseURL(t *testing.T) {
	const console = "127.0.0.1:8081"
	cases := []struct {
		base string
		dial string
		want BaseResult
	}{
		{"", "", BaseUnknown},
		{"not a url", "", BaseUnknown},
		{"http://169.254.1.1/v1", "", BaseRejected},
		{"http://[fe80::1]/v1", "", BaseRejected},
		{"http://127.0.0.1:8081/v1", "", BaseRejected},
		{"http://localhost:8081/v1", "", BaseRejected},
		{"http://127.0.0.1:11434/v1", "", BaseOK},
		{"http://127.0.0.1:11434/v1", "http://127.0.0.1:11434/other", BaseOK},
		{"http://127.0.0.1:11434/v1", "http://127.0.0.1:9999/v1", BaseRejected},
		{"https://example.com/v1", "", BaseOK},
	}
	for _, tc := range cases {
		if got := CheckBaseURL(tc.base, console, tc.dial); got != tc.want {
			t.Errorf("CheckBaseURL(%q, dial %q) = %v, want %v", tc.base, tc.dial, got, tc.want)
		}
	}
}

func TestAllowRedirect(t *testing.T) {
	const saved = "http://127.0.0.1:11434/v1"
	if AllowRedirect(saved, "http://evil.example/v1") {
		t.Fatal("followed a different origin")
	}
	if AllowRedirect(saved, "http://169.254.1.1/x") {
		t.Fatal("followed link-local")
	}
	if !AllowRedirect(saved, "http://127.0.0.1:11434/other") {
		t.Fatal("rejected the same origin")
	}
	if !AllowRedirect(saved, "/other") {
		t.Fatal("rejected a relative redirect")
	}
}
