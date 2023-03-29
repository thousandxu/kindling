package prometheusexporter

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Kindling-project/kindling/collector/pkg/aggregator/defaultaggregator"
	"github.com/Kindling-project/kindling/collector/pkg/component"
	"github.com/Kindling-project/kindling/collector/pkg/model"
	"github.com/Kindling-project/kindling/collector/pkg/model/constnames"
	"github.com/Kindling-project/kindling/collector/pkg/model/constvalues"
)

type collector struct {
	aggregator *defaultaggregator.CumulativeAggregator
}

func (c *collector) Collect(metrics chan<- prometheus.Metric) {
	// TODO debug
	dataGroups := c.aggregator.DumpAndRemoveExpired(time.Now())
	for i := 0; i < len(dataGroups); i++ {
		dataGroup := dataGroups[i]
		labelMap := dataGroup.Labels.GetValues()
		ts := getTimestamp(dataGroup.Timestamp)
		keys := make([]string, 0, len(labelMap))
		values := make([]string, 0, len(labelMap))
		for k, v := range labelMap {
			keys = append(keys, k)
			values = append(values, v.ToString())
		}
		for s := 0; s < len(dataGroup.Metrics); s++ {
			metric := dataGroup.Metrics[s]
			switch metric.DataType() {
			case model.IntMetricType:
				metric, err := prometheus.NewConstMetric(prometheus.NewDesc(
					sanitize(metric.Name, true),
					"",
					keys,
					nil,
					// TODO not all IntMetric are Counter, they can also be a Metric
				), prometheus.CounterValue, float64(metric.GetInt().Value), values...)
				if err == nil {
					tm := prometheus.NewMetricWithTimestamp(ts, metric)
					metrics <- tm
				}
			case model.HistogramMetricType:
				histogram := metric.GetHistogram()
				buckets := make(map[float64]uint64, len(histogram.ExplicitBoundaries))
				for x := 0; x < len(histogram.ExplicitBoundaries); x++ {
					bound := histogram.ExplicitBoundaries[x]
					buckets[float64(bound)] = histogram.BucketCounts[x]
				}
				metric, err := prometheus.NewConstHistogram(prometheus.NewDesc(
					sanitize(metric.Name, true),
					"",
					keys,
					nil,
				), histogram.Count, float64(histogram.Sum), buckets, values...)
				if err == nil {
					tm := prometheus.NewMetricWithTimestamp(ts, metric)
					metrics <- tm
				}
			}
		}
	}
}

func (c *collector) recordMetricGroups(group *model.DataGroup) {
	c.aggregator.AggregatorWithAllLabelsAndMetric(group, time.Now())
}

func newCollector(_ *Config, _ *component.TelemetryLogger) *collector {
	// TODO Do this in config later !!!!
	requestTimeHistogramTopologyMetric := constnames.ToKindlingNetMetricName(constvalues.RequestTimeHistogram, false)
	requestTimeHistogramEntityMetric := constnames.ToKindlingNetMetricName(constvalues.RequestTimeHistogram, true)
	profilingBoundaries := []int64{5000000, 10000000, 20000000, 30000000, 50000000, 80000000, 100000000, 150000000, 200000000, 300000000, 400000000, 500000000, 800000000, 1200000000, 3000000000, 5000000000}
	requestDurationBoundaries := []int64{10000000, 30000000, 50000000, 80000000, 100000000, 150000000, 200000000, 300000000, 350000000, 400000000, 450000000, 500000000, 600000000, 800000000, 1200000000, 3000000000, 5000000000}
	return &collector{
		aggregator: defaultaggregator.NewCumulativeAggregator(
			&defaultaggregator.AggregatedConfig{
				KindMap: map[string][]defaultaggregator.KindConfig{
					constnames.TcpRttMetricName: {{
						Kind:       defaultaggregator.LastKind,
						OutputName: constnames.TcpRttMetricName,
					}},
					requestTimeHistogramTopologyMetric: {{
						Kind:               defaultaggregator.HistogramKind,
						OutputName:         requestTimeHistogramTopologyMetric,
						ExplicitBoundaries: []int64{10e6, 20e6, 50e6, 80e6, 130e6, 200e6, 300e6, 400e6, 500e6, 700e6, 1000e6, 2000e6, 5000e6, 30000e6},
					}},
					requestTimeHistogramEntityMetric: {{
						Kind:               defaultaggregator.HistogramKind,
						OutputName:         requestTimeHistogramEntityMetric,
						ExplicitBoundaries: []int64{10e6, 20e6, 50e6, 80e6, 130e6, 200e6, 300e6, 400e6, 500e6, 700e6, 1000e6, 2000e6, 5000e6, 30000e6},
					}},
					constnames.ProfilingCpuDurationMetric: {{
						Kind:               defaultaggregator.HistogramKind,
						OutputName:         constnames.ProfilingCpuDurationMetric,
						ExplicitBoundaries: profilingBoundaries,
					}},
					constnames.ProfilingNetDurationMetric: {{
						Kind:               defaultaggregator.HistogramKind,
						OutputName:         constnames.ProfilingNetDurationMetric,
						ExplicitBoundaries: profilingBoundaries,
					}},
					constnames.ProfilingFileDurationMetric: {{
						Kind:               defaultaggregator.HistogramKind,
						OutputName:         constnames.ProfilingFileDurationMetric,
						ExplicitBoundaries: profilingBoundaries,
					}},
					constnames.ProfilingFutexDurationMetric: {{
						Kind:               defaultaggregator.HistogramKind,
						OutputName:         constnames.ProfilingFutexDurationMetric,
						ExplicitBoundaries: profilingBoundaries,
					}},
					constnames.SpanTraceDurationMetric: {{
						Kind:               defaultaggregator.HistogramKind,
						OutputName:         constnames.SpanTraceDurationMetric,
						ExplicitBoundaries: requestDurationBoundaries,
					}},
				},
			}, time.Minute*5)}
}

func getTimestamp(ts uint64) time.Time {
	return time.UnixMicro(int64(ts / 1000))
}

// Describe is a no-op, because the collector dynamically allocates metrics.
// https://github.com/prometheus/client_golang/blob/v1.9.0/prometheus/collector.go#L28-L40
func (c *collector) Describe(_ chan<- *prometheus.Desc) {}
