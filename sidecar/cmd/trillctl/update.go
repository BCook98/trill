package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"trill/sidecar/buildinfo"
	"trill/sidecar/distribution"
	"trill/sidecar/setup"
)

func update(args []string, output, errorOutput io.Writer, home string) error {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	checkOnly := flags.Bool("check", false, "check the latest stable release without modifying installation or services")
	install := flags.Bool("install", false, "first install into a new managed location; never adopt source/custom installations")
	requestedTag := flags.String("tag", "", "select an exact shared vVERSION-build.COMMIT12 tag; downgrades are refused")
	stateDirectory := flags.String("state-dir", filepath.Join(home, ".config", "trill"), "existing companion state directory")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || (*checkOnly && *install) {
		return errors.New("usage: trill update [--check | --install] [--tag vVERSION-build.COMMIT12] [--state-dir PATH]")
	}
	if *requestedTag != "" {
		if _, err := distribution.ReleaseVersion(*requestedTag); err != nil {
			return err
		}
	}
	if runtime.GOOS != "darwin" {
		return errors.New("companion updates require macOS")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if distribution.HomebrewPrefix(executable) != "" {
		_, err := fmt.Fprintln(output, "Homebrew manages this installation. Run: brew upgrade --cask bcook98/tap/trill")
		return err
	}
	if _, err := distribution.SigningTeam(); err != nil {
		return err
	}
	if _, err := distribution.CompareVersions(buildinfo.Version, buildinfo.Version); err != nil {
		return errors.New("source/development builds cannot install signed releases; rebuild using docs/DEVELOPMENT.md or use Homebrew for a new installation")
	}
	dir, err := filepath.Abs(*stateDirectory)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var config setup.Config
	configured := false
	if _, err := os.Lstat(filepath.Join(dir, "config.json")); err == nil {
		config, err = setup.LoadConfig(dir)
		if err != nil {
			return err
		}
		configured = true
		if config.CLI != filepath.Join(distribution.InstallationRoot(home), "bin", "trill") || config.Daemon != filepath.Join(distribution.InstallationRoot(home), "bin", "trilld") {
			return errors.New("source/custom configuration detected; automatic conversion is not supported; rebuild its binaries using docs/DEVELOPMENT.md or explicitly migrate it")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !configured && !*install && !*checkOnly {
		return errors.New("no companion configuration found; run trill setup or supply the original --state-dir before updating")
	}
	var root string
	if !*checkOnly {
		if !configured {
			agents := setup.Agents{Runner: setup.ExecRunner{}, Home: home, UID: os.Getuid()}
			if err := agents.RequireNoServices(ctx); err != nil {
				return err
			}
		}
		root, err = distribution.OpenInstallation(home, *install)
		if err != nil {
			return err
		}
		unlock, err := setup.LockMaintenance(root)
		if err != nil {
			return err
		}
		defer unlock()
		if err := distribution.PreflightLinks(home, root); err != nil {
			return err
		}
		if *install {
			if _, err := os.Lstat(filepath.Join(root, "current")); err == nil {
				return errors.New("signed companion is already installed; run trill update")
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if !*install {
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			executable, err = filepath.EvalSymlinks(executable)
			if err != nil {
				return err
			}
			if distribution.StableCLI(home, executable) != filepath.Join(root, "bin", "trill") {
				return errors.New("run update from the currently installed trill command, not an older or extracted release")
			}
		}
		if configured {
			agents := setup.Agents{Runner: setup.ExecRunner{}, Home: home, UID: os.Getuid()}
			if recovered, err := agents.RecoverRelease(ctx, config, dir, root); err != nil {
				return err
			} else if recovered {
				fmt.Fprintln(output, "Recovered the previous release and service state. Run trill update again to retry.")
				return nil
			}
		}
	}
	tag := *requestedTag
	if tag == "" {
		tag, err = distribution.LatestReleaseTag(ctx)
		if err != nil {
			return err
		}
	}
	manifest, err := distribution.FetchManifest(ctx, tag)
	if err != nil {
		return err
	}
	if !*install {
		available, err := distribution.UpdateAvailable(manifest, buildinfo.Current())
		if err != nil {
			return err
		}
		if !available && !*checkOnly {
			fmt.Fprintf(output, "Companion %s is current.\n", tag)
			return nil
		}
	}
	if *checkOnly {
		return json.NewEncoder(output).Encode(struct {
			Installed string `json:"installed"`
			Available string `json:"available"`
			Commit    string `json:"commit"`
			Tag       string `json:"tag"`
			Build     string `json:"build"`
		}{buildinfo.Version, manifest.Version, manifest.Commit, manifest.Tag, manifest.Build})
	}
	staging, err := distribution.Stage(ctx, manifest, filepath.Join(root, "releases"))
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	target := "releases/" + manifest.Version + "-" + manifest.Commit[:12] + "-" + strings.TrimPrefix(filepath.Base(staging), ".release-")
	if err := os.Rename(staging, filepath.Join(root, target)); err != nil {
		return err
	}
	releases, err := os.Open(filepath.Join(root, "releases"))
	if err != nil {
		return err
	}
	syncErr := releases.Sync()
	closeErr := releases.Close()
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := distribution.InstallLinks(home, root); err != nil {
		return err
	}
	if configured {
		fmt.Fprintln(errorOutput, "Updating disconnects phone transports; provider sessions and paired devices are preserved.")
		agents := setup.Agents{Runner: setup.ExecRunner{}, Home: home, UID: os.Getuid()}
		if err := agents.SwitchRelease(ctx, config, dir, root, target, func(ctx context.Context) error {
			return awaitUpdatedCompanion(ctx, config, manifest.Version, manifest.Commit)
		}); err != nil {
			return err
		}
	} else {
		if err := setup.SetCurrentRelease(root, target); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(output, "Installed companion %s (%s).\nRun %s doctor to check this installation.\n", manifest.Version, manifest.Commit[:12], filepath.Join(root, "bin", "trill"))
	return err
}

func awaitUpdatedCompanion(ctx context.Context, config setup.Config, expectedVersion, expectedCommit string) error {
	healthCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if version, commit, err := runningCompanion(healthCtx, config); err == nil && version == expectedVersion && commit == expectedCommit {
			return nil
		}
		select {
		case <-healthCtx.Done():
			return errors.New("updated companion did not become reachable with its existing TLS identity")
		case <-ticker.C:
		}
	}
}
