package main

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// procStat reads the fields of /proc/<pid>/stat that loods needs.
// The command name in parentheses may contain spaces, so parse after the last ')'.
func procStat(pid int) (session int, startTicks uint64, ok bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0, false
	}
	f := strings.Fields(s[i+1:])
	// f[0]=state f[1]=ppid f[2]=pgrp f[3]=session … f[19]=starttime
	if len(f) < 20 {
		return 0, 0, false
	}
	session, _ = strconv.Atoi(f[3])
	startTicks, _ = strconv.ParseUint(f[19], 10, 64)
	return session, startTicks, true
}

// sessionPids lists every process in the session led by sid. Each Garage
// process starts its own session (pty), so this is its whole process tree,
// including children that daemonised into their own process group.
func sessionPids(sid int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if s, _, ok := procStat(pid); ok && s == sid {
			out = append(out, pid)
		}
	}
	return out
}

var pageSize = int64(os.Getpagesize())

func rssBytes(pids []int) int64 {
	var total int64
	for _, pid := range pids {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/statm")
		if err != nil {
			continue
		}
		if f := strings.Fields(string(b)); len(f) > 1 {
			n, _ := strconv.ParseInt(f[1], 10, 64)
			total += n * pageSize
		}
	}
	return total
}

// listeningPorts finds TCP ports in LISTEN state owned by any of pids.
func listeningPorts(pids []int) []int {
	inodes := map[string]bool{}
	for _, pid := range pids {
		fdDir := "/proc/" + strconv.Itoa(pid) + "/fd"
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err == nil && strings.HasPrefix(link, "socket:[") {
				inodes[link[8:len(link)-1]] = true
			}
		}
	}
	if len(inodes) == 0 {
		return nil
	}
	var ports []int
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		f, err := os.Open(table)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Scan() // header
		for sc.Scan() {
			// sl local_address rem_address st tx:rx tr:when retrnsmt uid timeout inode
			fl := strings.Fields(sc.Text())
			if len(fl) < 10 || fl[3] != "0A" || !inodes[fl[9]] {
				continue
			}
			_, hexPort, _ := strings.Cut(fl[1], ":")
			if p, err := strconv.ParseInt(hexPort, 16, 32); err == nil && !slices.Contains(ports, int(p)) {
				ports = append(ports, int(p))
			}
		}
		f.Close()
	}
	slices.Sort(ports)
	return ports
}

// memInfo returns total and available system memory in bytes.
func memInfo() (total, available int64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), ":")
		n, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
		switch k {
		case "MemTotal":
			total = n * 1024
		case "MemAvailable":
			available = n * 1024
		}
	}
	return total, available
}
