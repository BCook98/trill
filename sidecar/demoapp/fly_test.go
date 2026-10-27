package demoapp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func flyOptions(t *testing.T) options {
	t.Helper()
	t.Setenv("FLY_APP_NAME", "trill-demo")
	t.Setenv("FLY_MACHINE_ID", "test-machine")
	o := goodOptions()
	o.flyEdgeTLS = true
	o.listen = "0.0.0.0:8080"
	o.address = "https://trill-demo.fly.dev"
	o.reviewPassword = "/private/password"
	return o
}
func TestFlyModeRequiresExplicitDeploymentContract(t *testing.T) {
	o := flyOptions(t)
	if err := validateOptions(&o); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*options){
		func(o *options) { o.flyEdgeTLS = false },
		func(o *options) { o.listen = "0.0.0.0:80" },
		func(o *options) { o.address = "https://other.fly.dev" },
		func(o *options) { o.cert = "cert.pem" },
	} {
		x := o
		change(&x)
		if err := validateOptions(&x); err == nil {
			t.Fatal("accepted unsafe Fly options")
		}
	}
	t.Setenv("FLY_MACHINE_ID", "")
	if err := validateOptions(&o); err == nil {
		t.Fatal("enabled Fly mode outside Machine")
	}
}
func TestFlyAuthorityNeverComesFromForwardingHeaders(t *testing.T) {
	calls := 0
	h := flyTLSHandler("https://trill-demo.fly.dev", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.TLS == nil || r.Host != "trill-demo.fly.dev" {
			t.Fatal("wrong trusted origin")
		}
		w.WriteHeader(204)
	}))
	for _, host := range []string{"trill-demo.fly.dev", "trill-demo.fly.dev:443", "evil.example", "trill-demo.fly.dev:8080"} {
		r := httptest.NewRequest("GET", "http://"+host+"/v1/sessions", nil)
		r.Header.Set("X-Forwarded-Host", "evil.example")
		r.Header.Set("X-Forwarded-Proto", "http")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 400
		if host == "trill-demo.fly.dev" || host == "trill-demo.fly.dev:443" {
			want = 204
		}
		if w.Code != want {
			t.Fatalf("host %s: %d", host, w.Code)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
