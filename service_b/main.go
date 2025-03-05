package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
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

const (
	urlViaCep     = "https://viacep.com.br/"
	urlWeatherApi = "https://api.weatherapi.com/v1/"
	weatherApiKey = "66f074cb079d40c1bee04932241302"
)

type ViaCepResponse struct {
	Erro        string `json:"erro"`
	Cep         string `json:"cep"`
	Logradouro  string `json:"logradouro"`
	Complemento string `json:"complemento"`
	Bairro      string `json:"bairro"`
	Localidade  string `json:"localidade"`
	Uf          string `json:"uf"`
	Ibge        string `json:"ibge"`
	Gia         string `json:"gia"`
	Ddd         string `json:"ddd"`
	Siafi       string `json:"siafi"`
}

type WeatherApiResponse struct {
	Location struct {
		Name           string  `json:"name"`
		Region         string  `json:"region"`
		Country        string  `json:"country"`
		Lat            float64 `json:"lat"`
		Lon            float64 `json:"lon"`
		TzID           string  `json:"tz_id"`
		LocaltimeEpoch int     `json:"localtime_epoch"`
		Localtime      string  `json:"localtime"`
	} `json:"location"`
	Current struct {
		LastUpdatedEpoch int     `json:"last_updated_epoch"`
		LastUpdated      string  `json:"last_updated"`
		TempC            float64 `json:"temp_c"`
		TempF            float64 `json:"temp_f"`
		IsDay            int     `json:"is_day"`
		Condition        struct {
			Text string `json:"text"`
			Icon string `json:"icon"`
			Code int    `json:"code"`
		} `json:"condition"`
		WindMph    float64 `json:"wind_mph"`
		WindKph    float64 `json:"wind_kph"`
		WindDegree int     `json:"wind_degree"`
		WindDir    string  `json:"wind_dir"`
		PressureMb float64 `json:"pressure_mb"`
		PressureIn float64 `json:"pressure_in"`
		PrecipMm   float64 `json:"precip_mm"`
		PrecipIn   float64 `json:"precip_in"`
		Humidity   int     `json:"humidity"`
		Cloud      int     `json:"cloud"`
		FeelslikeC float64 `json:"feelslike_c"`
		FeelslikeF float64 `json:"feelslike_f"`
		VisKm      float64 `json:"vis_km"`
		VisMiles   float64 `json:"vis_miles"`
		Uv         float64 `json:"uv"`
		GustMph    float64 `json:"gust_mph"`
		GustKph    float64 `json:"gust_kph"`
	} `json:"current"`
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
	router.Get("/{cep}", handleRequest)
	if err := http.ListenAndServe(":8081", router); err != nil {
		log.Fatal(err)
	}
}

func handleRequest(w http.ResponseWriter, r *http.Request) {
	cep := chi.URLParam(r, "cep")
	cep = strings.Replace(cep, "-", "", -1)

	carrier := propagation.HeaderCarrier(r.Header)
	ctx := r.Context()
	ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)
	ctx, span := tracer.Start(ctx, "request-service-b")
	defer span.End()

	url := urlViaCep + "ws/" + cep + "/json"
	respCep, err := fetchData(ctx, url)
	if err != nil {
		fmt.Printf("ERROR: %s\n", err.Error())
		w.WriteHeader(500)
		w.Write([]byte("Error fetching zipcode data"))
		return
	}

	cepResponse := ViaCepResponse{}
	err = json.Unmarshal(respCep, &cepResponse)
	if err != nil {
		fmt.Printf("ERROR: %s\n", err.Error())
		w.WriteHeader(500)
		w.Write([]byte("Error parsing zipcode data"))
		return
	}

	if cepResponse.Erro == "true" {
		w.WriteHeader(404)
		w.Write([]byte("can not find zipcode"))
		return
	}

	city := string(cepResponse.Localidade)
	city = removeAccents(city)
	state := string(cepResponse.Uf)

	url = urlWeatherApi + "current.json?key=" + weatherApiKey + "&q=" + city + " - " + state + " - Brazil&aqi=no&tides=no"
	url = strings.Replace(url, " ", "%20", -1)

	respWeather, err := fetchData(ctx, url)
	if err != nil {
		fmt.Printf("ERROR: %s\n", err.Error())
		w.WriteHeader(500)
		w.Write([]byte("internal server error"))
		return
	}
	weatherResponse := WeatherApiResponse{}
	err = json.Unmarshal(respWeather, &weatherResponse)
	if err != nil {
		fmt.Printf("ERROR: %s\n", err.Error())
		w.WriteHeader(500)
		w.Write([]byte("internal server error"))
		return
	}

	tempC := strconv.FormatFloat(weatherResponse.Current.TempC, 'f', -1, 64)
	tempF := strconv.FormatFloat(weatherResponse.Current.TempF, 'f', -1, 64)
	tempK := strconv.FormatFloat(weatherResponse.Current.TempC+273.15, 'f', -1, 64)

	response := []byte(`{ "city: ` + city + `/` + state + `, "temp_C": ` + tempC + `, "temp_F": ` + tempF + `, "temp_K": ` + tempK + ` }`)

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

func removeAccents(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	output, _, e := transform.String(t, s)
	if e != nil {
		panic(e)
	}
	return output
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
			semconv.ServiceNameKey.String("service-b"),
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
