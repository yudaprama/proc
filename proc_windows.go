// +build windows

package proc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	pdh                            = syscall.NewLazyDLL("pdh.dll")
	winPdhOpenQuery                = pdh.NewProc("PdhOpenQuery")
	winPdhAddCounter               = pdh.NewProc("PdhAddCounterW")
	winPdhCollectQueryData         = pdh.NewProc("PdhCollectQueryData")
	winPdhGetFormattedCounterValue = pdh.NewProc("PdhGetFormattedCounterValue")
	winPdhGetFormattedCounterArray = pdh.NewProc("PdhGetFormattedCounterArrayW")
)

var (
	pcHandle                                       PDH_HQUERY
	pidCounter, cpuCounter, rssCounter, vssCounter PDH_HCOUNTER
	prevCPU                                        float64
	prevRss                                        int64
	prevVss                                        int64
	lastSampleTime                                 time.Time
	processPid                                     int
	pcQueryLock                                    sync.Mutex
	initialSample                                  = true
)

// maxQuerySize represents the maximum servers that can be queried
// simultaneously running on a machine.
const maxQuerySize = 512

// Keep static memory around to reuse
var counterResults [maxQuerySize]PDH_FMT_COUNTER_VALUE_ITEM_DOUBLE

// PDH Types
type (
	PDH_HQUERY   syscall.Handle
	PDH_HCOUNTER syscall.Handle
)

// PDH constants used here
const (
	PDH_FMT_DOUBLE   = 0x00000200
	PDH_INVALID_DATA = 0xC0000BC6
	PDH_MORE_DATA    = 0x800007D2
)

// PDH_FMT_COUNTER_VALUE_DOUBLE - double value
type PDH_FMT_COUNTER_VALUE_DOUBLE struct {
	CStatus     uint32
	DoubleValue float64
}

// PDH_FMT_COUNTER_VALUE_ITEM_DOUBLE need at least 1 addressable null ptr.
type PDH_FMT_COUNTER_VALUE_ITEM_DOUBLE struct {
	SzName   *uint16
	FmtValue PDH_FMT_COUNTER_VALUE_DOUBLE
}

func pdhAddCounter(hQuery PDH_HQUERY, szFullCounterPath string, dwUserData uintptr, phCounter *PDH_HCOUNTER) error {
	ptxt, _ := syscall.UTF16PtrFromString(szFullCounterPath)
	r0, _, _ := winPdhAddCounter.Call(
		uintptr(hQuery),
		uintptr(unsafe.Pointer(ptxt)),
		dwUserData,
		uintptr(unsafe.Pointer(phCounter)))

	if r0 != 0 {
		return fmt.Errorf("pdhAddCounter failed. %d", r0)
	}
	return nil
}

func pdhOpenQuery(datasrc *uint16, userdata uint32, query *PDH_HQUERY) error {
	r0, _, _ := syscall.Syscall(winPdhOpenQuery.Addr(), 3, 0, uintptr(userdata), uintptr(unsafe.Pointer(query)))
	if r0 != 0 {
		return fmt.Errorf("pdhOpenQuery failed - %d", r0)
	}
	return nil
}

func pdhCollectQueryData(hQuery PDH_HQUERY) error {
	r0, _, _ := winPdhCollectQueryData.Call(uintptr(hQuery))
	if r0 != 0 {
		return fmt.Errorf("pdhCollectQueryData failed - %d", r0)
	}
	return nil
}

func pdhGetFormattedCounterArrayDouble(hCounter PDH_HCOUNTER, lpdwBufferSize *uint32, lpdwBufferCount *uint32, itemBuffer *PDH_FMT_COUNTER_VALUE_ITEM_DOUBLE) uint32 {
	ret, _, _ := winPdhGetFormattedCounterArray.Call(
		uintptr(hCounter),
		uintptr(PDH_FMT_DOUBLE),
		uintptr(unsafe.Pointer(lpdwBufferSize)),
		uintptr(unsafe.Pointer(lpdwBufferCount)),
		uintptr(unsafe.Pointer(itemBuffer)))

	return uint32(ret)
}

