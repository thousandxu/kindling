package traceidanalyzer

type Config struct {
	// JavaTraceSlowTime is used to identify the threshold of slow requests recognized by the apm side
	// The unit is milliSeconds.
	JavaTraceSlowTime int `mapstructure:"java_trace_slow_time"`
}

func NewDefaultConfig() *Config {
	return &Config{
		JavaTraceSlowTime: 500,
	}
}
