package cpuanalyzer

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/Kindling-project/kindling/collector/pkg/metadata/kubernetes"
	"github.com/Kindling-project/kindling/collector/pkg/model"
	"github.com/Kindling-project/kindling/collector/pkg/model/constlabels"
	"github.com/Kindling-project/kindling/collector/pkg/model/constnames"
	"github.com/Kindling-project/kindling/collector/pkg/model/constvalues"
)

type SampleTrace struct {
	timestamp   uint64
	duration    uint64
	url         string
	apmType     string
	protocol    string
	hasError    bool
	traceId     string
	containerId string
	pid         int64
	repeatNum   int
}

func (sampleTrace *SampleTrace) getPidUrl() string {
	return fmt.Sprintf("%d-%s", sampleTrace.pid, sampleTrace.url)
}

func NewSampleTrace(entry *TransactionIdEvent, exit *TransactionIdEvent, repeatNum int) *SampleTrace {
	pid, _ := strconv.ParseInt(entry.PidString, 10, 64)
	return &SampleTrace{
		timestamp:   entry.Timestamp,
		duration:    exit.Timestamp - entry.EndTimestamp(),
		url:         entry.Url,
		apmType:     entry.ApmType,
		protocol:    entry.Protocol,
		hasError:    exit.Error > 0,
		traceId:     entry.TraceId,
		containerId: entry.ContainerId,
		pid:         pid,
		repeatNum:   repeatNum,
	}
}

func (sampleTrace *SampleTrace) storeProfiling(metadata *kubernetes.K8sMetaDataCache) {
	protocol := sampleTrace.protocol
	labels := model.NewAttributeMapWithValues(map[string]model.AttributeValue{
		constlabels.IsSlow:         model.NewBoolValue(true),
		constlabels.Pid:            model.NewIntValue(sampleTrace.pid),
		constlabels.Protocol:       model.NewStringValue(protocol),
		constlabels.ContentKey:     model.NewStringValue(sampleTrace.url),
		"isInstallApm":             model.NewBoolValue(true),
		constlabels.IsServer:       model.NewBoolValue(true),
		constlabels.HttpApmTraceId: model.NewStringValue(sampleTrace.traceId),
	})
	if protocol == "http" {
		labels.AddStringValue(constlabels.HttpUrl, sampleTrace.url)
	}
	if kubernetes.IsInitSuccess {
		k8sInfo, ok := metadata.GetByContainerId(sampleTrace.containerId)
		if ok {
			labels.AddStringValue(constlabels.DstWorkloadName, k8sInfo.RefPodInfo.WorkloadName)
			labels.AddStringValue(constlabels.DstContainer, k8sInfo.Name)
			labels.AddStringValue(constlabels.DstPod, k8sInfo.RefPodInfo.PodName)
		}
	}
	metric := model.NewIntMetric(constvalues.RequestTotalTime, int64(sampleTrace.duration))
	dataGroup := model.NewDataGroup(constnames.SpanEvent, labels, sampleTrace.timestamp, metric)
	ReceiveDataGroupAsSignal(dataGroup)
}

type SampleCache struct {
	// 实时Trace数据
	traceLock  sync.RWMutex
	traceCache []*SampleTrace
	// 本机采样命中TraceId记录，每秒清空
	notifyLock     sync.RWMutex
	notifyTraceIds []string
	// 全链路需采样的TraceId记录<traceId, sampledTime>，每个TraceId缓存traceHoldTime毫秒.
	sampledTraceIds sync.Map
	// 命中采样的URL记录<url, lastTime>，每个url缓存urlHitDuration毫秒.
	urlHits sync.Map
	// 缓存所需配置
	slowThreshold  uint64 // 慢请求阈值(ms)
	traceHoldTime  uint64 // 一个采样TraceId等待N(ms)，该时间段内接收的该TraceId数据都保存.
	urlHitDuration uint64 // 保存某个URL N(ms)，该时间段内该URL不会被采样命中.
	// Grpc调用API
	client    model.TraceIdServiceClient
	queryTime int64
	metadata  *kubernetes.K8sMetaDataCache
}

func NewSampleCache(client model.TraceIdServiceClient, slowThreshold int, traceHoldTime int, urlHitDuration int, metadata *kubernetes.K8sMetaDataCache) *SampleCache {
	return &SampleCache{
		traceCache:     make([]*SampleTrace, 0),
		notifyTraceIds: make([]string, 0),
		slowThreshold:  uint64(slowThreshold) * uint64(time.Millisecond),
		traceHoldTime:  uint64(traceHoldTime) * uint64(time.Millisecond),
		urlHitDuration: uint64(urlHitDuration) * uint64(time.Millisecond),
		client:         client,
		queryTime:      0,
		metadata:       metadata,
	}
}

func (cache *SampleCache) isTailBaseSampled(sampleTrace *SampleTrace) bool {
	_, ok := cache.sampledTraceIds.Load(sampleTrace.traceId)
	return ok
}

