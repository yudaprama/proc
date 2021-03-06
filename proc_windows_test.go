// +build windows

package proc

import (
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func checkValues(t *testing.T, pcpu, tPcpu float64, rss, tRss int64) {
	if pcpu != tPcpu {
		delta := int64(pcpu - tPcpu)
		if delta < 0 {
			delta = -delta
		}
		if delta > 30 { // 30%?
			t.Fatalf("CPUs did not match close enough: %f vs %f", pcpu, tPcpu)
		}
	}
	if rss != tRss {
		delta := rss - tRss
		if delta < 0 {
			delta = -delta
		}
		if delta > 1024*1024 { // 1MB
			t.Fatalf("RSSs did not match close enough: %d vs %d", rss, tRss)
		}
	}
}

func TestPSEmulationWin(t *testing.T) {
	var pcpu, tPcpu float64
	var rss, vss, tRss int64

	runtime.GC()

	if err := Usage(&pcpu, &rss, &vss); err != nil {
		t.Fatalf("Error:  %v", err)
	}

	runtime.GC()

	imageName := getProcessImageName()
	out, err := exec.Command("typeperf.exe",
		fmt.Sprintf("\\Process(%s)\\%% Processor Time", imageName),
		fmt.Sprintf("\\Process(%s)\\Working Set - Private", imageName),
		fmt.Sprintf("\\Process(%s)\\Virtual Bytes", imageName),
		"-sc", "1").Output()
	if err != nil {
		t.Fatal("unable to run command", err)
	}

	results := strings.Split(string(out), "\r\n")
	values := strings.Split(results[2], ",")

	tPcpu, err = strconv.ParseFloat(strings.Trim(values[1], "\""), 64)
	if err != nil {
		t.Fatalf("Unable to parse percent cpu: %s", values[1])
	}

	fval, err := strconv.ParseFloat(strings.Trim(values[2], "\""), 64)
	if err != nil {
		t.Fatalf("Unable to parse private bytes: %s", values[2])
	}
	tRss = int64(fval)

	checkValues(t, pcpu, tPcpu, rss, tRss)

	runtime.GC()

	if err = Usage(&pcpu, &rss, &vss); err != nil {
		t.Fatalf("Error:  %v", err)
	}
	checkValues(t, pcpu, tPcpu, rss, tRss)
}
