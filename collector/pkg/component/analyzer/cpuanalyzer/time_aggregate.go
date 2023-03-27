package cpuanalyzer

type AggregatedTime struct {
	// on, file, net, futex, idle, other, epoll
	times [CPUTYPE_MAX]uint64
}

// 查询某条具体trace的时间数据都是独立的，同一个线程在某段时间范围内具有trace排他性，有且只有一条trace，因此无法复用时间数据
// 选择最小计算量方式：1. 仅计算需要的tid；2. 仅计算trace时间范围内的数据
// 数据精度较高，精度取决于getCpuEvents能否获得相关的Cpu数据
func (ca *CpuAnalyzer) GetTimes(pid uint32, tid uint32, startTime uint64, endTime uint64) AggregatedTime {
	ca.lock.RLock()
	defer ca.lock.RUnlock()
	tidCpuEvents, exist := ca.cpuPidEvents[pid][tid]
	if !exist {
		return AggregatedTime{}
	}
	cpuEvents := ca.getCpuEvents(tidCpuEvents, startTime, endTime)

	return aggregateTime(cpuEvents, startTime, endTime)
}

func (ca *CpuAnalyzer) getCpuEvents(segments *TimeSegments, startTime uint64, endTime uint64) (events []TimedEvent) {
	maxSegmentSize := ca.cfg.SegmentSize
	startTimeSecond := startTime / nanoToSeconds
	endTimeSecond := endTime / nanoToSeconds
	if endTimeSecond < segments.BaseTime || startTimeSecond > segments.BaseTime+uint64(maxSegmentSize) {
		return
	}

	startIndex := int(startTimeSecond - segments.BaseTime)
	if startIndex < 0 {
		startIndex = 0
	}
	endIndex := endTimeSecond - segments.BaseTime
	if endIndex > segments.BaseTime+uint64(maxSegmentSize) {
		endIndex = segments.BaseTime + uint64(maxSegmentSize)
	}

	for i := startIndex; i <= int(endIndex) && i < maxSegmentSize; i++ {
		val := segments.Segments.GetByIndex(i)
		if val == nil {
			continue
		}
		segment := val.(*Segment)
		events = append(events, segment.CpuEvents...)
	}

	start := lowerBoundEvent(events, startTime) - 1
	if start < 0 {
		start = 0
	}
	end := lowerBoundEvent(events, endTime)

	return events[start:end]
}

func lowerBoundEvent(events []TimedEvent, targetTime uint64) int {
	end := len(events)
	if end == 0 {
		return 0
	}
	l, r := 0, end-1
	for l < r {
		mid := (l + r) / 2
		if events[mid].StartTimestamp() >= targetTime {
			r = mid
		} else {
			l = mid + 1
		}
	}
	if r == end {
		return end
	} else {
		return r
	}
}

func aggregateTime(cpuEvents []TimedEvent, startTime uint64, endTime uint64) (aggregatedTime AggregatedTime) {
	for cur := 0; cur < len(cpuEvents); cur++ {
		event := cpuEvents[cur].(*CpuEvent)
		currentTime := event.StartTimestamp()
		for i := 0; i < len(event.TypeSpecs); i++ {
			currentTime += event.TypeSpecs[i]
			if currentTime < startTime {
				continue
			}
			aggregatedTime.times[event.TimeType[i]] += event.TypeSpecs[i]

			if currentTime > endTime {
				break
			}
		}
	}

	return
}
