// Package distribution manages signed companion releases. Trust is anchored in
// the public Apple team ID committed before the first release, never in downloads.
package distribution

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

//go:embed signing-team.txt
var signingTeam string

func SigningTeam() (string, error) {
	team := strings.TrimSpace(signingTeam)
	if !regexp.MustCompile(`^[A-Z0-9]{10}$`).MatchString(team) {
		return "", errors.New("signed distribution is not configured: maintainer must set distribution/signing-team.txt to the public Apple Developer team ID")
	}
	return team, nil
}

func VerifyExecutable(ctx context.Context, path, identifier string) error {
	team, err := SigningTeam()
	if err != nil {
		return err
	}
	if runtime.GOOS != "darwin" {
		return errors.New("signature verification requires macOS")
	}
	requirement := fmt.Sprintf(`anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists and certificate leaf[subject.OU] = "%s" and identifier "%s"`, team, identifier)
	if err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--strict", "--all-architectures", "--test-requirement", requirement, path).Run(); err != nil {
		return fmt.Errorf("%s: expected Developer ID signature from team %s (%s)", path, team, identifier)
	}
	if err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--check-notarization", path).Run(); err != nil {
		return fmt.Errorf("%s: Apple notarization could not be verified; internet access and an accepted notarization ticket are required", path)
	}
	return nil
}

const CLIIdentifier = "io.bcook.trill.cli"
const DaemonIdentifier = "io.bcook.trill.daemon"
