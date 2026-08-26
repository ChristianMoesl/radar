package workspace

import (
	"os"

	"radar/internal/pi"
)

func radarPiLaunch(args string, environment map[string]string) (string, map[string]string, error) {
	extensionPath, err := pi.MaterializeRadarExtension()
	if err != nil {
		return "", nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return "", nil, err
	}
	environmentCopy := make(map[string]string, len(environment)+1)
	for key, value := range environment {
		environmentCopy[key] = value
	}
	environmentCopy["RADAR_BINARY"] = executable
	return args + " --extension " + shellQuote(extensionPath), environmentCopy, nil
}
