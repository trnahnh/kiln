package fault

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// PIDByContainerID finds a host process running inside the container, by the container
// runtime id kubelet reports, from the cgroup every process in the container belongs to.
// The lowest PID is the container's own init.
func PIDByContainerID(procRoot, containerID string) (int, error) {
	id := strings.TrimPrefix(strings.TrimPrefix(containerID, "containerd://"), "cri-o://")
	if id == "" {
		return 0, fmt.Errorf("empty container id")
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return 0, err
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	for _, pid := range pids {
		path, err := CgroupPath(procRoot, pid)
		if err != nil {
			continue
		}
		if strings.Contains(path, id) {
			return pid, nil
		}
	}
	return 0, fmt.Errorf("no process on this node belongs to container %s", id)
}

// CgroupPath is the unified-hierarchy path of a process, relative to the cgroup root of
// whoever reads it.
func CgroupPath(procRoot string, pid int) (string, error) {
	f, err := os.Open(filepath.Join(procRoot, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "0::"); ok {
			return rest, nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("pid %d has no cgroup v2 entry", pid)
}

// Cmdline of a process, arguments joined by spaces; empty when the process is gone.
func Cmdline(procRoot string, pid int) string {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
}
