package traceidanalyzer

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Kindling-project/kindling/collector/pkg/component"
	"github.com/Kindling-project/kindling/collector/pkg/component/analyzer"
	"github.com/Kindling-project/kindling/collector/pkg/component/analyzer/cpuanalyzer"
	"github.com/Kindling-project/kindling/collector/pkg/component/consumer"
	"github.com/Kindling-project/kindling/collector/pkg/metadata/kubernetes"
	"github.com/Kindling-project/kindling/collector/pkg/model"
	"github.com/Kindling-project/kindling/collector/pkg/model/constlabels"
	"github.com/Kindling-project/kindling/collector/pkg/model/constnames"
	"github.com/Kindling-project/kindling/collector/pkg/model/constvalues"
)

const (
	TraceId analyzer.Type = "traceidanalyzer"
)

type TraceIdAnalyzer struct {
	cfg           *Config
	telemetry     *component.TelemetryTools
	javaTraces    sync.Map
	nextConsumers []consumer.Consumer
	metadata      *kubernetes.K8sMetaDataCache
}

func (ta *TraceIdAnalyzer) Type() analyzer.Type {
	return TraceId
}

func (ta *TraceIdAnalyzer) ConsumableEvents() []string {
	return []string{constnames.TransactionIdEvent}
}

func NewTraceIdAnalyzer(cfg interface{}, telemetry *component.TelemetryTools, consumers []consumer.Consumer) analyzer.Analyzer {
	config, _ := cfg.(*Config)
	return &TraceIdAnalyzer{
		cfg:           config,
		telemetry:     telemetry,
		nextConsumers: consumers,
		metadata:      kubernetes.MetaDataCache,
	}
}

func (ta *TraceIdAnalyzer) Start() error {
	go ta.cleanExpiredTraces()
	return nil
}

func (ta *TraceIdAnalyzer) Shutdown() error {
	return nil
}

func (ta *TraceIdAnalyzer) ConsumeEvent(event *model.KindlingEvent) error {
	if !cpuanalyzer.EnableProfile || !ta.cfg.OpenJavaTraceSampling {
		// Skip datas if profiling is not start or sampling is not opened.
		return nil
	}
	isEnter, _ := strconv.ParseUint(event.GetStringUserAttribute("is_enter"), 10, 32)
	var evt *ThreadTraceIdEvent
	if isEnter == 1 {
		evt = &ThreadTraceIdEvent{
			Timestamp:    event.Timestamp,
			ApmType:      event.GetStringUserAttribute("apm_type"),
			ParentSpanId: event.GetStringUserAttribute("parent_id"),
			SpanId:       event.GetStringUserAttribute("span_id"),
			Tid:          event.Ctx.ThreadInfo.GetTid(),
		}
	} else {
		threadType, _ := strconv.ParseInt(event.GetStringUserAttribute("thread_type"), 10, 0)
		isError, _ := strconv.ParseInt(event.GetStringUserAttribute("error"), 10, 0)
		evt = &ThreadTraceIdEvent{
			Timestamp:     event.Timestamp,
			Protocol:      event.GetStringUserAttribute("protocol"),
			Url:           event.GetStringUserAttribute("url"),
			ThreadType:    int(threadType),
			Error:         int(isError),
			ClientSpanIds: strings.Split(event.GetStringUserAttribute("client_span_ids"), ","),
			Tid:           event.Ctx.ThreadInfo.GetTid(),
			ContainerId:   event.GetContainerId(),
		}
	}
	ta.analyzerTraceEvent(isEnter == 1, evt, event.GetStringUserAttribute("trace_id"), event.GetPid())
	return nil
}

