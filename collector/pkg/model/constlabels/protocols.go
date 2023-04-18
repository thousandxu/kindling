package constlabels

const (
	ContentKey      = "content_key"
	RequestPayload  = "request_payload"
	ResponsePayload = "response_payload"

	ApmParentSpanId = "apm_parent_id"
	ApmSpanIds      = "apm_span_ids"

	IsProfiled = "is_profiled"
	P90        = "p90"

	HttpMethod       = "http_method"
	HttpUrl          = "http_url"
	HttpApmTraceType = "trace_type"
	HttpApmTraceId   = "trace_id"
	HttpStatusCode   = "http_status_code"
	HttpContinue     = "http_continue"

	DnsId     = "dns_id"
	DnsDomain = "dns_domain"
	DnsRcode  = "dns_rcode"
	DnsIp     = "dns_ip"

	Sql        = "sql"
	SqlErrCode = "sql_error_code"
	SqlErrMsg  = "sql_error_msg"

	RedisCommand = "redis_command"
	RedisErrMsg  = "redis_error_msg"

	KafkaApi           = "kafka_api"
	KafkaVersion       = "kafka_version"
	KafkaCorrelationId = "kafka_id"
	KafkaTopic         = "kafka_topic"
	KafkaErrorCode     = "kafka_error_code"

	DubboErrorCode = "dubbo_error_code"

	RocketMQOpaque     = "rocketmq_opaque"
	RocketMQRequestMsg = "rocketmq_request_msg"
	RocketMQErrMsg     = "rocketmq_error_msg"
	RocketMQErrCode    = "rocketmq_error_code"
)
