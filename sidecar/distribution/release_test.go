package distribution

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"trill/sidecar/buildinfo"
)

func TestVersionComparisonRejectsUnsafeAndDevelopmentVersions(t *testing.T) {
	for _, version := range []string{"dev", "1.2", "v1.2.3", "01.2.3", "1.2.3-beta", "1.2.3\n", "10000.1.1", "../../bad"} {
		if _, err := CompareVersions(version, "1.0.0"); err == nil {
			t.Fatalf("accepted %q", version)
		}
	}
	for _, item := range []struct {
		left, right string
		comparison  int
	}{{"1.10.0", "1.9.9", 1}, {"0.1.0", "0.1.1", -1}, {"2.0.0", "2.0.0", 0}} {
		comparison, err := CompareVersions(item.left, item.right)
		if err != nil || comparison != item.comparison {
			t.Fatalf("%+v: %d %v", item, comparison, err)
		}
	}
}

func validManifest() Manifest {
	manifest := Manifest{1, "0.1.0", strings.Repeat("a", 40), map[string]Artifact{}}
	for _, architecture := range []string{"arm64", "amd64"} {
		manifest.Artifacts[architecture] = Artifact{artifactName(manifest.Version, architecture), strings.Repeat("b", 64)}
	}
	return manifest
}

func TestManifestBindsVersionAndRestrictsArtifactNames(t *testing.T) {
	if err := validManifest().Validate("0.1.0"); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Manifest){
		func(m *Manifest) { m.Version = "0.2.0" },
		func(m *Manifest) { m.Schema = 2 },
		func(m *Manifest) { m.Commit = "bad" },
		func(m *Manifest) {
			m.Artifacts["arm64"] = Artifact{"https://evil.invalid/release.zip", strings.Repeat("b", 64)}
		},
		func(m *Manifest) { delete(m.Artifacts, "amd64") },
		func(m *Manifest) { m.Artifacts["arm64"] = Artifact{artifactName(m.Version, "arm64"), "bad"} },
	} {
		manifest := validManifest()
		mutate(&manifest)
		if err := manifest.Validate("0.1.0"); err == nil {
			t.Fatalf("accepted invalid manifest: %+v", manifest)
		}
	}
}

