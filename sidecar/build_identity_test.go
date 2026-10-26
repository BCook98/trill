package sidecar

import (
	"net/http"
	"testing"

	"trill/sidecar/buildinfo"
)

func TestHealthExposesBuildIdentityButKeepsBodyAuthenticated(t *testing.T) {
	handler, _, _ := setup(t, false, false)
	response := request(handler, http.MethodGet, "/v1/health", "", "")
	if response.Code != http.StatusUnauthorized {
		t.Fatal("health body lost authentication")
	}
	if response.Header().Get("X-Trill-Version") != buildinfo.Current().Version || response.Header().Get("X-Trill-Commit") != buildinfo.Current().Commit {
		t.Fatal("missing public build identity")
	}
}
