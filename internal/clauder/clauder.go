// Package clauder shells out to the clauder CLI for cross-instance
// messaging (https://github.com/MaorBril/clauder). All calls are
// best-effort — clauder is an optional companion.
package clauder

import (
	"os/exec"
	"regexp"
	"strings"
)

var instanceID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func Installed() bool {
	_, err := exec.LookPath("clauder")
	return err == nil
}

// Instances returns the IDs of running clauder instances.
func Instances() ([]string, error) {
	out, err := exec.Command("clauder", "instances").Output()
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if instanceID.MatchString(line) {
			ids = append(ids, line)
		}
	}
	return ids, nil
}

// Send delivers a message to one instance; clauder injects it into the
// session when idle.
func Send(id, message string) error {
	return exec.Command("clauder", "send", id, message).Run()
}

// Broadcast sends a message to every running instance, returning how many
// were reached.
func Broadcast(message string) int {
	if !Installed() {
		return 0
	}
	ids, err := Instances()
	if err != nil {
		return 0
	}
	sent := 0
	for _, id := range ids {
		if Send(id, message) == nil {
			sent++
		}
	}
	return sent
}
