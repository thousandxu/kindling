package cameraexporter

import (
	"fmt"

	"github.com/Kindling-project/kindling/collector/pkg/esclient"
	"github.com/Kindling-project/kindling/collector/pkg/model"
	"github.com/Kindling-project/kindling/collector/pkg/model/constlabels"
	"github.com/Kindling-project/kindling/collector/pkg/model/constnames"
)

type esWriter struct {
	config   *esConfig
	esClient *esclient.EsClient
}

func newEsWriter(cfg *esConfig) (*esWriter, error) {
	client, err := esclient.NewEsClient(cfg.EsHost)
	if err != nil {
		return nil, fmt.Errorf("fail to create elasticsearch client: %w", err)
	}
	ret := &esWriter{
		config:   cfg,
		esClient: client,
	}
	return ret, nil
}

func (ew *esWriter) write(group *model.DataGroup) {
	groupName := group.Name
	switch groupName {
	case constnames.SingleNetRequestMetricGroup:
		fallthrough
	case constnames.SpanTraceGroupName:
		fallthrough
	case constnames.CameraEventGroupName:
		ew.writeEs(group)
	default:
		return
	}
}

func (ew *esWriter) writeEs(group *model.DataGroup) {
	isSent := group.Labels.GetIntValue(constlabels.IsSent)
	// The data has been sent before, so esExporter will not index it again.
	// But fileExporter will.
	if isSent == 1 {
		return
	}
	index := group.Name
	if ew.config.IndexSuffix != "" {
		index = index + "_" + ew.config.IndexSuffix
	}
	ew.esClient.AddIndexRequestWithParams(index, group)
}

func (ew *esWriter) name() string {
	return storageElasticsearch
}
