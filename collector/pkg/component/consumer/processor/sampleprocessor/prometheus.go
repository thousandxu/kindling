package sampleprocessor

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/Kindling-project/kindling/collector/pkg/component"
	"github.com/Kindling-project/kindling/collector/pkg/model"
	"github.com/Kindling-project/kindling/collector/pkg/model/constlabels"
)

type prometheusP9xCache struct {
	// Grpc API
	client model.P9XServiceClient
	// <containreId, *prometheusContainerP9x>
	p9xCache  sync.Map
	hostName  string
	timeout   int64
	telemetry *component.TelemetryTools
}

func newPrometheusP9xCache(client model.P9XServiceClient, telemetry *component.TelemetryTools) *prometheusP9xCache {
	return &prometheusP9xCache{
		client:    client,
		hostName:  getHostname(),
		timeout:   24 * 3600, // 1 day
		telemetry: telemetry,
	}
}

func getHostname() string {
	if value, ok := os.LookupEnv("MY_NODE_IP"); ok {
		return value
	}
	hostName, err := os.Hostname()
	if err != nil {
		hostName = "unknown"
	}
	return hostName
}

func (cache *prometheusP9xCache) getP9x(trace *SampleTrace) float64 {
	if p9xInterface, exist := cache.p9xCache.Load(trace.dataGroup.Labels.GetStringValue(constlabels.ContainerId)); exist {
		p9x := p9xInterface.(*prometheusContainerP9x)
		return p9x.p9xValues[trace.dataGroup.Labels.GetStringValue(constlabels.ContentKey)]
	}
	return 0
}

func (cache *prometheusP9xCache) updateP9xByGrpc() {
	result, err := cache.client.QueryP9X(context.Background(), &model.P9XRequest{Ip: cache.hostName})
	if err != nil {
		cache.telemetry.Logger.Errorf("Send P9x failed: %v", err)
		return
	}
	if result == nil || len(result.Datas) == 0 {
		return
	}

	var p9x *prometheusContainerP9x
	now := time.Now().Unix() // Set as second.
	for _, data := range result.Datas {
		p9xInterface, exist := cache.p9xCache.Load(data.ContainerId)
		if !exist {
			p9x = newPrometheusContainerP9x(now)
			cache.p9xCache.Store(data.ContainerId, p9x)
		} else {
			p9x = p9xInterface.(*prometheusContainerP9x)
		}
		p9x.updateP9x(data, now)
	}

	// Clean Expired ContainerId
	cache.p9xCache.Range(func(k, v interface{}) bool {
		p9x := v.(*prometheusContainerP9x)
		if now-p9x.updateTime >= cache.timeout {
			cache.telemetry.Logger.Infof("Clean Expired P9x for Container: %s", k)
			cache.p9xCache.Delete(k)
		}
		return true
	})
}

type prometheusContainerP9x struct {
	updateTime int64
	p9xValues  map[string]float64
}

func newPrometheusContainerP9x(now int64) *prometheusContainerP9x {
	return &prometheusContainerP9x{
		updateTime: now,
		p9xValues:  make(map[string]float64, 0),
	}
}

func (p9x *prometheusContainerP9x) updateP9x(data *model.P9XData, time int64) {
	if data.Value > 0 {
		p9x.p9xValues[data.Url] = data.Value
		p9x.updateTime = time
	}
}
