package sampleprocessor

type Config struct {
	// Set RepeatNum for Trace Sampling as Tail Trace may set traceId later.
	SampleTraceRepeatNum int `mapstructure:"sample_trace_repeat_num"`
	// Set Max wait time for sampled traceId info, the rootTrace may return aftern Ns.
	// The unit is seconds.
	SampleTraceWaitTime int `mapstructure:"sample_trace_wait_time"`
	// The same url will be sampled once in hit duration.
	// The unit is seconds.
	SampleUrlHitDuration int `mapstructure:"sample_url_hit_duration"`
	// Set Mini time for sampled trace. The trace will ignore when the duration is less than the threshold.
	// The unit is millisecond.
	SampleTraceIgnoreThreshold int `mapstructure:"sample_trace_ignore_threshold"`
	// Set Promethues Query Interval
	// The unit is seconds.
	PrometheusQueryInterval int `mapstructure:"promethues_query_interval"`
	// Set the increase rate for p9x
	P9xIncreaseRate float64 `mapstructure:"query_p9x_increase_rate"`
	// Store tailbased Profiling data
	StoreProfileTailBase bool `mapstructure:"store_profile_tailbase"`
	// The Receiver Ip
	ReceiverIp string `mapstructure:"receiver_ip"`
	// The Receiver Port
	ReceiverPort int `mapstructure:"receiver_port"`
}

func NewDefaultConfig() *Config {
	return &Config{
		SampleTraceRepeatNum:       3,
		SampleTraceIgnoreThreshold: 100,
		SampleTraceWaitTime:        30,
		SampleUrlHitDuration:       5,
		PrometheusQueryInterval:    30,
		P9xIncreaseRate:            1.0,
		ReceiverIp:                 "127.0.0.1",
		ReceiverPort:               29090,
	}
}
