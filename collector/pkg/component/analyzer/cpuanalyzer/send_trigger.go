package cpuanalyzer

import (
	"strconv"
	"sync"
	"time"

	"github.com/Kindling-project/kindling/collector/pkg/filepathhelper"
	"github.com/Kindling-project/kindling/collector/pkg/model"
	"github.com/Kindling-project/kindling/collector/pkg/model/constlabels"
	"github.com/Kindling-project/kindling/collector/pkg/model/constnames"
	"github.com/Kindling-project/kindling/collector/pkg/model/constvalues"
)

var (
	EnableProfile         bool
	profilingOnce         sync.Once
	metricsOnce           sync.Once
	metricsTriggerChan    chan *model.DataGroup
	profilingTriggerChan  chan SendTriggerEvent
	abnormalInnerCallChan chan *model.DataGroup
	sampleMap             sync.Map
	isInstallApm          map[uint64]bool
)

func init() {
	isInstallApm = make(map[uint64]bool, 100000)
}

func ReceiveProfilingSignal(data *model.DataGroup) {
	if !EnableProfile {
		profilingOnce.Do(func() {
			// We must close the channel at the sender-side.
			// Otherwise, we need complex codes to handle it.
			if profilingTriggerChan != nil {
				close(profilingTriggerChan)
			}
		})
		return
	}
	if duration, ok := data.GetMetric(constvalues.RequestTotalTime); ok {
		event := SendTriggerEvent{
			Pid:              uint32(data.Labels.GetIntValue("pid")),
			StartTime:        data.Timestamp,
			SpendTime:        uint64(duration.GetInt().Value),
			OriginalData:     data,
			TraceIdProfiling: true,
		}
		profilingTriggerChan <- event
	}
}

// ReceiveDataGroupAsSignal receives model.DataGroup as a signal.
// Signal is used to trigger to send CPU on/off events
func ReceiveDataGroupAsSignal(data *model.DataGroup) {
	if !EnableProfile {
		metricsOnce.Do(func() {
			// We must close the channel at the sender-side.
			// Otherwise, we need complex codes to handle it.
			if metricsTriggerChan != nil {
				close(metricsTriggerChan)
			}
			if abnormalInnerCallChan != nil {
				close(abnormalInnerCallChan)
			}
		})
		return
	}
	if data.Name == constnames.SpanTraceGroupName {
		isInstallApm[uint64(data.Labels.GetIntValue("pid"))] = true
		// Trigger the metric extraction using the key thread
		metricsTriggerChan <- data.Clone()
	} else {
		// Sampling the abnormal profiling
		if !data.Labels.GetBoolValue(constlabels.IsSlow) {
			return
		}
		// Clone the data for further usage. Otherwise, it will be reused and loss fields.
		trace := data.Clone()
		// CpuAnalyzer consumes all traces from the client-side to add them to TimeSegments for data enrichment.
		// Now we don't store the trace from the server-side due to the storage concern.
		if !trace.Labels.GetBoolValue(constlabels.IsServer) {
			abnormalInnerCallChan <- trace
			// The trace sent from the client-side won't be treated as trigger event, so we just return here.
			return
		}
		// If the data is not from APM while there have been APM traces received, we don't make it as a signal.
		if !isInstallApm[uint64(data.Labels.GetIntValue("pid"))] {
			// We save the trace to sampleMap to make it as the sending trigger event.
			pidString := strconv.FormatInt(data.Labels.GetIntValue("pid"), 10)
			sampleMap.LoadOrStore(data.Labels.GetStringValue(constlabels.ContentKey)+pidString, trace)
		}
	}
}

type SendTriggerEvent struct {
	Pid              uint32           `json:"pid"`
	StartTime        uint64           `json:"startTime"`
	SpendTime        uint64           `json:"spendTime"`
	OriginalData     *model.DataGroup `json:"originalData"`
	TraceIdProfiling bool
}

