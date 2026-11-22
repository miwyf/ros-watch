package appupdate

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	// Bump for incompatible changes to the container runtime, its bundled data,
	// or the SQLite databases.
	Compatibility = "runtime1-db1"
	DirName       = ".ros-watch-update"
	DirEnv        = "ROS_WATCH_UPDATE_DIR"
	RestartCode   = 42
)

var ErrRestart = errors.New("apply staged update")

func ParseVersion(value string) ([3]uint64, bool) {
	var version [3]uint64
	parts := strings.Split(value, ".")
	if len(parts) != len(version) {
		return version, false
	}
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil || strconv.FormatUint(n, 10) != part {
			return version, false
		}
		version[i] = n
	}
	return version, true
}

func Newer(candidate, installed string) bool {
	next, valid := ParseVersion(candidate)
	have, ok := ParseVersion(installed)
	if !valid || !ok {
		return false
	}
	for i := range next {
		if next[i] != have[i] {
			return next[i] > have[i]
		}
	}
	return false
}

func Inspect(ctx context.Context, path string) (version, compatibility string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "update-info").Output()
	if err != nil {
		return "", "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 2 {
		if _, valid := ParseVersion(fields[0]); valid {
			return fields[0], fields[1], nil
		}
	}
	return "", "", errors.New("invalid update metadata")
}
