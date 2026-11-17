package probe

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func readLoadavg(path string) (*CPU, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := strings.Fields(string(data))
	if len(f) < 3 {
		return nil, fmt.Errorf("%s: unexpected format", path)
	}
	var vals [3]float64
	for i := range vals {
		if vals[i], err = strconv.ParseFloat(f[i], 64); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return &CPU{Load1: vals[0], Load5: vals[1], Load15: vals[2]}, nil
}

type cpuTimes struct{ idle, total uint64 }

// readStat parses the aggregate "cpu" line of /proc/stat.
func readStat(path string) (cpuTimes, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return cpuTimes{}, err
	}
	line, _, _ := bytes.Cut(data, []byte("\n"))
	f := strings.Fields(string(line))
	if len(f) < 5 || f[0] != "cpu" {
		return cpuTimes{}, fmt.Errorf("%s: unexpected format", path)
	}
	var t cpuTimes
	for i, s := range f[1:] {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return cpuTimes{}, fmt.Errorf("%s: %w", path, err)
		}
		// Fields: user nice system idle iowait irq softirq steal guest guest_nice.
		// guest and guest_nice are already included in user and nice.
		if i >= 8 {
			break
		}
		t.total += v
		if i == 3 || i == 4 { // idle + iowait
			t.idle += v
		}
	}
	return t, nil
}

func usage(prev, cur cpuTimes) (float64, bool) {
	if cur.total <= prev.total || cur.idle < prev.idle {
		return 0, false
	}
	total := float64(cur.total - prev.total)
	idle := float64(cur.idle - prev.idle)
	return (total - idle) / total, true
}

func readMeminfo(path string) (*Memory, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var m Memory
	var haveTotal, haveAvail bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			m.TotalBytes, haveTotal = kb*1024, true
		case "MemAvailable":
			m.AvailableBytes, haveAvail = kb*1024, true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !haveTotal || !haveAvail {
		return nil, fmt.Errorf("%s: MemTotal/MemAvailable not found", path)
	}
	return &m, nil
}
