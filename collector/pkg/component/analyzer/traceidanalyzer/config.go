package traceidanalyzer

type Config struct {
	// OpenJavaTraceSampling a switch for whether to use Java-Trace to trigger sampling.
	// The default is false.
	OpenJavaTraceSampling bool `mapstructure:"open_java_trace_sampling"`
	// JavaTraceSlowTime is used to identify the threshold of slow requests recognized by the apm side
	// The unit is milliSeconds.
	JavaTraceSlowTime int `mapstructure:"java_trace_slow_time"`
}

func NewDefaultConfig() *Config {
	return &Config{
		OpenJavaTraceSampling: false,
		JavaTraceSlowTime:     500,
	}
}
