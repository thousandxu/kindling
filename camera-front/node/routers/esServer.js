
'use strict';
const router = require('express').Router();
const _ = require('lodash');
const fs = require('fs');
const esClientService = require('./esquery');
const esService = new esClientService();
const { Client } = require('@elastic/elasticsearch');

const esServerConfig = require('../settings').esServerConfig;
const esClient = new Client({ node: `http://${esServerConfig.host}:${esServerConfig.port}` });

function handleData(data) {
    let nodes = [], edges = [];
    _.forEach(data, item => {
        let metric = _.find(item.metrics, { Name: 'request_total_time' });
        if (_.findIndex(nodes, {apm_span_ids: item.labels.apm_span_ids}) === -1) {
            let node = {
                content_key: item.labels.content_key,
                id: item.labels.apm_span_ids,
                apm_span_ids: item.labels.apm_span_ids,
                dst_container: item.labels.dst_container,
                dst_pod: item.labels.dst_pod,
                dst_workload_name: item.labels.dst_workload_name,
                is_profiled: item.labels.is_profiled,
                pid: item.labels.pid,
                protocol: item.labels.protocol,
                list: [
                    {
                        endTime: parseInt(item.timestamp / 1000000),
                        totalTime: metric ? metric.Data.Value : 0,
                        p90: item.labels.p90 | 0
                    }
                ]
            }
            nodes.push(node);
        } else {
            let node = _.find(nodes, {apm_span_ids: item.labels.apm_span_ids});
            if (_.findIndex(node.list, {endTime: item.labels.endTime}) === -1) {
                node.list.push({
                    endTime: parseInt(item.timestamp / 1000000),
                    totalTime: metric ? metric.Data.Value : 0,
                    p90: item.labels.p90 | 0,
                });
            }
        }
    });
    _.forEach(data, item => {
        if (item.labels.apm_parent_id !== '0') {
            let sourceNode = _.find(nodes, node => node.apm_span_ids.split(',').indexOf(item.labels.apm_parent_id) > -1);
            if (sourceNode) {
                let sourceId = sourceNode.apm_span_ids;
                if (_.findIndex(edges, {source: sourceId, target: item.labels.apm_span_ids}) === -1) {
                    let edge = {
                        source: sourceId,
                        target: item.labels.apm_span_ids
                    }
                    edges.push(edge);
                }
            }
        }
    });
    return { nodes, edges };
}

router.get('/getTraceData', async(req, res, next) => {
    const { traceId } = req.query;
    console.log(traceId);
    // try {
    //     let result = await esService.getEsData('camera_event_group_proc', 'labels.tid', 24512, 10);
    //     console.log('result', result);
    // } catch (error) {
    //     console.log('我报错了 哈哈哈哈哈哈');
    //     console.log('error', error)
    // }
    
    esClient.search({
        index: 'span_trace_group_dev',
        // 如果您使用的是Elasticsearch≤6，取消对这一行的注释
        // type: '_doc',
        body: {
            query: {
                match: { 'labels.trace_id': traceId }
            }
        },
    }, {
        headers: {
            'content-type': 'application/json'
        }
    }, (err, result) => {
        if (err) {
            console.log(err);
            res.status(504).json({
                success: false,
                data: err
            });
        } else {
            let hits = result.body.hits.hits;
            let data = _.map(hits, '_source');
            let finalResult = handleData(data);

            res.status(200).json({
                success: true,
                data: finalResult
            });
        }        
    });
}); 



// router.get('/getTestTraceData', async(req, res, next) => {
//     let result = handleData(mockData);
//     res.status(200).json({
//         success: true,
//         data: result
//     });
// }); 
module.exports = router;