func (ta *TraceIdAnalyzer) analyzerTraceEvent(isEnter bool, ev *ThreadTraceIdEvent, traceId string, pid uint32) {
	pidString := strconv.FormatUint(uint64(pid), 10)
	key := traceId + pidString
	entryEventInterface, exist := ta.javaTraces.Load(key)
	if !exist {
		if isEnter {
			ta.javaTraces.Store(key, NewTraceIdEvents(pid, traceId, ev))
		} else {
			ta.telemetry.Logger.Infof("Miss entry traceid event for TraceID=%s, Pid=%s", traceId, pidString)
		}
		return
	}

	entryEvent := entryEventInterface.(*TraceIdEvents)
	if isEnter {
		entryEvent.addTraceEvent(ev)
	} else {
		traceEvent := entryEvent.mergeTraceEvent(ev)
		if traceEvent == nil || !traceEvent.isBusinessThread() {
			// Skip none business thread.
			return
		}
		// Remove JavaTraceEvent
		ta.javaTraces.Delete(key)

		spendTime := traceEvent.Duration
		var isSlow bool
		if spendTime > uint64(ta.cfg.JavaTraceSlowTime)*1000000 {
			isSlow = true
		}
		contentKey := traceEvent.Url
		protocol := traceEvent.Protocol
		labels := model.NewAttributeMapWithValues(map[string]model.AttributeValue{
			constlabels.IsSlow:          model.NewBoolValue(isSlow),
			constlabels.Pid:             model.NewIntValue(int64(entryEvent.pid)),
			constlabels.Tid:             model.NewIntValue(int64(traceEvent.Tid)),
			constlabels.Protocol:        model.NewStringValue(protocol),
			constlabels.ContentKey:      model.NewStringValue(contentKey),
			constlabels.IsServer:        model.NewBoolValue(true),
			constlabels.IsError:         model.NewBoolValue(traceEvent.Error > 0),
			constlabels.ApmParentSpanId: model.NewStringValue(entryEvent.getParentSpanId()),
			constlabels.ApmSpanIds:      model.NewStringValue(entryEvent.getSpanIds()),
			constlabels.HttpApmTraceId:  model.NewStringValue(entryEvent.traceId),
			constlabels.ContainerId:     model.NewStringValue(traceEvent.ContainerId),
			constlabels.EndTime:         model.NewIntValue(int64(traceEvent.Timestamp + traceEvent.Duration)),
		})
		if protocol == "http" {
			labels.AddStringValue(constlabels.HttpUrl, contentKey)
		}
		if kubernetes.IsInitSuccess {
			k8sInfo, ok := ta.metadata.GetByContainerId(traceEvent.ContainerId)
			if ok {
				labels.AddStringValue(constlabels.DstWorkloadName, k8sInfo.RefPodInfo.WorkloadName)
				labels.AddStringValue(constlabels.DstContainer, k8sInfo.Name)
				labels.AddStringValue(constlabels.DstPod, k8sInfo.RefPodInfo.PodName)
			}
		}
		metric := model.NewIntMetric(constvalues.RequestTotalTime, int64(spendTime))
		dataGroup := model.NewDataGroup(constnames.SpanTraceGroupName, labels, traceEvent.Timestamp, metric)

		// Send Metric Trigger.
		cpuanalyzer.ReceiveDataGroupAsSignal(dataGroup)

		// Send to Aggreagate Processor and Sampling Processor.
		for _, nexConsumer := range ta.nextConsumers {
			_ = nexConsumer.Consume(dataGroup)
		}
	}
}

func (ta *TraceIdAnalyzer) cleanExpiredTraces() {
	timer := time.NewTicker(1 * time.Second)
	for {
		select {
		case <-timer.C:
			ta.javaTraces.Range(func(k, v interface{}) bool {
				entryEvent := v.(*TraceIdEvents)
				if entryEvent.getAndIncrCheckTimes() > 5 {
					// Clean Every 1 Mintue.
					ta.telemetry.Logger.Infof("==>Clean Expired JavaTrace: %v\n", entryEvent)
					ta.javaTraces.Delete(k)
				}
				return true
			})
		}
	}
}
