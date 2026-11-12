// A Go app with the stock OpenTelemetry SDK, configured only by the
// OTEL_* variables Dock injects when tracing is on: the Go runtime's
// metrics, and with BOMB set, 15 s after it starts, a counter with 2,000
// values of an attribute (a cardinality bomb, for Dock's series cap).
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

func main() {
	ctx := context.Background()
	reader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		log.Fatal(err)
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() { _ = mp.Shutdown(ctx) }()
	otel.SetMeterProvider(mp)
	if err := runtime.Start(); err != nil {
		log.Fatal(err)
	}
	if os.Getenv("BOMB") != "" {
		bombs, _ := otel.Meter("go-otel").Int64Counter("bomb.hits")
		time.AfterFunc(15*time.Second, func() {
			for i := range 2000 { // the SDK's own cap a metric
				bombs.Add(ctx, 1, metric.WithAttributes(attribute.String("user.id", strconv.Itoa(i))))
			}
		})
	}
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("go-otel\n")) })
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
