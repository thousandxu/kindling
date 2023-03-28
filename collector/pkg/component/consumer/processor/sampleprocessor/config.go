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
	// The Receiver Ip
	ReceiverIp string `mapstructure:"receiver_ip"`
	// The Receiver Port
	ReceiverPort int `mapstructure:"receiver_port"`
}

func NewDefaultConfig() *Config {
	return &Config{
		SampleTraceRepeatNum: 3,
		SampleTraceWaitTime:  30,
		SampleUrlHitDuration: 5,
		ReceiverIp:           "127.0.0.1",
		ReceiverPort:         29090,
	}
}
