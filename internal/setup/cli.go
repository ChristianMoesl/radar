package setup

import (
	"context"
	"regexp"
	"strconv"
	"strings"
)

// CLIConnection describes a foreground connection. Verify must check identity
// and read access without prompting. Only Login runs with the user's terminal.
type CLIConnection struct {
	Name, Service, MinimumVersion, Reason string
	InstallInstructions                   string
	Install                               *Command
	Env                                   []string
	Login                                 Command
	Verify                                func(context.Context) error
}

// ConnectCLI lets a user repair or defer one integration without losing the
// rest of setup. Installation and login are immediate external effects; callers
// record them in the final review, while configuration remains deferred.
func ConnectCLI(ctx context.Context, ui UI, sys System, cli CLIConnection, completed func(string)) (bool, error) {
	ui.Print("%s uses %s %s+ — %s. Credentials stay with %s.\n", cli.Service, cli.Name, cli.MinimumVersion, cli.Reason, cli.Name)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		installed := sys.LookPath(cli.Name)
		if installed {
			version, err := sys.Output(ctx, Command{Name: cli.Name, Args: []string{"--version"}, CaptureStderr: true, Env: cli.Env})
			if err == nil && VersionAtLeast(version, cli.MinimumVersion) {
				break
			}
		}
		ui.Print("Install or update %s, then ensure it is on PATH:\n  %s\n", cli.Name, cli.InstallInstructions)
		if installed {
			ui.Print("Repair or upgrade the existing %s installation using its original installation method.\n", cli.Name)
		}
		if !installed && cli.Install != nil {
			yes, err := ui.Confirm("Install or update "+cli.Name+" now?", true)
			if err != nil {
				return false, err
			}
			if yes {
				if err := sys.Run(ctx, *cli.Install); err == nil {
					if completed != nil {
						completed(cli.Name + " installation")
					}
				} else {
					ui.Print("%s installation did not complete. Use the instructions above and check PATH.\n", cli.Name)
				}
			}
		}
		yes, err := ui.Confirm("Recheck "+cli.Name+" now? (No defers "+cli.Service+")", true)
		if err != nil {
			return false, err
		}
		if !yes {
			ui.Print("%s deferred. Rerun radar setup to connect it.\n", cli.Service)
			return false, nil
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if err := cli.Verify(ctx); err == nil {
			return true, nil
		}
		ui.Print("%s identity or read access could not be verified. Check the selected account, permissions and network.\n", cli.Service)
		yes, err := ui.Confirm(cli.Service+" CLI is not authenticated. Run `"+cli.Name+" "+strings.Join(cli.Login.Args, " ")+"`?", true)
		if err != nil {
			return false, err
		}
		if !yes {
			ui.Print("%s deferred. Rerun radar setup to connect it.\n", cli.Service)
			return false, nil
		}
		if err := sys.Run(ctx, cli.Login); err == nil {
			if completed != nil {
				completed(cli.Service + " login")
			}
			if err := cli.Verify(ctx); err == nil {
				return true, nil
			}
		}
		ui.Print("%s login or access verification did not complete. A terminal, browser or the CLI's verification URL may be required.\n", cli.Service)
		yes, err = ui.Confirm("Retry "+cli.Service+" connection? (No defers it)", false)
		if err != nil {
			return false, err
		}
		if !yes {
			return false, nil
		}
	}
}

var cliVersion = regexp.MustCompile(`(?:^|\s)v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:\s|$)`)

// VersionAtLeast rejects unknown and prerelease spellings rather than guessing
// whether their command/output contracts match a qualified stable release.
func VersionAtLeast(output, minimum string) bool {
	match := cliVersion.FindStringSubmatch(strings.TrimSpace(output))
	parts := strings.Split(minimum, ".")
	if match == nil || len(parts) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(match[i+1])
		if err != nil {
			return false
		}
		min, err := strconv.Atoi(parts[i])
		if err != nil {
			return false
		}
		if n != min {
			return n > min
		}
	}
	return true
}
