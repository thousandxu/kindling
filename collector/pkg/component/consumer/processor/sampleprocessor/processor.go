package sampleprocessor

import (
	"net"
	"strconv"
	"time"

	"github.com/Kindling-project/kindling/collector/pkg/component"
	"github.com/Kindling-project/kindling/collector/pkg/component/consumer"
	"github.com/Kindling-project/kindling/collector/pkg/component/consumer/processor"
	"github.com/Kindling-project/kindling/collector/pkg/model"
	"github.com/Kindling-project/kindling/collector/pkg/model/constlabels"
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
	// Check tailBase Traces and clean expired traceIds per second.
	go p.sampleCache.loopCheckTailBaseTraces()
	// Send local sampled traceIds to receiver
	// Get tailbase sampled traceIds from receiver
	go p.sampleCache.loopSendAndRecvTraces()
	return p
}

func (p *SampleProcessor) Consume(dataGroup *model.DataGroup) error {
	sampleTrace := NewSampleTrace(dataGroup, p.traceRetryNum)
	if p.sampleCache.isSampled(sampleTrace) {
		// Store Trace and Profiling
		p.sampleCache.storeProfiling(sampleTrace)
		sampleTrace.dataGroup.Labels.AddBoolValue(constlabels.IsProfiled, true)
		p.sampleCache.storeTrace(sampleTrace)
	} else if p.sampleCache.isTailBaseSampled(sampleTrace) {
		// Store Trace
		p.sampleCache.storeTrace(sampleTrace)
	} else {
		// Store datas into SampleCache for none-error, none slow or hit datas in N seconds.
		p.sampleCache.cacheSampleTrace(sampleTrace)
	}
	return nil
}
