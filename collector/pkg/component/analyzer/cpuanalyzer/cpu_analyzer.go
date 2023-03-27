package cpuanalyzer

import (
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/Kindling-project/kindling/collector/pkg/metadata/kubernetes"
	"github.com/Kindling-project/kindling/collector/pkg/model/constlabels"
	"github.com/Kindling-project/kindling/collector/pkg/model/constvalues"
	"google.golang.org/grpc"

	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/Kindling-project/kindling/collector/pkg/component"
	"github.com/Kindling-project/kindling/collector/pkg/component/analyzer"
	"github.com/Kindling-project/kindling/collector/pkg/component/consumer"
	"github.com/Kindling-project/kindling/collector/pkg/model"
	"github.com/Kindling-project/kindling/collector/pkg/model/constnames"
)

const (
	CpuProfile analyzer.Type = "cpuanalyzer"
)

type CpuAnalyzer struct {
	cfg          *Config
	cpuPidEvents map[uint32]map[uint32]*TimeSegments
	// { pid: routine }
	sendEventsRoutineMap sync.Map
	routineSize          *atomic.Int32
	lock                 sync.RWMutex
	telemetry            *component.TelemetryTools
	tidExpiredQueue      *tidDeleteQueue
	javaTraces           map[string]*TransactionIdEvent
	nextConsumers        []consumer.Consumer
	sampleCache          *SampleCache
}

func (ca *CpuAnalyzer) Type() analyzer.Type {
	return CpuProfile
}

func (ca *CpuAnalyzer) ConsumableEvents() []string {
	return []string{constnames.CpuEvent, constnames.JavaFutexInfo, constnames.TransactionIdEvent, constnames.ProcessExitEvent, constnames.SpanEvent}
}

func NewCpuAnalyzer(cfg interface{}, telemetry *component.TelemetryTools, consumers []consumer.Consumer) analyzer.Analyzer {
	config, _ := cfg.(*Config)

	conn, err := grpc.Dial(config.ReceiverIp+":"+strconv.Itoa(config.ReceiverPort), grpc.WithInsecure())
	if err != nil {
		if ce := telemetry.Logger.Check(zapcore.WarnLevel, "Fail to Create GrpcClient: "); ce != nil {
			ce.Write(
				zap.String("ip", config.ReceiverIp),
				zap.Error(err),
			)
		}
		return nil
	}

	ca := &CpuAnalyzer{
		cfg:           config,
		telemetry:     telemetry,
		nextConsumers: consumers,
		routineSize:   atomic.NewInt32(0),
		sampleCache:   NewSampleCache(model.NewTraceIdServiceClient(conn), config.JavaTraceSlowTime, config.SampleTraceWaitTime, config.SampleUrlHitDuration, kubernetes.MetaDataCache),
	}
	ca.cpuPidEvents = make(map[uint32]map[uint32]*TimeSegments, 100000)
	ca.tidExpiredQueue = newTidDeleteQueue()
	ca.javaTraces = make(map[string]*TransactionIdEvent, 100000)
	go ca.TidDelete(30*time.Second, 10*time.Second)
	go ca.sampleSend()
	go ca.sampleCache.loopCheckTailBaseTraces()
	go ca.sampleCache.loopSendAndRecvTraces()
	newSelfMetrics(telemetry.MeterProvider, ca)
	return ca
}

func (ca *CpuAnalyzer) Start() error {
	// Disable receiving and sending the profiling data by default.
	return nil
}

func (ca *CpuAnalyzer) Shutdown() error {
	_ = ca.StopProfile()
	return nil
}

func (ca *CpuAnalyzer) ConsumeEvent(event *model.KindlingEvent) error {
	if !enableProfile {
		return nil
	}
	switch event.Name {
	case constnames.CpuEvent:
		ca.ConsumeCpuEvent(event)
	case constnames.JavaFutexInfo:
		ca.ConsumeJavaFutexEvent(event)
	case constnames.TransactionIdEvent:
		ca.ConsumeTransactionIdEvent(event)
	case constnames.ProcessExitEvent:
		pid := event.GetPid()
		tid := event.Ctx.ThreadInfo.GetTid()
		ca.trimExitedThread(pid, tid)
	case constnames.SpanEvent:
		ca.ConsumeSpanEvent(event)
	}
	return nil
}

