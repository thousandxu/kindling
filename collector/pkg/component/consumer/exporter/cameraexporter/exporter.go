package cameraexporter

import (
	"go.uber.org/zap"

	"github.com/Kindling-project/kindling/collector/pkg/component"
	"github.com/Kindling-project/kindling/collector/pkg/component/consumer/exporter"
	"github.com/Kindling-project/kindling/collector/pkg/model"
)

const Type = "cameraexporter"

type CameraExporter struct {
	config *Config
	writer writer

	telemetry *component.TelemetryTools
}

func New(config interface{}, telemetry *component.TelemetryTools) exporter.Exporter {
	cfg, _ := config.(*Config)
	ret := &CameraExporter{
		config:    cfg,
		telemetry: telemetry,
	}
	switch cfg.Storage {
	case storageElasticsearch:
		writer, err := newEsWriter(cfg.EsConfig)
		if err != nil {
			telemetry.Logger.Panicf("Can't create new cameraexporter with eswriter: %v", err)
		}
		ret.writer = writer
	case storageFile:
		writer, err := newFileWriter(cfg.FileConfig, telemetry.Logger)
		if err != nil {
			telemetry.Logger.Panicf("Can't create new cameraexporter with filewriter: %v", err)
		}
		ret.writer = writer
	}
	return ret
}

func (e *CameraExporter) Consume(dataGroup *model.DataGroup) error {
	if ce := e.telemetry.Logger.Check(zap.DebugLevel, ""); ce != nil {
		e.telemetry.Logger.Debug(dataGroup.String())
	}
	e.writer.write(dataGroup)
	return nil
}

type writer interface {
	write(dataGroup *model.DataGroup)
	name() string
}