// ReadTraceChan reads the trace channel and make cpuanalyzer consume them as general events.
func (ca *CpuAnalyzer) ReadTraceChan() {
	// Break the for loop if the channel is closed
	for trace := range abnormalInnerCallChan {
		ca.ConsumeTraces(trace)
	}
}

func (ca *CpuAnalyzer) ReadMetricsTriggerChan() {
	// Break the for loop if the channel is closed
	for trigger := range metricsTriggerChan {
		pid := trigger.Labels.GetIntValue(constlabels.Pid)
		tid := trigger.Labels.GetIntValue(constlabels.Tid)
		startTime := trigger.Timestamp
		endTime := trigger.Labels.GetIntValue(constlabels.EndTime)
		aggregatedTime := ca.GetTimes(uint32(pid), uint32(tid), startTime, uint64(endTime))
		ca.telemetry.Logger.Infof("Receive the metrics trigger event: pid=%d, tid=%d, startTime=%d, endTime=%d."+
			"The aggregated time is: %v", pid, tid, startTime, endTime, aggregatedTime)
		// metrics composite
		metrics := []*model.Metric{
			model.NewIntMetric(constnames.ProfilingCpuDurationMetric, int64(aggregatedTime.times[0])),
			model.NewIntMetric(constnames.ProfilingFileDurationMetric, int64(aggregatedTime.times[1])),
			model.NewIntMetric(constnames.ProfilingNetDurationMetric, int64(aggregatedTime.times[2])),
			model.NewIntMetric(constnames.ProfilingFutexDurationMetric, int64(aggregatedTime.times[3])),
		}
		// Ignore the original metrics
		trigger.Metrics = metrics
		trigger.Name = constnames.ProfilingMetricsGroupName
		// Aggregate the metrics
		for _, nexConsumer := range ca.nextConsumers {
			_ = nexConsumer.Consume(trigger)
		}
	}
}

// ReadProfilingTriggerChan reads the triggerEvent channel and creates tasks to send cpuEvents.
func (ca *CpuAnalyzer) ReadProfilingTriggerChan() {
	// Break the for loop if the channel is closed
	for sendContent := range profilingTriggerChan {
		profiling := false
		if sendContent.OriginalData.Labels.GetBoolValue(constlabels.IsSlow) {
			profiling = true
		} else if sendContent.OriginalData.Labels.GetBoolValue(constlabels.IsError) && ca.cfg.ProfilingError {
			profiling = true
		}
		if !profiling {
			continue
		}
		if !sendContent.TraceIdProfiling {
			// Store the Network Traces
			for _, nexConsumer := range ca.nextConsumers {
				_ = nexConsumer.Consume(sendContent.OriginalData)
			}
		}
		// Copy the value and then get its pointer to create a new task
		triggerEvent := sendContent
		task := &SendEventsTask{
			cpuAnalyzer:              ca,
			triggerEvent:             &triggerEvent,
			edgeEventsWindowDuration: time.Duration(ca.cfg.EdgeEventsWindowSize) * time.Second,
		}
		expiredCallback := func() {
			ca.routineSize.Dec()
		}
		// The expired duration should be windowDuration+1 because the ticker and the timer are not started together.
		NewAndStartScheduledTaskRoutine(1*time.Second, time.Duration(ca.cfg.EdgeEventsWindowSize)*time.Second+1, task, expiredCallback)
		ca.routineSize.Inc()
	}
}

type SendEventsTask struct {
	tickerCount              int
	cpuAnalyzer              *CpuAnalyzer
	triggerEvent             *SendTriggerEvent
	edgeEventsWindowDuration time.Duration
}

