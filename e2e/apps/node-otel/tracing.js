// The stock OpenTelemetry Node SDK with auto-instrumentation, configured
// only by the OTEL_* variables Dock injects when tracing is on.
const { NodeSDK } = require("@opentelemetry/sdk-node");
const { OTLPTraceExporter } = require("@opentelemetry/exporter-trace-otlp-proto");
const { getNodeAutoInstrumentations } = require("@opentelemetry/auto-instrumentations-node");

new NodeSDK({
  traceExporter: new OTLPTraceExporter(),
  instrumentations: [getNodeAutoInstrumentations({ "@opentelemetry/instrumentation-fs": { enabled: false } })],
}).start();
