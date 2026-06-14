package tmux

import (
	"fmt"
	"os/exec"
	"strconv"
)

func Popup(radarPath string) error {
	command := strconv.Quote(radarPath)
	if output, err := exec.Command("tmux", "display-popup", "-E", command).CombinedOutput(); err != nil {
		return fmt.Errorf("tmux display-popup failed: %s", output)
	}
	return nil
}