func (ca *CpuAnalyzer) ConsumeTransactionIdEvent(event *model.KindlingEvent) {
	isEntry, _ := strconv.ParseUint(event.GetStringUserAttribute("is_enter"), 10, 32)
	var ev *TransactionIdEvent
	if isEntry == 1 {
		ev = &TransactionIdEvent{
			Timestamp:   event.Timestamp,
			TraceId:     event.GetStringUserAttribute("trace_id"),
			IsEntry:     1,
			Protocol:    event.GetStringUserAttribute("protocol"),
			Url:         event.GetStringUserAttribute("url"),
			ApmType:     event.GetStringUserAttribute("apm_type"),
			PidString:   strconv.FormatUint(uint64(event.GetPid()), 10),
			ContainerId: event.GetContainerId(),
		}
	} else {
		threadType, _ := strconv.ParseInt(event.GetStringUserAttribute("thread_type"), 10, 0)
		error, _ := strconv.ParseInt(event.GetStringUserAttribute("error"), 10, 0)
		ev = &TransactionIdEvent{
			Timestamp:  event.Timestamp,
			TraceId:    event.GetStringUserAttribute("trace_id"),
			IsEntry:    0,
			ThreadType: int(threadType),
			Error:      int(error),
		}
	}
	//ca.sendEventDirectly(event.GetPid(), event.Ctx.ThreadInfo.GetTid(), event.Ctx.ThreadInfo.Comm, ev)
	ca.PutEventToSegments(event.GetPid(), event.Ctx.ThreadInfo.GetTid(), event.Ctx.ThreadInfo.Comm, ev)
	if ca.cfg.OpenJavaTraceSampling {
		ca.analyzerJavaTraceTime(ev)
	}
}

func (ca *CpuAnalyzer) analyzerJavaTraceTime(ev *TransactionIdEvent) {
	if ev.IsEntry == 1 {
		ca.javaTraces[ev.TraceId+ev.PidString] = ev
	} else {
		oldEvent := ca.javaTraces[ev.TraceId+ev.PidString]
		if oldEvent == nil {
			return
		}
		if oldEvent.ThreadType > 0 {
			// 丢弃非业务线程数据.
			return
		}

		sampleTrace := NewSampleTrace(oldEvent, ev, ca.cfg.SampleTraceRepeatNum)
		if ca.sampleCache.isSampled(sampleTrace) {
			// 保存Trace 和 Profiling
			ca.sampleCache.storeProfiling(sampleTrace)
			ca.sampleCache.storeTrace(sampleTrace)
		} else if ca.sampleCache.isTailBaseSampled(sampleTrace) {
			// 只保留Trace数据
			ca.sampleCache.storeTrace(sampleTrace)
		} else {
			// 非错 或 慢 或 URL5s内已采中, 将数据存储到SampleCache中
			ca.sampleCache.cacheSampleTrace(sampleTrace)
		}
	}
}

func (ca *CpuAnalyzer) ConsumeJavaFutexEvent(event *model.KindlingEvent) {
	ev := new(JavaFutexEvent)
	ev.StartTime = event.Timestamp
	for i := 0; i < int(event.ParamsNumber); i++ {
		userAttributes := event.UserAttributes[i]
		switch {
		case userAttributes.GetKey() == "end_time":
			ev.EndTime, _ = strconv.ParseUint(string(userAttributes.GetValue()), 10, 64)
		case userAttributes.GetKey() == "data":
			ev.DataVal = string(userAttributes.GetValue())
		}
	}
	//ca.sendEventDirectly(event.GetPid(), event.Ctx.ThreadInfo.GetTid(), event.Ctx.ThreadInfo.Comm, ev)
	ca.PutEventToSegments(event.GetPid(), event.Ctx.ThreadInfo.GetTid(), event.Ctx.ThreadInfo.Comm, ev)
}

func (ca *CpuAnalyzer) ConsumeSpanEvent(event *model.KindlingEvent) {
	ev := new(ApmSpanEvent)
	ev.StartTime = event.Timestamp
	for i := 0; i < int(event.ParamsNumber); i++ {
		userAttributes := event.UserAttributes[i]
		switch {
		case userAttributes.GetKey() == "end_time":
			ev.EndTime, _ = strconv.ParseUint(string(userAttributes.GetValue()), 10, 64)
		case userAttributes.GetKey() == "trace_id":
			ev.TraceId = string(userAttributes.GetValue())
		case userAttributes.GetKey() == "span":
			ev.Name = string(userAttributes.GetValue())
		}
	}
	ca.PutEventToSegments(event.GetPid(), event.Ctx.ThreadInfo.GetTid(), event.Ctx.ThreadInfo.Comm, ev)
}

