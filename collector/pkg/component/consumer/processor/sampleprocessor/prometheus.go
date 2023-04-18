package sampleprocessor

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Kindling-project/kindling/collector/pkg/component"
	"github.com/Kindling-project/kindling/collector/pkg/model/constlabels"
	"github.com/Kindling-project/kindling/collector/pkg/model/constnames"
	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

type prometheusP9xCache struct {
	// Pod - Url - P9x
	p9xCache          map[string]map[string]float64
	telemetry         *component.TelemetryTools
	query             string
	prometheusAddress string
}

func newPrometheusP9xCache(address string, p9xValue float32, duration string, promethuesPort int, telemetry *component.TelemetryTools) *prometheusP9xCache {
	query := fmt.Sprintf("histogram_quantile(%f, sum by (%s, %s, le) (rate(%s_bucket{instance=\"%s:%d\"}[%s])))",
		p9xValue,
		constlabels.ContentKey,
		constlabels.ContainerId,
		constnames.SpanTraceDurationMetric,
		getHostname(),
		promethuesPort,
		duration,
	)
	telemetry.Logger.Infof("## HostName: %s", getHostname())
	return &prometheusP9xCache{
		p9xCache:          make(map[string]map[string]float64, 0),
		query:             query,
		prometheusAddress: address,
		telemetry:         telemetry,
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

func (cache *prometheusP9xCache) updateP9x() {
	client, err := api.NewClient(api.Config{
		Address: cache.prometheusAddress,
	})
	if err != nil {
		cache.telemetry.Logger.Errorf("NewClient for Promethues - %s failed: %v", cache.prometheusAddress, err)
		return
	}
	v1Api := v1.NewAPI(client)
	result, warnings, err := v1Api.Query(context.Background(), cache.query, time.Now())
	if err != nil {
		cache.telemetry.Logger.Errorf("Request Prometheus P9x failed: %v", err)
		return
	}
	if len(warnings) > 0 {
		cache.telemetry.Logger.Warnf("Request Prometheus Warning: %s", warnings)
	}
	cache.telemetry.Logger.Infof("Receive [P9x] %s - %v", cache.query, result)

	if vector, ok := result.(model.Vector); ok {
		for _, sample := range vector {
			containerId := string(sample.Metric[constlabels.ContainerId])
			cacheContainer := cache.p9xCache[containerId]
			if len(cacheContainer) == 0 {
				cacheContainer = make(map[string]float64)
				cache.p9xCache[containerId] = cacheContainer
			}
			// Update P9x.
			url := string(sample.Metric[constlabels.ContentKey])
			cacheContainer[url] = float64(sample.Value)
			cache.telemetry.Logger.Infof("Update [P9x of %s]: %d", url, float64(sample.Value))
		}
	}
}
