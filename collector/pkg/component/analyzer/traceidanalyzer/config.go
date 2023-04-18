package traceidanalyzer

type Config struct {
	// OpenJavaTraceSampling a switch for whether to use Java-Trace to trigger sampling.
	// The default is false.
	OpenJavaTraceSampling bool `mapstructure:"open_java_trace_sampling"`
	// JavaTraceSlowTime is used to identify the threshold of slow requests recognized by the apm side
	// The unit is milliSeconds.
	JavaTraceSlowTime int `mapstructure:"java_trace_slow_time"`
	// JavaTraceWaitSecond is used to identify how long the traceid will be kept in memroy.
	// The unit is Second.
	JavaTraceWaitSecond int `mapstructure:"java_trace_wait_second"`
}

func NewDefaultConfig() *Config {
	return &Config{
		OpenJavaTraceSampling: false,
		JavaTraceSlowTime:     500,
		JavaTraceWaitSecond:   30,
	}
}