func (cache *SampleCache) isSampled(sampleTrace *SampleTrace) bool {
	if _, ok := cache.urlHits.Load(sampleTrace.getPidUrl()); ok {
		return false
	}
	return sampleTrace.hasError || sampleTrace.duration >= cache.slowThreshold
}

func (cache *SampleCache) cacheSampleTrace(sampleTrace *SampleTrace) {
	cache.traceLock.Lock()
	defer cache.traceLock.Unlock()
	cache.traceCache = append(cache.traceCache, sampleTrace)
}

func (cache *SampleCache) storeProfiling(sampleTrace *SampleTrace) {
	now := uint64(time.Now().UnixMilli())
	// 记录该URL命中
	cache.urlHits.LoadOrStore(sampleTrace.getPidUrl(), now)
	// 记录TraceId采样
	if _, exist := cache.sampledTraceIds.LoadOrStore(sampleTrace.traceId, now); !exist {
		// 待转发TraceId列表
		cache.notifyLock.Lock()
		cache.notifyTraceIds = append(cache.notifyTraceIds, sampleTrace.traceId)
		cache.notifyLock.Unlock()
	}
	// 保存Profiling数据
	sampleTrace.storeProfiling(cache.metadata)
}

func (cache *SampleCache) storeTrace(sampleTrace *SampleTrace) {
	// TODO 保存SampleTrace数据
}

func (cache *SampleCache) loopCheckTailBaseTraces() {
	timer := time.NewTicker(1 * time.Second)
	for {
		select {
		case <-timer.C:
			cache.checkTailBaseTraces()
		}
	}
}

func (cache *SampleCache) checkTailBaseTraces() {
	now := uint64(time.Now().UnixMilli())

	// 删除过期TraceId记录.
	cache.sampledTraceIds.Range(func(k, v interface{}) bool {
		time := v.(uint64)
		if now > time+cache.traceHoldTime {
			cache.sampledTraceIds.Delete(k)
		}
		return true
	})

	// 删除过期Url Hit记录.
	cache.urlHits.Range(func(k, v interface{}) bool {
		time := v.(uint64)
		if now > time+cache.urlHitDuration {
			cache.urlHits.Delete(k)
		}
		return true
	})

	size := len(cache.traceCache)
	if size == 0 {
		return
	}

	var lastLoopTraces []*SampleTrace
	cache.traceLock.Lock()
	// 获取上一轮数据，避免某次循环需要清空几K Trace数据，减少加锁操作.
	lastLoopTraces = cache.traceCache[0:size]
	cache.traceCache = cache.traceCache[size:]
	cache.traceLock.Unlock()

	newLoopTraces := []*SampleTrace{}
	for _, sampleTrace := range lastLoopTraces {
		if cache.isTailBaseSampled(sampleTrace) {
			// TailBase 命中，保留该Trace数据
			cache.storeTrace(sampleTrace)
		} else if sampleTrace.repeatNum > 0 {
			sampleTrace.repeatNum--
			newLoopTraces = append(newLoopTraces, sampleTrace)
		}
		// 丢弃轮询N次SampleTrace数据.
	}
	if len(newLoopTraces) > 0 {
		cache.traceLock.Lock()
		// 将数据回写缓存，重试N次
		cache.traceCache = append(cache.traceCache, newLoopTraces...)
		cache.traceLock.Unlock()
	}
}

func (cache *SampleCache) loopSendAndRecvTraces() {
	timer := time.NewTicker(1 * time.Second)
	for {
		select {
		case <-timer.C:
			cache.sendAndRecvSampledTraces()
		}
	}
}

func (cache *SampleCache) sendAndRecvSampledTraces() {
	sampledTraceIds := []string{}
	notifyTraceCount := len(cache.notifyTraceIds)
	if notifyTraceCount > 0 {
		cache.notifyLock.Lock()
		// 获取待发送TraceId数据
		sampledTraceIds = cache.notifyTraceIds[0:notifyTraceCount]
		// 每次循环清空数据
		cache.notifyTraceIds = cache.notifyTraceIds[notifyTraceCount:]
		cache.notifyLock.Unlock()
	}
	traceIds := &model.TraceIds{
		QueryTime: cache.queryTime,
		TraceIds:  sampledTraceIds,
	}
	result, err := cache.client.SendTraceIds(context.Background(), traceIds)
	if err != nil {
		fmt.Printf("Send TraceIds failed%v\n", err)
		return
	}
	cache.queryTime = result.GetQueryTime()
	if result.GetTraceIds() != nil {
		now := uint64(time.Now().UnixMilli())
		for _, traceId := range result.GetTraceIds() {
			// 记录TailBase相关的TraceId列表
			cache.sampledTraceIds.LoadOrStore(traceId, now)
		}
	}
}
