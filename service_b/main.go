package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

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
			semconv.ServiceNameKey.String("service_b"),
		)),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	tracer = tp.Tracer("service_b")
}

func getCityFromCEP(cep string) (string, error) {
	resp, err := http.Get("https://viacep.com.br/ws/" + cep + "/json/")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(body, &result)

	if result["erro"] != nil {
		return "", fmt.Errorf("CEP not found")
	}

	return result["localidade"].(string), nil
}

func getTemperature(city string) (float64, error) {
	resp, err := http.Get("http://api.weatherapi.com/v1/current.json?key=66f074cb079d40c1bee04932241302&q=" + city)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(body, &result)

	tempC := result["current"].(map[string]interface{})["temp_c"].(float64)
	return tempC, nil
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

	city, err := getCityFromCEP(input.CEP)
	if err != nil {
		http.Error(w, `{"message": "can not find zipcode"}`, http.StatusNotFound)
		return
	}

	tempC, err := getTemperature(city)
	if err != nil {
		http.Error(w, "failed to get temperature", http.StatusInternalServerError)
		return
	}

	tempF := tempC*1.8 + 32
	tempK := tempC + 273

	response := map[string]interface{}{
		"city":   city,
		"temp_C": tempC,
		"temp_F": tempF,
		"temp_K": tempK,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

func main() {
	initTracer()

	http.HandleFunc("/weather", handler)
	fmt.Println("Service B running on :8081")
	http.ListenAndServe(":8081", nil)
}
