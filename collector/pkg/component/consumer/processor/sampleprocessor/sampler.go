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
	duration  uint64
	repeatNum int
}

func (sampleTrace *SampleTrace) getPidUrl() string {
	return fmt.Sprintf("%d-%s",
		sampleTrace.dataGroup.Labels.GetIntValue(constlabels.Pid),
		sampleTrace.dataGroup.Labels.GetStringValue(constlabels.ContentKey))
}

func NewSampleTrace(dataGroup *model.DataGroup, duration uint64, repeatNum int) *SampleTrace {
	return &SampleTrace{
		dataGroup: dataGroup,
		traceId:   dataGroup.Labels.GetStringValue(constlabels.HttpApmTraceId),
		duration:  duration,
		repeatNum: repeatNum,
	}
}

type SampleCache struct {
	traceLock  sync.RWMutex
	traceCache []*SampleTrace
	unSendIds  *UnSendIds
	// <traceId, sampledTime>, store every traceId traceHoldTime(ms).
	sampledTraceIds sync.Map
	// <url, lastTime>, store every url urlHitDuration(ms).
	urlHits sync.Map
	// config
	traceHoldTime   uint64
	urlHitDuration  uint64
	p9xIncreaseRate float64
	// Grpc API
	client    model.TraceIdServiceClient
	queryTime int64
	telemetry *component.TelemetryTools
	// P90 API
	p9xCache *prometheusP9xCache
	// Store Trace
	nextConsumer consumer.Consumer
}

func NewSampleCache(client model.TraceIdServiceClient, cfg *Config, telemetry *component.TelemetryTools, nextConsumer consumer.Consumer) *SampleCache {
	return &SampleCache{
		traceCache:      make([]*SampleTrace, 0),
		unSendIds:       NewUnSendIds(cfg.SampleTraceRepeatNum),
		traceHoldTime:   uint64(cfg.SampleTraceWaitTime) * 1000,
		urlHitDuration:  uint64(cfg.SampleUrlHitDuration) * 1000,
		client:          client,
		queryTime:       0,
		p9xCache:        newPrometheusP9xCache(cfg.PrometheusAddress, cfg.P9xValue, cfg.P9xDuration, cfg.PortForPrometheus, telemetry),
		p9xIncreaseRate: cfg.P9xIncreaseRate,
		telemetry:       telemetry,
		nextConsumer:    nextConsumer,
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
		return false
	}
	return cache.isSlow(sampleTrace)
}

func (cache *SampleCache) isSlow(sampleTrace *SampleTrace) bool {
	p9x := cache.p9xCache.getP9x(sampleTrace)
	sampleTrace.dataGroup.Labels.AddIntValue(constlabels.P90, int64(p9x))
	if p9x == 0.0 {
		// Not Got P9x.
		return sampleTrace.dataGroup.Labels.GetBoolValue(constlabels.IsSlow)
	}
	if sampleTrace.duration >= uint64(p9x*cache.p9xIncreaseRate) {
		return true
	}
	return false
}

func (cache *SampleCache) cacheSampleTrace(sampleTrace *SampleTrace) {
	cache.traceLock.Lock()
	defer cache.traceLock.Unlock()
	cache.traceCache = append(cache.traceCache, sampleTrace)
}

func (cache *SampleCache) storeProfiling(sampleTrace *SampleTrace) {
	sampleTrace.dataGroup.Labels.AddBoolValue(constlabels.IsProfiled, true)

	now := uint64(time.Now().UnixMilli())
	if _, exist := cache.sampledTraceIds.LoadOrStore(sampleTrace.traceId, now); !exist {
		// Record sampled traceIds and send them to receiver per second.
		cache.unSendIds.cacheIds(sampleTrace.traceId)
	}
	// Store Profiling Data.
	cpuanalyzer.ReceiveProfilingSignal(sampleTrace.dataGroup)

	cache.telemetry.Logger.Infof("Store Profiling: TraceId[%s]", sampleTrace.traceId)
}

func (cache *SampleCache) storeTrace(sampleTrace *SampleTrace) {
	// Update Url HitTime when trace is stored.
	cache.urlHits.Store(sampleTrace.getPidUrl(), uint64(time.Now().UnixMilli()))

	// Store sampled spanTrace.
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

	// Delete expired traceIds.
	cache.sampledTraceIds.Range(func(k, v interface{}) bool {
		time := v.(uint64)
		if now > time+cache.traceHoldTime {
			cache.telemetry.Logger.Infof("Delete Old TraceId: %s", k.(string))
			cache.sampledTraceIds.Delete(k)
		}
		return true
	})

	// Delete expired urlHits.
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
	lastLoopTraces = cache.traceCache[0:size]
	cache.traceCache = cache.traceCache[size:]
	cache.traceLock.Unlock()

	newLoopTraces := []*SampleTrace{}
	for _, sampleTrace := range lastLoopTraces {
		if cache.isTailBaseSampled(sampleTrace) {
			if cache.isSlow(sampleTrace) {
				// Store Profiling
				cache.storeProfiling(sampleTrace)
			}
			// Store tailbase sampled trace
			cache.storeTrace(sampleTrace)
		} else if sampleTrace.repeatNum > 0 {
			// Set sampleTrace times-1.
			sampleTrace.repeatNum--
			newLoopTraces = append(newLoopTraces, sampleTrace)
		}
		// Skip sampleTrace after N times.
	}
	if len(newLoopTraces) > 0 {
		cache.traceLock.Lock()
		// Put the data back to cache for looping n times.
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
	// Don't request traceIds when profiling is not started.
	if !cpuanalyzer.EnableProfile {
		if notifyTraceCount > 0 {
			cache.unSendIds.markSent(notifyTraceCount)
		}
		return
	}
	// Cache N seconds data when the server is not avaiable, send them when the server is avaiable.
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

	// Record Last queryTime for server
	cache.queryTime = result.GetQueryTime()
	if result.GetTraceIds() != nil {
		now := uint64(time.Now().UnixMilli())
		for _, traceId := range result.GetTraceIds() {
			// Store the tailbased traceIds.
			cache.sampledTraceIds.LoadOrStore(traceId, now)
		}
	}
}

func (cache *SampleCache) updateP9xByPromethus(interval int) {
	timer := time.NewTicker(time.Duration(interval) * time.Second)
	for {
		select {
		case <-timer.C:
			cache.p9xCache.updateP9x()
		}
	}
}

type UnSendIds struct {
	lock      sync.RWMutex
	toSendIds []string
	counts    []int // Record count per seconds
	lastNum   int   // Record how many datas are not sent last time.
	size      int   // Record how many datas are sent
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
		// Clean the oldest record, add new recrod to make cache store N seconds.
		unSend.counts = append(unSend.counts[1:], num-unSend.lastNum)
		unSend.lastNum = num - toRemoveSize
	}
}

func (unSend *UnSendIds) markSent(num int) {
	unSend.lock.Lock()
	unSend.toSendIds = unSend.toSendIds[num:]
	unSend.lock.Unlock()

	// Reset count
	unSend.size = 0
	unSend.lastNum = 0
}

func (unSend *UnSendIds) getToSendCount() int {
	return len(unSend.toSendIds)
}

func (unSend *UnSendIds) getToSendIds(size int) []string {
	return unSend.toSendIds[0:size]
}
