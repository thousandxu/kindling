package sampleprocessor

import (
	"net"
	"strconv"
	"time"

	"github.com/Kindling-project/kindling/collector/pkg/component"
	"github.com/Kindling-project/kindling/collector/pkg/component/consumer"
	"github.com/Kindling-project/kindling/collector/pkg/component/consumer/processor"
	"github.com/Kindling-project/kindling/collector/pkg/model"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

const (
	Sample = "sampleprocessor"
)

type SampleProcessor struct {
	cfg           *Config
	sampleCache   *SampleCache
	traceRetryNum int
}

func New(config interface{}, telemetry *component.TelemetryTools, nextConsumer consumer.Consumer) processor.Processor {
	cfg := config.(*Config)

	var kacp = keepalive.ClientParameters{
		Time:                5 * time.Second, // send pings every 5 seconds if there is no activity
		Timeout:             time.Second,     // wait 1 second for ping ack before considering the connection dead
		PermitWithoutStream: true,            // send pings even without active streams
	}
	conn, err := grpc.Dial(net.JoinHostPort(cfg.ReceiverIp, strconv.Itoa(cfg.ReceiverPort)),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(kacp))
	if err != nil {
		if ce := telemetry.Logger.Check(zapcore.WarnLevel, "Fail to Create GrpcClient: "); ce != nil {
			ce.Write(
				zap.String("ip", cfg.ReceiverIp),
				zap.Error(err),
			)
		}
		return nil
	}

	p := &SampleProcessor{
		cfg:           cfg,
		sampleCache:   NewSampleCache(model.NewTraceIdServiceClient(conn), cfg, telemetry, nextConsumer),
		traceRetryNum: cfg.SampleTraceRepeatNum,
	}
	// 每1s 校验TailBase Trace数据，并清除失效TraceIds.
	go p.sampleCache.loopCheckTailBaseTraces()
	// 每1s 将本机采样的traceIds发送给服务端，并获取Tail TraceIds数据.
	go p.sampleCache.loopSendAndRecvTraces()
	return p
}

func (p *SampleProcessor) Consume(dataGroup *model.DataGroup) error {
	sampleTrace := NewSampleTrace(dataGroup, p.traceRetryNum)
	if p.sampleCache.isSampled(sampleTrace) {
		// 保存Trace 和 Profiling
		p.sampleCache.storeProfiling(sampleTrace)
		p.sampleCache.storeTrace(sampleTrace)
	} else if p.sampleCache.isTailBaseSampled(sampleTrace) {
		// 只保留Trace数据
		p.sampleCache.storeTrace(sampleTrace)
	} else {
		// 非错 或 慢 或 URL5s内已采中, 将数据存储到SampleCache中
		p.sampleCache.cacheSampleTrace(sampleTrace)
	}
	return nil
}
