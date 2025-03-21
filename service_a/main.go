package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/zipkin"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.opentelemetry.io/otel/trace"
)

var tracer trace.Tracer

func initTracer() {
	exporter, err := zipkin.New("http://zipkin:9411/api/v2/spans")
	if err != nil {
		panic(err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String("service_a"),
		)),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	tracer = tp.Tracer("service_a")
}

func isValidCEP(cep string) bool {
	match, _ := regexp.MatchString(`^\d{8}$`, cep)
	return match
}

func forwardToServiceB(cep string, parentCtx context.Context) (string, error) {
	ctx, span := tracer.Start(parentCtx, "forward-to-service-b", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	reqBody, _ := json.Marshal(map[string]string{"cep": cep})
	req, _ := http.NewRequest("POST", "http://service_b:8081/weather", bytes.NewBuffer(reqBody))
	req.Header.Set("Content-Type", "application/json")

	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	return string(body), nil
}

func handler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	span := trace.SpanFromContext(ctx)
	defer span.End()

	var input struct {
		CEP string `json:"cep"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid input", http.StatusBadRequest)
		return
	}

	if !isValidCEP(input.CEP) {
		http.Error(w, `{"message": "invalid zipcode"}`, http.StatusUnprocessableEntity)
		return
	}

	response, err := forwardToServiceB(input.CEP, ctx)
	if err != nil {
		http.Error(w, "service b unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(response))
}

func main() {
	initTracer()

	http.HandleFunc("/cep", handler)
	fmt.Println("Service A running on :8080")
	http.ListenAndServe(":8080", nil)
}
