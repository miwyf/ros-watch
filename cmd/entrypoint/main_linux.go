// Container bootstrap: prepare application data, then run with the requested IDs.
package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
	"ros-watch/internal/appupdate"
)

func configuredID(name string) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return 1000, nil
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("%s must be a numeric ID between 0 and 2147483647", name)
		}
	}
	id, err := strconv.ParseUint(value, 10, 31)
	if err != nil {
		return 0, fmt.Errorf("%s must be a numeric ID between 0 and 2147483647", name)
	}
	return int(id), nil
}

// Only the data directory and ROS Watch's own files are adjusted. Open without
// following symlinks so a mounted directory cannot redirect ownership changes.
func prepareData(dir string, uid, gid int) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err = ownFile(fd, uid, gid, 0700); err != nil {
		return err
	}
	names := []string{"session.key"}
	for _, database := range []string{"control.sqlite3", "events.sqlite3"} {
		for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
			names = append(names, database+suffix)
		}
	}
	if err := prepareFiles(fd, names, uid, gid, 0600); err != nil {
		return err
	}
	if uid != 0 {
		if err := prepareUpdateData(fd, uid, gid); err != nil {
			log.Printf("ROS Watch upgrade directory unavailable: %v", err)
		}
	}
	return nil
}

func prepareUpdateData(fd, uid, gid int) error {
	updateFD, err := unix.Openat(fd, appupdate.DirName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(updateFD)
	if err := ownFile(updateFD, uid, gid, 0700); err != nil {
		return err
	}
	return prepareFiles(updateFD, []string{"current", "pending"}, uid, gid, 0700)
}

func prepareFiles(fd int, names []string, uid, gid int, mode uint32) error {
	for _, name := range names {
		file, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err == nil {
			var stat unix.Stat_t
			err = unix.Fstat(file, &stat)
			if err == nil && stat.Mode&unix.S_IFMT != unix.S_IFREG {
				err = errors.New("expected a regular application data file")
			}
			if err == nil {
				err = ownFile(file, uid, gid, mode)
			}
			unix.Close(file)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func ownFile(fd, uid, gid int, mode uint32) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if int(stat.Uid) != uid || int(stat.Gid) != gid {
		if err := unix.Fchown(fd, uid, gid); err != nil {
			return err
		}
	}
	if stat.Mode&mode != mode {
		return unix.Fchmod(fd, stat.Mode&0777|mode)
	}
	return nil
}

func run() error {
	uid, err := configuredID("PUID")
	if err != nil {
		return err
	}
	gid, err := configuredID("PGID")
	if err != nil {
		return err
	}
	dir := os.Getenv("DATA_DIR")
	if dir == "" {
		dir = "/data"
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return err
	}
	if dir == "/" {
		return errors.New("DATA_DIR must be a dedicated application data directory")
	}
	if os.Geteuid() == 0 {
		if err = prepareData(dir, uid, gid); err != nil {
			return fmt.Errorf("prepare DATA_DIR %s for %d:%d: %w; check the writable mount and initialization capabilities", dir, uid, gid, err)
		}
		if err = syscall.Setgroups([]int{}); err != nil {
			return fmt.Errorf("clear supplementary groups (requires SETGID): %w", err)
		}
		if err = syscall.Setgid(gid); err != nil {
			return fmt.Errorf("set PGID (requires SETGID): %w", err)
		}
		if err = syscall.Setuid(uid); err != nil {
			return fmt.Errorf("set PUID (requires SETUID): %w", err)
		}
	} else {
		// Docker --user remains supported; it bypasses root initialization.
		if os.Getenv("PUID") != "" && uid != os.Geteuid() || os.Getenv("PGID") != "" && gid != os.Getegid() {
			return errors.New("PUID/PGID conflict with Docker --user; use matching IDs or omit PUID/PGID")
		}
	}
	log.Printf("ROS Watch runtime UID:GID %d:%d", os.Geteuid(), os.Getegid())
	return startApplication(dir, os.Args[1:])
}

func main() {
	if err := run(); err != nil {
		log.Print("ROS Watch container stopped: ", err)
		var status *exec.ExitError
		if errors.As(err, &status) && status.ExitCode() > 0 {
			os.Exit(status.ExitCode())
		}
		os.Exit(1)
	}
}