func (ca *CpuAnalyzer) ConsumeTraces(trace *model.DataGroup) {
	pid := trace.Labels.GetIntValue("pid")
	tid := trace.Labels.GetIntValue(constlabels.RequestTid)
	threadName := trace.Labels.GetStringValue(constlabels.Comm)
	duration, ok := trace.GetMetric(constvalues.RequestTotalTime)
	if !ok {
		ca.telemetry.Logger.Warnf("No request_total_time in the trace, pid=%d, threadName=%s", pid, threadName)
		return
	}
	event := &InnerCall{
		StartTime: trace.Timestamp,
		EndTime:   trace.Timestamp + uint64(duration.GetInt().Value),
		Trace:     trace,
	}
	ca.PutEventToSegments(uint32(pid), uint32(tid), threadName, event)
}

func (ca *CpuAnalyzer) ConsumeCpuEvent(event *model.KindlingEvent) {
	ev := new(CpuEvent)
	for i := 0; i < int(event.ParamsNumber); i++ {
		userAttributes := event.UserAttributes[i]
		switch {
		case userAttributes.GetKey() == "start_time":
			ev.StartTime = userAttributes.GetUintValue()
		case event.UserAttributes[i].GetKey() == "end_time":
			ev.EndTime = userAttributes.GetUintValue()
		case event.UserAttributes[i].GetKey() == "type_specs":
			ev.TypeSpecs = string(userAttributes.GetValue())
		case event.UserAttributes[i].GetKey() == "runq_latency":
			ev.RunqLatency = string(userAttributes.GetValue())
		case event.UserAttributes[i].GetKey() == "time_type":
			ev.TimeType = string(userAttributes.GetValue())
		case event.UserAttributes[i].GetKey() == "on_info":
			ev.OnInfo = string(userAttributes.GetValue())
		case event.UserAttributes[i].GetKey() == "off_info":
			ev.OffInfo = string(userAttributes.GetValue())
		case event.UserAttributes[i].GetKey() == "log":
			ev.Log = string(userAttributes.GetValue())
		case event.UserAttributes[i].GetKey() == "stack":
			ev.Stack = string(userAttributes.GetValue())
		}
	}
	if ce := ca.telemetry.Logger.Check(zapcore.DebugLevel, ""); ce != nil {
		ca.telemetry.Logger.Debug(fmt.Sprintf("Receive CpuEvent: pid=%d, tid=%d, comm=%s, %+v", event.GetPid(),
			event.Ctx.ThreadInfo.GetTid(), event.Ctx.ThreadInfo.Comm, ev))
	}
	//ca.sendEventDirectly(event.GetPid(), event.Ctx.ThreadInfo.GetTid(), event.Ctx.ThreadInfo.Comm, ev)
	ca.PutEventToSegments(event.GetPid(), event.Ctx.ThreadInfo.GetTid(), event.Ctx.ThreadInfo.Comm, ev)
}

var nanoToSeconds uint64 = 1e9

