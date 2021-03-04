// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package otlp

import (
	"context"
	"sync"

	"github.com/pkg/errors"
	"go.opentelemetry.io/collector/consumer/pdata"
	"go.opentelemetry.io/collector/receiver/otlpreceiver"
	"go.opentelemetry.io/collector/receiver/otlpreceiver/metrics"
	"go.opentelemetry.io/collector/receiver/otlpreceiver/trace"
	"google.golang.org/grpc"

	"github.com/elastic/apm-server/beater/request"
	"github.com/elastic/apm-server/model"
	"github.com/elastic/apm-server/processor/otel"
	"github.com/elastic/beats/v7/libbeat/logp"
	"github.com/elastic/beats/v7/libbeat/monitoring"
)

var (
	monitoringKeys = []request.ResultID{
		request.IDRequestCount, request.IDResponseCount, request.IDResponseErrorsCount, request.IDResponseValidCount,
	}

	gRPCMetricsRegistry      = monitoring.Default.NewRegistry("apm-server.otlp.grpc.metrics")
	gRPCMetricsMonitoringMap = request.MonitoringMapForRegistry(gRPCMetricsRegistry, monitoringKeys)
	gRPCTracesRegistry       = monitoring.Default.NewRegistry("apm-server.otlp.grpc.traces")
	gRPCTracesMonitoringMap  = request.MonitoringMapForRegistry(gRPCTracesRegistry, monitoringKeys)
)

func init() {
	monitoring.NewFunc(gRPCMetricsRegistry, "consumer", collectMetricsMonitoring, monitoring.Report)
}

// RegisterGRPCServices registers OTLP consumer services with the given gRPC server.
func RegisterGRPCServices(grpcServer *grpc.Server, processor model.BatchProcessor, logger *logp.Logger) error {
	consumer := &monitoredConsumer{
		consumer: &otel.Consumer{Processor: processor},
		logger:   logger,
	}

	// TODO(axw) stop assuming we have only one OTLP gRPC service running
	// at any time, and instead aggregate metrics from consumers that are
	// dynamically registered and unregistered.
	setCurrentMonitoredConsumer(consumer)

	traceReceiver := trace.New("otlp", consumer)
	metricsReceiver := metrics.New("otlp", consumer)
	if err := otlpreceiver.RegisterTraceReceiver(context.Background(), traceReceiver, grpcServer, nil); err != nil {
		return errors.Wrap(err, "failed to register OTLP trace receiver")
	}
	if err := otlpreceiver.RegisterMetricsReceiver(context.Background(), metricsReceiver, grpcServer, nil); err != nil {
		return errors.Wrap(err, "failed to register OTLP metrics receiver")
	}
	return nil
}

type monitoredConsumer struct {
	consumer *otel.Consumer
	logger   *logp.Logger
}

// ConsumeTraces consumes OpenTelemtry trace data.
func (c *monitoredConsumer) ConsumeTraces(ctx context.Context, traces pdata.Traces) error {
	gRPCTracesMonitoringMap[request.IDRequestCount].Inc()
	defer gRPCTracesMonitoringMap[request.IDResponseCount].Inc()
	if err := c.consumer.ConsumeTraces(ctx, traces); err != nil {
		gRPCTracesMonitoringMap[request.IDResponseErrorsCount].Inc()
		c.logger.With(logp.Error(err)).Error("ConsumeTraces returned an error")
		return err
	}
	gRPCTracesMonitoringMap[request.IDResponseValidCount].Inc()
	return nil
}

// ConsumeMetrics consumes OpenTelemtry metrics data.
func (c *monitoredConsumer) ConsumeMetrics(ctx context.Context, metrics pdata.Metrics) error {
	gRPCMetricsMonitoringMap[request.IDRequestCount].Inc()
	defer gRPCMetricsMonitoringMap[request.IDResponseCount].Inc()
	if err := c.consumer.ConsumeMetrics(ctx, metrics); err != nil {
		gRPCMetricsMonitoringMap[request.IDResponseErrorsCount].Inc()
		c.logger.With(logp.Error(err)).Error("ConsumeMetrics returned an error")
		return err
	}
	gRPCMetricsMonitoringMap[request.IDResponseValidCount].Inc()
	return nil
}

func (c *monitoredConsumer) collectMetricsMonitoring(_ monitoring.Mode, V monitoring.Visitor) {
	V.OnRegistryStart()
	V.OnRegistryFinished()

	stats := c.consumer.Stats()
	monitoring.ReportNamespace(V, "consumer", func() {
		monitoring.ReportInt(V, "unsupported_dropped", stats.UnsupportedMetricsDropped)
	})
}

var (
	currentMonitoredConsumerMu sync.RWMutex
	currentMonitoredConsumer   *monitoredConsumer
)

func setCurrentMonitoredConsumer(c *monitoredConsumer) {
	currentMonitoredConsumerMu.Lock()
	defer currentMonitoredConsumerMu.Unlock()
	currentMonitoredConsumer = c
}

func collectMetricsMonitoring(mode monitoring.Mode, V monitoring.Visitor) {
	currentMonitoredConsumerMu.RLock()
	c := currentMonitoredConsumer
	currentMonitoredConsumerMu.RUnlock()
	if c != nil {
		c.collectMetricsMonitoring(mode, V)
	}
}
