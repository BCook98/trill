package distribution

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"trill/sidecar/buildinfo"
)

const repositoryURL = "https://github.com/BCook98/trill"
const releaseAPI = "https://api.github.com/repos/BCook98/trill/releases?per_page=100"
const maximumArchiveBytes = 200 << 20

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,3})\.(0|[1-9][0-9]{0,3})\.(0|[1-9][0-9]{0,3})$`)
var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Artifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Schema    int                 `json:"schema"`
	Version   string              `json:"version"`
	Commit    string              `json:"commit"`
	Artifacts map[string]Artifact `json:"artifacts"`
}

func CompareVersions(left, right string) (int, error) {
	if !versionPattern.MatchString(left) || !versionPattern.MatchString(right) {
		return 0, errors.New("companion versions must use numeric MAJOR.MINOR.PATCH")
	}
	leftParts, rightParts := strings.Split(left, "."), strings.Split(right, ".")
	for index := range leftParts {
		leftNumber, _ := strconv.Atoi(leftParts[index])
		rightNumber, _ := strconv.Atoi(rightParts[index])
		if leftNumber < rightNumber {
			return -1, nil
		}
		if leftNumber > rightNumber {
			return 1, nil
		}
	}
	return 0, nil
}

func artifactName(version, architecture string) string {
	return "trill-companion-" + version + "-darwin-" + architecture + ".zip"
}
func assetURL(version, name string) string {
	return repositoryURL + "/releases/download/companion-v" + version + "/" + name
}

func httpClient() *http.Client {
	return &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(request *http.Request, previous []*http.Request) error {
		if request.URL.Scheme != "https" || request.URL.User != nil || len(previous) >= 10 {
			return errors.New("refusing unsafe release redirect")
		}
		return nil
	}}
}

func download(ctx context.Context, client *http.Client, address string, output io.Writer, maximumBytes int64) error {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil {
		return errors.New("release downloads require HTTPS")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "trill-companion/"+buildinfo.Version)
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("release server returned HTTP %d", response.StatusCode)
	}
	written, err := io.Copy(output, io.LimitReader(response.Body, maximumBytes+1))
	if err != nil {
		return err
	}
	if written > maximumBytes {
		return errors.New("release response exceeds size limit")
	}
	return nil
}

func LatestVersion(ctx context.Context) (string, error) {
	client := httpClient()
	for page := 1; page <= 10; page++ {
		var buffer strings.Builder
		if err := download(ctx, client, releaseAPI+"&page="+strconv.Itoa(page), &buffer, 8<<20); err != nil {
			return "", err
		}
		var releases []struct {
			Tag        string `json:"tag_name"`
			Draft      bool   `json:"draft"`
			Prerelease bool   `json:"prerelease"`
		}
		if err := json.Unmarshal([]byte(buffer.String()), &releases); err != nil {
			return "", errors.New("invalid release listing")
		}
		// GitHub orders releases by creation time. The first stable companion
		// release is authoritative; iOS release tags are deliberately ignored.
		for _, release := range releases {
			version := strings.TrimPrefix(release.Tag, "companion-v")
			if !release.Draft && !release.Prerelease && strings.HasPrefix(release.Tag, "companion-v") && versionPattern.MatchString(version) {
				return version, nil
			}
		}
		if len(releases) < 100 {
			break
		}
	}
	return "", errors.New("no published stable companion release found")
}

func FetchManifest(ctx context.Context, version string) (Manifest, error) {
	var manifest Manifest
	if !versionPattern.MatchString(version) {
		return manifest, errors.New("invalid companion release version")
	}
	var buffer strings.Builder
	if err := download(ctx, httpClient(), assetURL(version, "manifest.json"), &buffer, 1<<20); err != nil {
		return manifest, err
	}
	if err := json.Unmarshal([]byte(buffer.String()), &manifest); err != nil {
		return manifest, errors.New("invalid companion manifest")
	}
	if err := manifest.Validate(version); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func (manifest Manifest) Validate(version string) error {
	if manifest.Schema != 1 || manifest.Version != version || !versionPattern.MatchString(version) || !commitPattern.MatchString(manifest.Commit) {
		return errors.New("release manifest schema, version or commit is invalid")
	}
	if len(manifest.Artifacts) != 2 {
		return errors.New("release must include arm64 and amd64 artifacts")
	}
	for _, architecture := range []string{"arm64", "amd64"} {
		artifact := manifest.Artifacts[architecture]
		if artifact.Name != artifactName(version, architecture) || !digestPattern.MatchString(artifact.SHA256) {
			return errors.New("release manifest artifact is invalid")
		}
	}
	return nil
}

// Stage downloads and authenticates the complete release before executing any
// payload. Hashes detect corruption; pinned Developer ID requirements establish
// publisher identity. Embedded versions bind signed code to manifest metadata.
func Stage(ctx context.Context, manifest Manifest, parent string) (string, error) {
	if _, err := SigningTeam(); err != nil {
		return "", err
	}
	if runtime.GOOS != "darwin" {
		return "", errors.New("companion releases require macOS")
	}
	return stage(ctx, manifest, parent, runtime.GOARCH, httpClient(), VerifyExecutable)
}

func stage(ctx context.Context, manifest Manifest, parent, architecture string, client *http.Client, verify func(context.Context, string, string) error) (string, error) {
	if err := manifest.Validate(manifest.Version); err != nil {
		return "", err
	}
	artifact, ok := manifest.Artifacts[architecture]
	if !ok {
		return "", errors.New("unsupported companion architecture")
	}
	staging, err := os.MkdirTemp(parent, ".release-*")
	if err != nil {
		return "", err
	}
	success := false
	defer func() {
		if !success {
			os.RemoveAll(staging)
		}
	}()
	archive, err := os.CreateTemp(parent, ".download-*.zip")
	if err != nil {
		return "", err
	}
	defer os.Remove(archive.Name())
	digest := sha256.New()
	downloadErr := download(ctx, client, assetURL(manifest.Version, artifact.Name), io.MultiWriter(archive, digest), maximumArchiveBytes)
	closeErr := archive.Close()
	if downloadErr != nil {
		return "", downloadErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if hex.EncodeToString(digest.Sum(nil)) != artifact.SHA256 {
		return "", errors.New("release archive SHA-256 does not match manifest")
	}
	if err := extractArchive(archive.Name(), staging); err != nil {
		return "", err
	}
	for _, binary := range []struct{ name, identifier string }{{"trill", CLIIdentifier}, {"trillctl", CLIIdentifier}, {"trilld", DaemonIdentifier}} {
		if err := verify(ctx, filepath.Join(staging, binary.name), binary.identifier); err != nil {
			return "", err
		}
	}
	for _, binary := range []string{"trill", "trillctl", "trilld"} {
		argument := "version"
		if binary == "trilld" {
			argument = "--version"
		}
		command := exec.CommandContext(ctx, filepath.Join(staging, binary), argument)
		var output limitedBuffer
		command.Stdout = &output
		if err := command.Run(); err != nil {
			return "", fmt.Errorf("verified %s failed its version check", binary)
		}
		var info buildinfo.Info
		if err := json.Unmarshal(output.data, &info); err != nil || info.Version != manifest.Version || info.Commit != manifest.Commit || info.OS != "darwin" || info.Architecture != architecture {
			return "", fmt.Errorf("%s embedded identity does not match the release", binary)
		}
	}
	success = true
	return staging, nil
}

type limitedBuffer struct{ data []byte }

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	if len(buffer.data)+len(data) > 4096 {
		return 0, errors.New("version output exceeds size limit")
	}
	buffer.data = append(buffer.data, data...)
	return len(data), nil
}

func extractArchive(archivePath, destination string) error {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	seen := map[string]bool{}
	for _, entry := range archive.File {
		if (entry.Name != "trill" && entry.Name != "trillctl" && entry.Name != "trilld") || seen[entry.Name] || !entry.Mode().IsRegular() || entry.UncompressedSize64 > maximumArchiveBytes/3 {
			return errors.New("release archive contains an unexpected, duplicate or oversized entry")
		}
		seen[entry.Name] = true
		input, err := entry.Open()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(filepath.Join(destination, entry.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if err != nil {
			input.Close()
			return err
		}
		_, copyErr := io.Copy(output, io.LimitReader(input, maximumArchiveBytes/3+1))
		syncErr := output.Sync()
		closeErr := output.Close()
		input.Close()
		if copyErr != nil {
			return copyErr
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if len(seen) != 3 {
		return errors.New("release archive is missing companion executables")
	}
	return nil
}
