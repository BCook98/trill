package demoapp

import (
	"crypto/tls"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// This mode is a deployment contract, not proxy-header autodetection. The Fly
// config exposes ONLY TLS port 443 to this HTTP listener. The private Fly network
// and its operators are trusted; never expose port 8080 directly to the internet.
func validateFlyOptions(o *options) error {
	app := os.Getenv("FLY_APP_NAME")
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$`).MatchString(app) || os.Getenv("FLY_MACHINE_ID") == "" {
		return errors.New("fly-edge-tls requires a Fly Machine environment")
	}
	if o.address != "https://"+app+".fly.dev" || o.listen != "0.0.0.0:8080" || o.cert != "" || o.key != "" {
		return errors.New("fly-edge-tls requires the app's HTTPS fly.dev origin, 0.0.0.0:8080, and no local TLS certificate")
	}

	return nil
}

func flyTLSHandler(address string, next http.Handler) http.Handler {
	host := strings.TrimPrefix(address, "https://")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host && r.Host != host+":443" {
			problem(w, http.StatusBadRequest, "unexpected demo host")
			return
		}
		r = r.Clone(r.Context())
		r.Host = host
		// Existing production auth and preview code expects TLS to describe the
		// client-facing transport. The fixed listener topology provides that fact;
		// Forwarded/X-Forwarded-* headers never select the scheme or authority.
		r.TLS = &tls.ConnectionState{}
		next.ServeHTTP(w, r)
	})
}
