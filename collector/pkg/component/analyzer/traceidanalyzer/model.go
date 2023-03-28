package traceidanalyzer

type TransactionIdEvent struct {
	Timestamp   uint64 `json:"timestamp"`
	TraceId     string `json:"traceId"`
	IsEntry     uint32 `json:"isEntry"`
	Protocol    string `json:"protocol"`
	Url         string `json:"url"`
	ApmType     string `json:"apmType"`
	ThreadType  int    `json:"threadType"`
	Error       int    `json:"error"`
	Pid         uint32 `json:"pidString"`
	Tid         uint32 `json:"tid"`
	ContainerId string `json:"containerId"`
}
