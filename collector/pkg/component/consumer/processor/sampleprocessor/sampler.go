package sampleprocessor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Kindling-project/kindling/collector/pkg/component"
	"github.com/Kindling-project/kindling/collector/pkg/component/analyzer/cpuanalyzer"
	"github.com/Kindling-project/kindling/collector/pkg/component/consumer"
	"github.com/Kindling-project/kindling/collector/pkg/model"
	"github.com/Kindling-project/kindling/collector/pkg/model/constlabels"
)

type SampleTrace struct {
	dataGroup *model.DataGroup
	traceId   string
	repeatNum int
}

func (sampleTrace *SampleTrace) getPidUrl() string {
	return fmt.Sprintf("%d-%s",
		sampleTrace.dataGroup.Labels.GetIntValue(constlabels.Pid),
		sampleTrace.dataGroup.Labels.GetStringValue(constlabels.ContentKey))
}

func NewSampleTrace(dataGroup *model.DataGroup, repeatNum int) *SampleTrace {
	return &SampleTrace{
		dataGroup: dataGroup,
		traceId:   dataGroup.Labels.GetStringValue(constlabels.HttpApmTraceId),
		repeatNum: repeatNum,
	}
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
	traceHoldTime  uint64 // 一个采样TraceId等待N(ms)，该时间段内接收的该TraceId数据都保存.
	urlHitDuration uint64 // 保存某个URL N(ms)，该时间段内该URL不会被采样命中.
	// Grpc调用API
	client    model.TraceIdServiceClient
	queryTime int64
	// 日志输出
	telemetry *component.TelemetryTools
	// 保存Trace数据
	nextConsumer consumer.Consumer
}

func NewSampleCache(client model.TraceIdServiceClient, cfg *Config, telemetry *component.TelemetryTools, nextConsumer consumer.Consumer) *SampleCache {
	return &SampleCache{
		traceCache:     make([]*SampleTrace, 0),
		notifyTraceIds: make([]string, 0),
		traceHoldTime:  uint64(cfg.SampleTraceWaitTime) * 1000,
		urlHitDuration: uint64(cfg.SampleUrlHitDuration) * 1000,
		client:         client,
		queryTime:      0,
		telemetry:      telemetry,
		nextConsumer:   nextConsumer,
	}
}

func (cache *SampleCache) isTailBaseSampled(sampleTrace *SampleTrace) bool {
	if _, ok := cache.sampledTraceIds.Load(sampleTrace.traceId); ok {
		cache.telemetry.Logger.Warnf("Trace is stored by tailBase: traceId[%s]", sampleTrace.traceId)
		return ok
	}
	return false
}

func (cache *SampleCache) isSampled(sampleTrace *SampleTrace) bool {
	if _, ok := cache.urlHits.Load(sampleTrace.getPidUrl()); ok {
		cache.telemetry.Logger.Warnf("Ignore: Trace By Url[%s]", sampleTrace.getPidUrl())
		return false
	}
	return sampleTrace.dataGroup.Labels.GetBoolValue(constlabels.IsError) ||
		sampleTrace.dataGroup.Labels.GetBoolValue(constlabels.IsSlow)
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
	cpuanalyzer.ReceiveProfilingSignal(sampleTrace.dataGroup)

	cache.telemetry.Logger.Infof("Store Profiling: TraceId[%s]", sampleTrace.traceId)
}

func (cache *SampleCache) storeTrace(sampleTrace *SampleTrace) {
	// 保存SampleTrace数据
	// 修改 dataGroup Name
	// sampleTrace.dataGroup.Name = ""
	cache.telemetry.Logger.Infof("Store Trace: %v", sampleTrace.dataGroup)
	cache.nextConsumer.Consume(sampleTrace.dataGroup)
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
			cache.telemetry.Logger.Infof("Delete Old TraceId: %s", k.(string))
			cache.sampledTraceIds.Delete(k)
		}
		return true
	})

	// 删除过期Url Hit记录.
	cache.urlHits.Range(func(k, v interface{}) bool {
		time := v.(uint64)
		if now > time+cache.urlHitDuration {
			cache.telemetry.Logger.Infof("Delete Old Url: %s", k.(string))
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
	ctx, _ := context.WithTimeout(context.Background(), time.Second)
	result, err := cache.client.SendTraceIds(ctx, traceIds)
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
