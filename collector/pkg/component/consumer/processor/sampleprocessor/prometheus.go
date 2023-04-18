package sampleprocessor

import (
	"context"
	"os"

	"github.com/Kindling-project/kindling/collector/pkg/component"
	"github.com/Kindling-project/kindling/collector/pkg/model"
	"github.com/Kindling-project/kindling/collector/pkg/model/constlabels"
)

type prometheusP9xCache struct {
	// Grpc API
	client model.P9XServiceClient
	// Pod - Url - P9x
	p9xCache  map[string]map[string]float64
	hostName  string
	telemetry *component.TelemetryTools
}

func newPrometheusP9xCache(client model.P9XServiceClient, telemetry *component.TelemetryTools) *prometheusP9xCache {
	return &prometheusP9xCache{
		client:    client,
		p9xCache:  make(map[string]map[string]float64, 0),
		hostName:  getHostname(),
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
	if len(cache.p9xCache) == 0 {
		return 0
	}
	urlMap := cache.p9xCache[trace.dataGroup.Labels.GetStringValue(constlabels.ContainerId)]
	if len(urlMap) == 0 {
		return 0
	}
	return urlMap[trace.dataGroup.Labels.GetStringValue(constlabels.ContentKey)]
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

	for _, data := range result.Datas {
		cacheContainer := cache.p9xCache[data.ContainerId]
		if len(cacheContainer) == 0 {
			cacheContainer = make(map[string]float64)
			cache.p9xCache[data.ContainerId] = cacheContainer
		}
		// Update P9x.
		if data.Value > 0 {
			cacheContainer[data.Url] = data.Value
			cache.telemetry.Logger.Infof("Update [P9x of %s]: %d", data.Url, data.Value)
		}
	}
}