func TestExtractionRejectsTraversalLinksDuplicatesAndMissingTools(t *testing.T) {
	for _, names := range [][]string{{"../escaped", "trill", "trillctl", "trilld"}, {"trill", "trill", "trilld"}, {"trill"}, {"trill", "trillctl", "trilld", "extra"}, {"/absolute"}, {"symlink"}, {"trill", "trillctl", "trilld"}} {
		t.Run(strings.Join(names, ","), func(t *testing.T) {
			directory := t.TempDir()
			archivePath := filepath.Join(directory, "release.zip")
			file, err := os.Create(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			archive := zip.NewWriter(file)
			for _, name := range names {
				header := &zip.FileHeader{Name: name}
				header.SetMode(0700)
				if name == "symlink" {
					header.SetMode(os.ModeSymlink | 0700)
				}
				entry, err := archive.CreateHeader(header)
				if err != nil {
					t.Fatal(err)
				}
				io.WriteString(entry, "fixture")
			}
			if err := archive.Close(); err != nil {
				t.Fatal(err)
			}
			file.Close()
			destination := filepath.Join(directory, "stage")
			os.Mkdir(destination, 0700)
			err = extractArchive(archivePath, destination)
			valid := strings.Join(names, ",") == "trill,trillctl,trilld"
			if (err == nil) != valid {
				t.Fatalf("valid=%t error=%v", valid, err)
			}
			if _, err := os.Stat(filepath.Join(directory, "escaped")); !os.IsNotExist(err) {
				t.Fatal("archive escaped staging")
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestDownloadsRejectHTTPStatusAndOversizedResponses(t *testing.T) {
	for _, status := range []int{200, 404} {
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("too much"))}, nil
		})}
		if err := download(context.Background(), client, "https://example.invalid/file", io.Discard, 3); err == nil {
			t.Fatalf("accepted status %d / oversized response", status)
		}
		if err := download(context.Background(), client, "http://example.invalid/file", io.Discard, 100); err == nil {
			t.Fatal("accepted cleartext URL")
		}
	}
	client := httpClient()
	request, _ := http.NewRequest("GET", "http://example.invalid/redirect", nil)
	if err := client.CheckRedirect(request, nil); err == nil {
		t.Fatal("accepted HTTPS downgrade redirect")
	}
}

func TestSigningTrustCannotBeConfiguredThroughEnvironment(t *testing.T) {
	previous := signingTeam
	defer func() { signingTeam = previous }()
	signingTeam = ""
	t.Setenv("COMPANION_SIGNING_TEAM", "ABCDEFGHIJ")
	if _, err := SigningTeam(); err == nil {
		t.Fatal("environment bypassed compiled trust configuration")
	}
	signingTeam = "ABCDEFGHIJ\n"
	if team, err := SigningTeam(); err != nil || team != "ABCDEFGHIJ" {
		t.Fatalf("%s %v", team, err)
	}
	if err := VerifyExecutable(context.Background(), "/usr/bin/true", CLIIdentifier); err == nil {
		t.Fatal("accepted a binary from an unexpected signing team")
	}
}

func TestStagingAuthenticatesEveryExecutableBeforeRunningPayload(t *testing.T) {
	for _, scenario := range []string{"valid", "bad-hash", "bad-signature", "wrong-version"} {
		t.Run(scenario, func(t *testing.T) {
			parent := t.TempDir()
			marker := filepath.Join(parent, "executed")
			manifest := validManifest()
			info := buildinfo.Info{Version: manifest.Version, Commit: manifest.Commit, OS: "darwin", Architecture: "arm64"}
			if scenario == "wrong-version" {
				info.Version = "0.0.1"
			}
			identity, _ := json.Marshal(info)
			var archiveBytes bytes.Buffer
			archive := zip.NewWriter(&archiveBytes)
			for _, binary := range []string{"trill", "trillctl", "trilld"} {
				header := &zip.FileHeader{Name: binary}
				header.SetMode(0700)
				entry, err := archive.CreateHeader(header)
				if err != nil {
					t.Fatal(err)
				}
				// Both paths and JSON are generated entirely from local test data.
				io.WriteString(entry, "#!/bin/sh\ntouch '"+marker+"'\nprintf '%s\\n' '"+string(identity)+"'\n")
			}
			if err := archive.Close(); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(archiveBytes.Bytes())
			artifact := manifest.Artifacts["arm64"]
			artifact.SHA256 = hex.EncodeToString(digest[:])
			if scenario == "bad-hash" {
				artifact.SHA256 = strings.Repeat("0", 64)
			}
			manifest.Artifacts["arm64"] = artifact
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.String() != assetURL(manifest.Version, artifact.Name) {
					t.Error("downloaded an unexpected asset")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(archiveBytes.Bytes()))}, nil
			})}
			verified := 0
			verify := func(context.Context, string, string) error {
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Error("executed a payload before all signatures were checked")
				}
				verified++
				if scenario == "bad-signature" && verified == 3 {
					return errors.New("untrusted signature fixture")
				}
				return nil
			}
			staging, err := stage(context.Background(), manifest, parent, "arm64", client, verify)
			if scenario == "valid" {
				if err != nil || staging == "" || verified != 3 {
					t.Fatalf("%s %d %v", staging, verified, err)
				}
			} else {
				if err == nil || staging != "" {
					t.Fatalf("accepted %s", scenario)
				}
				entries, _ := os.ReadDir(parent)
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".release-") || strings.HasPrefix(entry.Name(), ".download-") {
						t.Error("failed staging left partial payloads")
					}
				}
			}
			if scenario == "bad-hash" || scenario == "bad-signature" {
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("executed an unauthenticated payload")
				}
			}
		})
	}
}