func getCounterArrayData(counter PDH_HCOUNTER) ([]float64, error) {
	var bufSize uint32
	var bufCount uint32

	initialBuf := make([]PDH_FMT_COUNTER_VALUE_ITEM_DOUBLE, 1)
	ret := pdhGetFormattedCounterArrayDouble(counter, &bufSize, &bufCount, &initialBuf[0])
	if ret == PDH_MORE_DATA {
		if bufCount > maxQuerySize {
			bufCount = maxQuerySize
		}
		ret = pdhGetFormattedCounterArrayDouble(counter, &bufSize, &bufCount, &counterResults[0])
		if ret == 0 {
			rv := make([]float64, bufCount)
			for i := 0; i < int(bufCount); i++ {
				rv[i] = counterResults[i].FmtValue.DoubleValue
			}
			return rv, nil
		}
	}
	if ret != 0 {
		return nil, fmt.Errorf("getCounterArrayData failed - %d", ret)
	}

	return nil, nil
}

// getProcessExeName returns the name of the process image, as expected by
// the performance counter API.
func getProcessExeName() (name string) {
	name = filepath.Base(os.Args[0])
	name = strings.TrimRight(name, ".exe")
	return
}

// initialize counters
func initCounters() (err error) {

	processPid = os.Getpid()
	var source uint16
	if err := pdhOpenQuery(&source, 0, &pcHandle); err != nil {
		return err
	}

	name := fmt.Sprintf("%s*", getProcessExeName())
	pidQuery := fmt.Sprintf("\\Process(%s)\\ID Process", name)
	cpuQuery := fmt.Sprintf("\\Process(%s)\\%% Processor Time", name)
	rssQuery := fmt.Sprintf("\\Process(%s)\\Working Set - Private", name)
	vssQuery := fmt.Sprintf("\\Process(%s)\\Virtual Bytes", name)

	if err = pdhAddCounter(pcHandle, pidQuery, 0, &pidCounter); err != nil {
		return err
	}
	if err = pdhAddCounter(pcHandle, cpuQuery, 0, &cpuCounter); err != nil {
		return err
	}
	if err = pdhAddCounter(pcHandle, rssQuery, 0, &rssCounter); err != nil {
		return err
	}
	if err = pdhAddCounter(pcHandle, vssQuery, 0, &vssCounter); err != nil {
		return err
	}

	if err = pdhCollectQueryData(pcHandle); err != nil {
		return err
	}
	time.Sleep(50)

	return nil
}

// Usage returns process CPU and memory
func Usage(pcpu *float64, rss, vss *int64) error {
	var err error

	pcQueryLock.Lock()
	defer pcQueryLock.Unlock()

	if initialSample {
		if err = initCounters(); err != nil {
			return err
		}
		initialSample = false
	} else if time.Since(lastSampleTime) < (2 * time.Second) {
		*pcpu = prevCPU
		*rss = prevRss
		*vss = prevVss
		return nil
	}

	defer func() {
		lastSampleTime = time.Now()
	}()

	if err = pdhCollectQueryData(pcHandle); err != nil {
		return err
	}

	var pidAry, cpuAry, rssAry, vssAry []float64
	if pidAry, err = getCounterArrayData(pidCounter); err != nil {
		return err
	}
	if cpuAry, err = getCounterArrayData(cpuCounter); err != nil {
		return err
	}
	if rssAry, err = getCounterArrayData(rssCounter); err != nil {
		return err
	}
	if vssAry, err = getCounterArrayData(vssCounter); err != nil {
		return err
	}
	idx := int(-1)
	for i := range pidAry {
		if int(pidAry[i]) == processPid {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("could not find pid in performance counter results")
	}
	*pcpu = cpuAry[idx]
	*rss = int64(rssAry[idx])
	*vss = int64(vssAry[idx])

	prevCPU = *pcpu
	prevRss = *rss
	prevVss = *vss

	return nil
}
