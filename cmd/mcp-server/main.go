package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"

	"mcp-incident-commander/internal/server"
	"mcp-incident-commander/internal/store"
)

// Ortam değişkeni anahtarları ve sunucu zaman aşımı sabitleri.
const (
	envLambdaRuntimeAPI = "AWS_LAMBDA_RUNTIME_API"
	envLambdaLogStream  = "AWS_LAMBDA_LOG_STREAM_NAME"
	envJWTSigningSecret = "JWT_SIGNING_SECRET"
	envDynamoDBTable    = "DYNAMODB_TABLE_NAME"
	envInstanceID       = "INSTANCE_ID"
	envPort             = "PORT"

	defaultHTTPPort     = "8080"
	readHeaderTimeout   = 10 * time.Second
	writeTimeout        = 60 * time.Second
	idleTimeout         = 120 * time.Second
	shutdownGracePeriod = 10 * time.Second
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	jwtSecret := strings.TrimSpace(os.Getenv(envJWTSigningSecret))
	if jwtSecret == "" {
		logger.Error("missing required environment variable", slog.String("env", envJWTSigningSecret))
		os.Exit(1)
	}

	stateStore, err := initStateStore(ctx, logger)
	if err != nil {
		logger.Error("failed to initialize state store", slog.String("error", err.Error()))
		os.Exit(1)
	}

	instanceID := resolveInstanceID()
	router, err := server.NewRouter(server.Config{
		ServerName:       server.DefaultServerName,
		ServerVersion:    server.DefaultServerVersion,
		InstanceID:       instanceID,
		JWTSigningSecret: []byte(jwtSecret),
		Store:            stateStore,
		Logger:           logger,
	})
	if err != nil {
		logger.Error("failed to create MCP HTTP router", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// AWS_LAMBDA_RUNTIME_API tanımlıysa AWS Lambda (ALB Target Group) modunda başlat.
	if strings.TrimSpace(os.Getenv(envLambdaRuntimeAPI)) != "" {
		logger.Info("starting mcp-incident-commander in AWS Lambda ALB mode",
			slog.String("instance_id", instanceID),
		)
		albAdapter := httpadapter.NewALB(router)
		lambda.Start(albAdapter.ProxyWithContext)
		return
	}

	// Tanımlı değilse yerel HTTP sunucusu (veya K8s container) modunda başlat.
	port := strings.TrimSpace(os.Getenv(envPort))
	if port == "" {
		port = defaultHTTPPort
	}

	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           router,
		ReadHeaderTimeout: readHeaderTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting mcp-incident-commander in HTTP server mode",
			slog.String("addr", httpServer.Addr),
			slog.String("instance_id", instanceID),
		)
		if serveErr := httpServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down HTTP server gracefully")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", slog.String("error", err.Error()))
		}
	case err := <-errCh:
		if err != nil {
			logger.Error("HTTP server terminated with error", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}
}

func initStateStore(ctx context.Context, logger *slog.Logger) (store.StateStore, error) {
	tableName := strings.TrimSpace(os.Getenv(envDynamoDBTable))
	if tableName == "" {
		logger.Info("DYNAMODB_TABLE_NAME not set; using thread-safe InMemoryStore")
		return store.NewInMemoryStore(), nil
	}

	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}

	ddbClient := dynamodb.NewFromConfig(awsCfg)
	logger.Info("initialized AWS DynamoDB state store", slog.String("table", tableName))
	return store.NewDynamoDBStore(ddbClient, tableName)
}

func resolveInstanceID() string {
	if id := strings.TrimSpace(os.Getenv(envInstanceID)); id != "" {
		return id
	}
	if stream := strings.TrimSpace(os.Getenv(envLambdaLogStream)); stream != "" {
		return stream
	}
	if host, err := os.Hostname(); err == nil && strings.TrimSpace(host) != "" {
		return host
	}
	return "stateless-node-local"
}
