package traceidanalyzer

type TraceIdEvents struct {
	hasBusiness bool
	pid         uint32
	traceId     string
	checkTimes  int
	events      []*ThreadTraceIdEvent
}

func NewTraceIdEvents(pid uint32, traceId string, event *ThreadTraceIdEvent) *TraceIdEvents {
	return &TraceIdEvents{
		hasBusiness: event.isBusinessThread(),
		pid:         pid,
		traceId:     traceId,
		checkTimes:  0,
		events:      []*ThreadTraceIdEvent{event},
	}
}

func (evts *TraceIdEvents) getAndIncrCheckTimes() int {
	evts.checkTimes = evts.checkTimes + 1
	return evts.checkTimes
}

func (evts *TraceIdEvents) addTraceEvent(enterEvent *ThreadTraceIdEvent) {
	evts.events = append(evts.events, enterEvent)
	if !evts.hasBusiness {
		// Record wheather business thread data is reached.
		evts.hasBusiness = enterEvent.isBusinessThread()
	}
}

func (evts *TraceIdEvents) mergeTraceEvent(exitEvent *ThreadTraceIdEvent) *ThreadTraceIdEvent {
	for _, evt := range evts.events {
		if len(evt.Url) == 0 && evt.Tid == exitEvent.Tid {
			evt.merge(exitEvent)
			return evt
		}
	}
	return nil
}

func (evts *TraceIdEvents) getParentSpanId() string {
	if len(evts.events) == 0 {
		return ""
	}
	return evts.events[0].ParentSpanId
}

func (evts *TraceIdEvents) getSpanIds() string {
	if len(evts.events) == 0 {
		return ""
	}
	spanIds := evts.events[0].SpanId
	for _, event := range evts.events {
		if len(event.ClientSpanIds) > 0 {
			for _, clientSpanId := range event.ClientSpanIds {
				if len(clientSpanId) > 0 {
					spanIds = spanIds + "," + clientSpanId
				}
			}
		}
	}
	return spanIds
}

type ThreadTraceIdEvent struct {
	Timestamp     uint64
	Duration      uint64
	ApmType       string
	ParentSpanId  string
	SpanId        string
	Protocol      string
	Url           string
	ThreadType    int // 0: businessThread 1: Async 2: I/O
	Error         int
	ClientSpanIds []string
	Tid           uint32
	ContainerId   string
}

func (evt *ThreadTraceIdEvent) merge(newEvt *ThreadTraceIdEvent) {
	evt.Duration = newEvt.Timestamp - evt.Timestamp
	evt.Protocol = newEvt.Protocol
	evt.Url = newEvt.Url
	evt.ThreadType = newEvt.ThreadType
	evt.Error = newEvt.Error
	evt.ClientSpanIds = newEvt.ClientSpanIds
	evt.Tid = newEvt.Tid
	evt.ContainerId = newEvt.ContainerId
}

func (evt *ThreadTraceIdEvent) isBusinessThread() bool {
	return evt.ThreadType == 0
}
