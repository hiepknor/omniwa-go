package telemetry

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/netguard"
	"github.com/gin-gonic/gin"
)

var telemetryRequester = mustTelemetryRequester()

func mustTelemetryRequester() netguard.Requester {
	requester, err := netguard.NewRequester(netguard.RequestSettings{
		AllowedHosts: []string{"log.evolution-api.com"}, Timeout: 3 * time.Second,
		MaxRequestBytes: 16 * 1024, MaxResponseBytes: 64 * 1024,
	})
	if err != nil {
		panic(err)
	}
	return requester
}

type TelemetryData struct {
	Route      string    `json:"route"`
	APIVersion string    `json:"apiVersion"`
	Timestamp  time.Time `json:"timestamp"`
}

type telemetryService struct{}

func (t *telemetryService) TelemetryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		route := c.FullPath()
		go SendTelemetry(route)
		c.Next()
	}
}

type TelemetryService interface {
	TelemetryMiddleware() gin.HandlerFunc
}

func SendTelemetry(route string) {
	if route == "/" {
		return
	}

	telemetry := TelemetryData{
		Route:      route,
		APIVersion: "evo-go",
		Timestamp:  time.Now(),
	}

	url := "https://log.evolution-api.com/telemetry"

	data, err := json.Marshal(telemetry)
	if err != nil {
		log.Println("Erro ao serializar telemetria:", err)
		return
	}

	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	_, err = telemetryRequester.Do(context.Background(), http.MethodPost, url, header, data)
	if err != nil {
		log.Println("Erro ao enviar telemetria:", err)
	}
}

func NewTelemetryService() TelemetryService {
	return &telemetryService{}
}
