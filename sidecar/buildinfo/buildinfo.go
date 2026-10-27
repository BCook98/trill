// Package buildinfo identifies Mac builds under the shared iOS/companion tag.
package buildinfo

import (
	"encoding/json"
	"io"
	"runtime"
	"runtime/debug"
)

// Set by the companion release builder with -ldflags. Source builds stay dev.
var Version = "dev"
var Commit = "unknown"
var ReleaseTag = "dev"
var Build = "dev"

type Info struct {
	Version      string `json:"version"`
	Commit       string `json:"commit"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	ReleaseTag   string `json:"releaseTag"`
	Build        string `json:"build"`
}

func Current() Info {
	commit := Commit
	if commit == "unknown" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" {
					commit = setting.Value
				}
			}
		}
	}
	return Info{Version: Version, Commit: commit, OS: runtime.GOOS, Architecture: runtime.GOARCH, ReleaseTag: ReleaseTag, Build: Build}
}

func Write(output io.Writer) error { return json.NewEncoder(output).Encode(Current()) }
