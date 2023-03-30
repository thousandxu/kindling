package cpuanalyzer

import (
	"sync"

	"github.com/Kindling-project/kindling/collector/pkg/model"
)

func (ca *CpuAnalyzer) ProfileModule() (submodule string, start func() error, stop func() error) {
	return "cpuanalyzer", ca.StartProfile, ca.StopProfile
}

func (ca *CpuAnalyzer) StartProfile() error {
	// control flow changed
	// Note that these two variables belongs to the package
	metricsTriggerChan = make(chan *model.DataGroup, 3e5)
	profilingTriggerChan = make(chan SendTriggerEvent, 3e5)
	abnormalInnerCallChan = make(chan *model.DataGroup, 1e4)
	EnableProfile = true
	profilingOnce = sync.Once{}
	metricsOnce = sync.Once{}
	go ca.ReadProfilingTriggerChan()
	go ca.ReadMetricsTriggerChan()
	go ca.ReadTraceChan()
	return nil
}

func (ca *CpuAnalyzer) StopProfile() error {
	// control flow changed
	ca.lock.Lock()
	defer ca.lock.Unlock()
	EnableProfile = false
	// Clear the old events even if they are not sent
	ca.cpuPidEvents = make(map[uint32]map[uint32]*TimeSegments, 100000)
	return nil
}
