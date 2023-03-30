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
	unSendIds *UnSendIds
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
		unSendIds:      NewUnSendIds(cfg.SampleTraceRepeatNum),
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
		cache.telemetry.Logger.Infof("Trace is stored by tailBase: traceId[%s]", sampleTrace.traceId)
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
	// 记录TraceId采样
	if _, exist := cache.sampledTraceIds.LoadOrStore(sampleTrace.traceId, now); !exist {
		// 待转发TraceId列表
		cache.unSendIds.cacheIds(sampleTrace.traceId)
	}
	// 保存Profiling数据
	cpuanalyzer.ReceiveProfilingSignal(sampleTrace.dataGroup)

	cache.telemetry.Logger.Infof("Store Profiling: TraceId[%s]", sampleTrace.traceId)
}

func (cache *SampleCache) storeTrace(sampleTrace *SampleTrace) {
	// 每次保存一条Trace，更新该URL命中时间
	cache.urlHits.Store(sampleTrace.getPidUrl(), uint64(time.Now().UnixMilli()))

	// 保存采样的SpanTrace数据
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
	cache.telemetry.Logger.Infof("Clear Normal Traces[%d] => %d", size, len(newLoopTraces))
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
	notifyTraceCount := cache.unSendIds.getToSendCount()
	if !cpuanalyzer.EnableProfile {
		// 没有主动开启Profiling，不请求采样TraceIds.
		if notifyTraceCount > 0 {
			cache.unSendIds.markSent(notifyTraceCount)
		}
		return
	}
	// 存在发送失败，缓存Ns数据。当服务端启动后直接发送Ns数据.
	traceIds := &model.TraceIds{
		QueryTime: cache.queryTime,
		TraceIds:  cache.unSendIds.getToSendIds(notifyTraceCount),
	}
	result, err := cache.client.SendTraceIds(context.Background(), traceIds)
	if err != nil {
		cache.telemetry.Logger.Errorf("Send TraceIds failed: %v", err)
		cache.unSendIds.markUnSent(notifyTraceCount)
		return
	}
	if notifyTraceCount > 0 {
		cache.unSendIds.markSent(notifyTraceCount)
	}

	// 记录最近一次请求的时间
	cache.queryTime = result.GetQueryTime()
	if result.GetTraceIds() != nil {
		now := uint64(time.Now().UnixMilli())
		for _, traceId := range result.GetTraceIds() {
			// 记录TailBase相关的TraceId列表
			cache.sampledTraceIds.LoadOrStore(traceId, now)
		}
	}
}

type UnSendIds struct {
	lock      sync.RWMutex
	toSendIds []string // 记录未发送记录
	counts    []int    // 记录每秒数据量.
	lastNum   int      // 记录上一轮检测时总共有多少数据未发送
	size      int      // 记录当前有几秒数据未发送，最多存储len(counts)记录
}

func NewUnSendIds(size int) *UnSendIds {
	return &UnSendIds{
		toSendIds: make([]string, 0),
		counts:    make([]int, size),
		size:      0,
	}
}

func (unSend *UnSendIds) cacheIds(id string) {
	unSend.lock.Lock()
	unSend.toSendIds = append(unSend.toSendIds, id)
	unSend.lock.Unlock()
}

func (unSend *UnSendIds) markUnSent(num int) {
	if unSend.size == 0 {
		unSend.counts[0] = num
		unSend.size = 1
		unSend.lastNum = num
	} else if unSend.size < len(unSend.counts) {
		unSend.counts[unSend.size] = num - unSend.lastNum
		unSend.size++
		unSend.lastNum = num
	} else {
		toRemoveSize := unSend.counts[0]
		if toRemoveSize > 0 {
			unSend.lock.Lock()
			unSend.toSendIds = unSend.toSendIds[toRemoveSize:]
			unSend.lock.Unlock()
		}
		// 清除第一个数据，并将数据添加到最后
		unSend.counts = append(unSend.counts[1:], num-unSend.lastNum)
		unSend.lastNum = num - toRemoveSize
	}
}

func (unSend *UnSendIds) markSent(num int) {
	unSend.lock.Lock()
	unSend.toSendIds = unSend.toSendIds[num:]
	unSend.lock.Unlock()

	// 数组不清空，只重置计数.
	unSend.size = 0
	unSend.lastNum = 0
}

func (unSend *UnSendIds) getToSendCount() int {
	return len(unSend.toSendIds)
}

func (unSend *UnSendIds) getToSendIds(size int) []string {
	return unSend.toSendIds[0:size]
}
