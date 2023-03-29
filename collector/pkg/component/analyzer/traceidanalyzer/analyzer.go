package traceidanalyzer

import (
	"strconv"

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
	javaTraces    map[string]*TransactionIdEvent
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
		javaTraces:    make(map[string]*TransactionIdEvent, 100000),
		nextConsumers: consumers,
		metadata:      kubernetes.MetaDataCache,
	}
}

func (ta *TraceIdAnalyzer) Start() error {
	return nil
}

func (ta *TraceIdAnalyzer) Shutdown() error {
	return nil
}

func (ta *TraceIdAnalyzer) ConsumeEvent(event *model.KindlingEvent) error {
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
			Pid:         event.GetPid(),
			Tid:         event.Ctx.ThreadInfo.GetTid(),
			ContainerId: event.GetContainerId(),
		}
	} else {
		threadType, _ := strconv.ParseInt(event.GetStringUserAttribute("thread_type"), 10, 0)
		isError, _ := strconv.ParseInt(event.GetStringUserAttribute("error"), 10, 0)
		ev = &TransactionIdEvent{
			Timestamp:   event.Timestamp,
			TraceId:     event.GetStringUserAttribute("trace_id"),
			IsEntry:     0,
			ThreadType:  int(threadType),
			Error:       int(isError),
			Pid:         event.GetPid(),
			Tid:         event.Ctx.ThreadInfo.GetTid(),
			ContainerId: event.GetContainerId(),
		}
	}

	ta.analyzerJavaTraceTime(ev)
	return nil
}

func (ta *TraceIdAnalyzer) analyzerJavaTraceTime(ev *TransactionIdEvent) {
	tidString := strconv.FormatUint(uint64(ev.Tid), 10)
	key := ev.TraceId + tidString
	if ev.IsEntry == 1 {
		ta.javaTraces[key] = ev
	} else {
		entryEvent := ta.javaTraces[key]
		if entryEvent == nil {
			ta.telemetry.Logger.Infof("Not find the entry traceid event for TraceID=%s, threadID=%s", ev.TraceId, tidString)
			return
		}
		delete(ta.javaTraces, key)

		if ev.ThreadType != 0 {
			// 丢弃非业务线程数据.
			return
		}

		spendTime := ev.Timestamp - entryEvent.Timestamp
		var isSlow bool
		if spendTime > uint64(ta.cfg.JavaTraceSlowTime)*1000000 {
			isSlow = true
		}
		contentKey := entryEvent.Url
		protocol := entryEvent.Protocol
		labels := model.NewAttributeMapWithValues(map[string]model.AttributeValue{
			constlabels.IsSlow:         model.NewBoolValue(isSlow),
			constlabels.Pid:            model.NewIntValue(int64(ev.Pid)),
			constlabels.Tid:            model.NewIntValue(int64(ev.Tid)),
			constlabels.Protocol:       model.NewStringValue(protocol),
			constlabels.ContentKey:     model.NewStringValue(contentKey),
			constlabels.IsServer:       model.NewBoolValue(true),
			constlabels.IsError:        model.NewBoolValue(ev.Error > 0),
			constlabels.HttpApmTraceId: model.NewStringValue(ev.TraceId),
			constlabels.ContainerId:    model.NewStringValue(ev.ContainerId),
			constlabels.EndTime:        model.NewIntValue(int64(ev.Timestamp)),
		})
		if protocol == "http" {
			labels.AddStringValue(constlabels.HttpUrl, contentKey)
		}
		if kubernetes.IsInitSuccess {
			k8sInfo, ok := ta.metadata.GetByContainerId(ev.ContainerId)
			if ok {
				labels.AddStringValue(constlabels.DstWorkloadName, k8sInfo.RefPodInfo.WorkloadName)
				labels.AddStringValue(constlabels.DstContainer, k8sInfo.Name)
				labels.AddStringValue(constlabels.DstPod, k8sInfo.RefPodInfo.PodName)
			}
		}
		metric := model.NewIntMetric(constvalues.RequestTotalTime, int64(spendTime))
		dataGroup := model.NewDataGroup(constnames.SpanTraceGroupName, labels, entryEvent.Timestamp, metric)
		// 发送Metric触发器.
		cpuanalyzer.ReceiveDataGroupAsSignal(dataGroup)

		// 分发给 Aggreagate Processor 和 Sampling Processor.
		for _, nexConsumer := range ta.nextConsumers {
			_ = nexConsumer.Consume(dataGroup)
		}
	}
}