func (ca *CpuAnalyzer) PutEventToSegments(pid uint32, tid uint32, threadName string, event TimedEvent) {
	ca.lock.Lock()
	defer ca.lock.Unlock()
	tidCpuEvents, exist := ca.cpuPidEvents[pid]
	if !exist {
		tidCpuEvents = make(map[uint32]*TimeSegments)
		ca.cpuPidEvents[pid] = tidCpuEvents
	}
	timeSegments, exist := tidCpuEvents[tid]
	maxSegmentSize := ca.cfg.SegmentSize
	if exist {
		endOffset := int(event.EndTimestamp()/nanoToSeconds - timeSegments.BaseTime)
		if endOffset < 0 {
			ca.telemetry.Logger.Debugf("EndOffset of the event is negative. EndTimestamp=%d, BaseTime=%d",
				event.EndTimestamp(), timeSegments.BaseTime)
			return
		}
		startOffset := int(event.StartTimestamp()/nanoToSeconds - timeSegments.BaseTime)
		if startOffset < 0 {
			startOffset = 0
		}
		// If the timeSegment is full, we clear half of its elements.
		// Note the offset will be times of maxSegmentSize when no events with this tid come for long time,
		// so the timeSegment will be cleared multiple times until it can accommodate the events.
		if startOffset >= maxSegmentSize || endOffset > maxSegmentSize {
			if startOffset*2 >= 3*maxSegmentSize {
				// clear all elements
				ca.telemetry.Logger.Debugf("pid=%d, tid=%d, comm=%s, reset BaseTime from %d to %d", pid, tid,
					threadName, timeSegments.BaseTime, event.StartTimestamp()/nanoToSeconds)
				timeSegments.Segments.Clear()
				timeSegments.BaseTime = event.StartTimestamp() / nanoToSeconds
				endOffset = endOffset - startOffset
				startOffset = 0
				for i := 0; i < maxSegmentSize; i++ {
					segment := newSegment((timeSegments.BaseTime+uint64(i))*nanoToSeconds,
						(timeSegments.BaseTime+uint64(i+1))*nanoToSeconds)
					timeSegments.Segments.UpdateByIndex(i, segment)
				}
			} else {
				// Clear half of the elements
				clearSize := maxSegmentSize / 2
				ca.telemetry.Logger.Debugf("pid=%d, tid=%d, comm=%s, update BaseTime from %d to %d", pid, tid,
					threadName, timeSegments.BaseTime, timeSegments.BaseTime+uint64(clearSize))
				timeSegments.BaseTime = timeSegments.BaseTime + uint64(clearSize)
				startOffset -= clearSize
				if startOffset < 0 {
					startOffset = 0
				}
				endOffset -= clearSize
				for i := 0; i < clearSize; i++ {
					movedIndex := i + clearSize
					val := timeSegments.Segments.GetByIndex(movedIndex)
					timeSegments.Segments.UpdateByIndex(i, val)
					segmentTmp := newSegment((timeSegments.BaseTime+uint64(movedIndex))*nanoToSeconds,
						(timeSegments.BaseTime+uint64(movedIndex+1))*nanoToSeconds)
					timeSegments.Segments.UpdateByIndex(movedIndex, segmentTmp)
				}
			}
		}
		// Update the thread name immediately
		timeSegments.updateThreadName(threadName)
		for i := startOffset; i <= endOffset && i < maxSegmentSize; i++ {
			val := timeSegments.Segments.GetByIndex(i)
			segment := val.(*Segment)
			segment.putTimedEvent(event)
			segment.IsSend = 0
			timeSegments.Segments.UpdateByIndex(i, segment)
		}

	} else {
		newTimeSegments := &TimeSegments{
			Pid:        pid,
			Tid:        tid,
			ThreadName: threadName,
			BaseTime:   event.StartTimestamp() / nanoToSeconds,
			Segments:   NewCircleQueue(maxSegmentSize),
		}
		for i := 0; i < maxSegmentSize; i++ {
			segment := newSegment((newTimeSegments.BaseTime+uint64(i))*nanoToSeconds,
				(newTimeSegments.BaseTime+uint64(i+1))*nanoToSeconds)
			newTimeSegments.Segments.UpdateByIndex(i, segment)
		}

		endOffset := int(event.EndTimestamp()/nanoToSeconds - newTimeSegments.BaseTime)

		for i := 0; i <= endOffset && i < maxSegmentSize; i++ {
			val := newTimeSegments.Segments.GetByIndex(i)
			segment := val.(*Segment)
			segment.putTimedEvent(event)
			segment.IsSend = 0
		}

		tidCpuEvents[tid] = newTimeSegments
	}
}

func (ca *CpuAnalyzer) trimExitedThread(pid uint32, tid uint32) {
	ca.tidExpiredQueue.queueMutex.Lock()
	defer ca.tidExpiredQueue.queueMutex.Unlock()
	ca.telemetry.Logger.Debugf("Receive a procexit pid=%d, tid=%d, which will be deleted from map after 10 seconds. ", pid, tid)

	cacheElem := deleteTid{pid: pid, tid: tid, exitTime: time.Now()}
	ca.tidExpiredQueue.Push(cacheElem)
}