// |________________|______________|_________________|
// 0  (edgeWindow)  1  (duration)  2  (edgeWindow)   3
// 0: The start time of the windows where the events we need are.
// 1: The start time of the "trace".
// 2: The end time of the "trace". This is nearly equal to the creating time of the task.
// 3: The end time of the windows where the events we need are.
func (t *SendEventsTask) run() {
	currentWindowsStartTime := uint64(t.tickerCount)*uint64(time.Second) + t.triggerEvent.StartTime - uint64(t.edgeEventsWindowDuration)
	currentWindowsEndTime := uint64(t.tickerCount)*uint64(time.Second) + t.triggerEvent.StartTime + t.triggerEvent.SpendTime
	t.tickerCount++
	// keyElements are used to correlate the cpuEvents with the trace.
	keyElements := filepathhelper.GetFilePathElements(t.triggerEvent.OriginalData, t.triggerEvent.StartTime)
	t.cpuAnalyzer.sendEvents(keyElements.ToAttributes(), t.triggerEvent.Pid, currentWindowsStartTime, currentWindowsEndTime)
}

func (ca *CpuAnalyzer) sampleSend() {
	timer := time.NewTicker(time.Duration(ca.cfg.SamplingInterval) * time.Second)
	for {
		select {
		case <-timer.C:
			// TODO More complicated sampling method
			sampleMap.Range(func(k, v interface{}) bool {
				data := v.(*model.DataGroup)
				duration, ok := data.GetMetric(constvalues.RequestTotalTime)
				if !ok {
					return false
				}
				event := SendTriggerEvent{
					Pid:              uint32(data.Labels.GetIntValue("pid")),
					StartTime:        data.Timestamp,
					SpendTime:        uint64(duration.GetInt().Value),
					OriginalData:     data,
					TraceIdProfiling: false,
				}
				profilingTriggerChan <- event
				sampleMap.Delete(k)
				return true
			})
		}
	}
}

func (ca *CpuAnalyzer) sendEvents(keyElements *model.AttributeMap, pid uint32, startTime uint64, endTime uint64) {
	ca.lock.RLock()
	defer ca.lock.RUnlock()

	maxSegmentSize := ca.cfg.SegmentSize
	tidCpuEvents, exist := ca.cpuPidEvents[pid]
	if !exist {
		ca.telemetry.Logger.Infof("Not found the cpu events with the pid=%d, startTime=%d, endTime=%d",
			pid, startTime, endTime)
		return
	}
	startTimeSecond := startTime / nanoToSeconds
	endTimeSecond := endTime / nanoToSeconds

	for _, timeSegments := range tidCpuEvents {
		if endTimeSecond < timeSegments.BaseTime || startTimeSecond > timeSegments.BaseTime+uint64(maxSegmentSize) {
			ca.telemetry.Logger.Infof("pid=%d tid=%d events are beyond the time windows. BaseTimeSecond=%d, "+
				"startTimeSecond=%d, endTimeSecond=%d", pid, timeSegments.Tid, timeSegments.BaseTime, startTimeSecond, endTimeSecond)
			continue
		}
		startIndex := int(startTimeSecond - timeSegments.BaseTime)
		if startIndex < 0 {
			startIndex = 0
		}
		endIndex := endTimeSecond - timeSegments.BaseTime
		if endIndex > timeSegments.BaseTime+uint64(maxSegmentSize) {
			endIndex = timeSegments.BaseTime + uint64(maxSegmentSize)
		}
		ca.telemetry.Logger.Infof("pid=%d tid=%d sends events. startSecond=%d, endSecond=%d",
			pid, timeSegments.Tid, startTimeSecond, endTimeSecond)
		for i := startIndex; i <= int(endIndex) && i < maxSegmentSize; i++ {
			val := timeSegments.Segments.GetByIndex(i)
			if val == nil {
				continue
			}
			segment := val.(*Segment)
			if len(segment.CpuEvents) != 0 {
				// Don't remove the duplicated one
				segment.IndexTimestamp = time.Now().String()
				dataGroup := segment.toDataGroup(timeSegments)
				dataGroup.Labels.Merge(keyElements)
				for _, nexConsumer := range ca.nextConsumers {
					_ = nexConsumer.Consume(dataGroup)
				}
				segment.IsSend = 1
			}
		}
	}
}
