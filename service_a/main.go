package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/render"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
)

type CepRequest struct {
	Cep string `json:"cep"`
}

func (req *CepRequest) Bind(r *http.Request) error {
	if req.Cep == "" {
		return errors.New("cep field is missing")
	}

	req.Cep = strings.Replace(req.Cep, "-", "", -1)

	if len(req.Cep) != 8 {
		return errors.New("invalid zipcode")
	}
	return nil
}

var tracer = otel.Tracer("challenge-weather-by-cep-otel")

func main() {
	tp := initTracer()
	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			log.Printf("Error shutting down tracer provider: %v", err)
		}
	}()

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.Recoverer)
	router.Use(middleware.Logger)
	router.Use(middleware.Timeout(60 * time.Second))
	router.Use(render.SetContentType(render.ContentTypeJSON))
	router.Post("/", handleRequest)
	if err := http.ListenAndServe(":8080", router); err != nil {
		log.Fatal(err)
	}
}

func handleRequest(w http.ResponseWriter, r *http.Request) {
	data := &CepRequest{}
	if err := render.Bind(r, data); err != nil {
		w.WriteHeader(422)
		w.Write([]byte(err.Error()))
		return
	}
	cep := data.Cep

	carrier := propagation.HeaderCarrier(r.Header)
	ctx := r.Context()
	ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)
	ctx, span := tracer.Start(ctx, "request-service-a")
	defer span.End()

	url := "http://service-b:8081/" + cep
	response, err := fetchData(ctx, url)
	if err != nil {
		fmt.Printf("ERROR: %s\n", err.Error())
		w.WriteHeader(500)
		w.Write([]byte("internal server error"))
		return
	}

	w.WriteHeader(200)
	w.Write(response)
}

func fetchData(c context.Context, url string) (response []byte, err error) {
	res, _ := otelhttp.Get(c, url)
	body, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil {
		return nil, err
	}

	return body, nil
}

func initTracer() *sdktrace.TracerProvider {
	ctx := context.Background()
	conn, err := grpc.DialContext(ctx, "collector:4317",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		log.Fatal("failed to create gRPC connection to collector: %w", err)
	}
	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		log.Fatal("failed to create trace exporter: %w", err)
	}
	if err != nil {
		log.Fatal(err)
	}
	resource, _ := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String("service-a"),
		),
	)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return tp
}
