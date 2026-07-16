/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package router

import (
	"net/http"
	"strconv"
	"time"

	_ "github.com/NVIDIA/nvcf/src/control-plane-services/nats-auth-callout/api"

	ginzap "github.com/gin-contrib/zap"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
	"go.uber.org/zap"
)

// Config holds the configuration for the router
type Config struct {
	ServiceName    string
	TracingEnabled bool
	Metrics        *MetricsConfig
}

// MetricsConfig holds metrics-specific configuration
type MetricsConfig struct {
	Enabled bool
	Port    string
}

// Router wraps the gin.Engine with additional functionality
type Router struct {
	engine *gin.Engine
	logger *zap.Logger
	config *Config
}

// New creates a new router with the given logger and config
func New(logger *zap.Logger, config *Config) *Router {
	// set gin mode to release
	gin.SetMode(gin.ReleaseMode)

	// create gin router
	engine := gin.New()

	// add middleware
	engine.Use(ginzap.Ginzap(logger, time.RFC3339, true))
	engine.Use(ginzap.RecoveryWithZap(logger, true))

	r := &Router{
		engine: engine,
		logger: logger,
		config: config,
	}

	// add OpenTelemetry middleware if tracing is enabled
	if config.TracingEnabled {
		engine.Use(otelgin.Middleware(config.ServiceName))
		logger.Info("OpenTelemetry middleware enabled", zap.String("service", config.ServiceName))
	}

	// add prometheus middleware if metrics are enabled
	if config.Metrics != nil && config.Metrics.Enabled {
		r.engine.Use(r.prometheusMiddleware())
		logger.Info("Prometheus metrics enabled", zap.String("port", config.Metrics.Port))
	}

	r.setupRoutes()
	return r
}

// Engine returns the underlying gin.Engine
func (r *Router) Engine() *gin.Engine {
	return r.engine
}

// GetMetricsHandler returns a handler for metrics endpoint
func (r *Router) GetMetricsHandler() http.Handler {
	if r.config.Metrics != nil && r.config.Metrics.Enabled {
		return promhttp.Handler()
	}
	return http.NotFoundHandler()
}

// prometheusMiddleware creates a middleware for recording prometheus metrics
func (r *Router) prometheusMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// skip metrics collection for healthz and metrics endpoints
		path := c.Request.URL.Path
		if path == "/healthz" || path == "/metrics" {
			c.Next()
			return
		}

		start := time.Now()
		c.Next()

		// record request duration and count
		duration := time.Since(start).Seconds()
		status := strconv.Itoa(c.Writer.Status())
		method := c.Request.Method

		DurationHistogram.WithLabelValues(path, method, status).Observe(duration)
		RequestCounter.WithLabelValues(path, method, status).Inc()
	}
}

// setupRoutes configures all the routes
func (r *Router) setupRoutes() {
	// Health check interface (no version)
	r.engine.GET("/healthz", r.handleHealthz)

	// Note: Metrics endpoint is now served on a separate port via GetMetricsHandler()
	// and not included in the main application routes

	// Version v1 group
	v1 := r.engine.Group("/v1")
	{
		// Ping interface
		v1.GET("/ping", r.handlePing)
	}

	// add root path redirect to v1/ping
	r.engine.GET("/", r.handleRoot)

	// Add routes
	r.engine.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "ok",
		})
	})

	r.engine.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "pong",
		})
	})

	// Swagger documentation
	r.engine.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

}

// handlePing handle ping endpoint
// @Summary		Ping interface
// @Description	return pong response, for testing if the service is running
// @Tags			Health
// @Accept			json
// @Produce		json
// @Security		BasicAuth
// @Success		200	{object}	models.PingResponse		"Success response"
// @Failure		400	{object}	models.ErrorResponse	"Bad request"
// @Failure		401	{object}	models.ErrorResponse	"Unauthorized"
// @Failure		429	{object}	models.ErrorResponse	"Too many requests"
// @Failure		500	{object}	models.ErrorResponse	"Internal server error"
// @Router			/v1/ping [get]
func (r *Router) handlePing(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"message":   "pong",
		"service":   r.config.ServiceName,
		"timestamp": time.Now().Unix(),
	})
}

// handleHealthz handle health check endpoint
// @Summary		Health check interface
// @Description	return service health status
// @Tags			Health
// @Accept			json
// @Produce		json
// @Security		BasicAuth
// @Success		200	{object}	models.HealthResponse	"Health check success"
// @Failure		400	{object}	models.ErrorResponse	"Bad request"
// @Failure		401	{object}	models.ErrorResponse	"Unauthorized"
// @Failure		429	{object}	models.ErrorResponse	"Too many requests"
// @Failure		500	{object}	models.ErrorResponse	"Internal server error"
// @Router			/healthz [get]
func (r *Router) handleHealthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
	})
}

// handleRoot handles the root path
func (r *Router) handleRoot(c *gin.Context) {
	c.Redirect(http.StatusMovedPermanently, "/v1/ping")
}
