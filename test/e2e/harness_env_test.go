//go:build e2e

package e2e

import (
	"context"
	"net"
	"net/http"
	"time"

	"google.golang.org/grpc"

	appsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/apps/v1"
	filesv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/files/v1"
	"github.com/agynio/telegram-connector/.gen/go/agynio/api/gateway/v1/gatewayv1connect"
	notificationsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/notifications/v1"
	organizationsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/organizations/v1"
	threadsv1 "github.com/agynio/telegram-connector/.gen/go/agynio/api/threads/v1"
	"github.com/agynio/telegram-connector/internal/connector"
	"github.com/agynio/telegram-connector/internal/gateway"
)

type localEnvironment struct {
	state             *testState
	store             *memoryStore
	appsAddr          string
	organizationsAddr string
	threadsAddr       string
	filesAddr         string
	notificationsAddr string
	gatewayAddr       string
	connectorAddr     string
	telegramAddr      string
	grpcServers       []*grpc.Server
	httpServers       []*http.Server
	connectorCancel   context.CancelFunc
}

func startLocalEnvironment() (*localEnvironment, error) {
	state := newTestState()
	env := &localEnvironment{
		state: state,
		store: newMemoryStore(),
	}

	appsAddr, appsServer, err := startGRPCServer(func(server *grpc.Server) {
		appsv1.RegisterAppsServiceServer(server, &appsService{state: state})
	})
	if err != nil {
		return nil, err
	}
	orgAddr, orgServer, err := startGRPCServer(func(server *grpc.Server) {
		organizationsv1.RegisterOrganizationsServiceServer(server, &organizationsService{state: state})
	})
	if err != nil {
		return nil, err
	}
	threadsAddr, threadsServer, err := startGRPCServer(func(server *grpc.Server) {
		threadsv1.RegisterThreadsServiceServer(server, &threadsService{state: state})
	})
	if err != nil {
		return nil, err
	}
	filesAddr, filesServer, err := startGRPCServer(func(server *grpc.Server) {
		filesv1.RegisterFilesServiceServer(server, &filesService{state: state})
	})
	if err != nil {
		return nil, err
	}
	notificationsAddr, notificationsServer, err := startGRPCServer(func(server *grpc.Server) {
		notificationsv1.RegisterNotificationsServiceServer(server, &notificationsService{state: state})
	})
	if err != nil {
		return nil, err
	}

	gatewayAddr, gatewayServer, err := startGatewayServer(state)
	if err != nil {
		return nil, err
	}

	telegramAddr, telegramServer, err := startTelegramMockServer()
	if err != nil {
		return nil, err
	}

	connectorAddr, connectorServer, err := startHealthServer()
	if err != nil {
		return nil, err
	}

	env.appsAddr = appsAddr
	env.organizationsAddr = orgAddr
	env.threadsAddr = threadsAddr
	env.filesAddr = filesAddr
	env.notificationsAddr = notificationsAddr
	env.gatewayAddr = gatewayAddr
	env.telegramAddr = telegramAddr
	env.connectorAddr = connectorAddr
	env.grpcServers = []*grpc.Server{appsServer, orgServer, threadsServer, filesServer, notificationsServer}
	env.httpServers = []*http.Server{gatewayServer, telegramServer, connectorServer}
	return env, nil
}

func (e *localEnvironment) StartConnector(appID, appIdentityID string) {
	client := gateway.NewClient(http.DefaultClient, "http://"+e.gatewayAddr, appIdentityID)
	service := connector.NewService(e.store, client, connector.ServiceConfig{
		AppID:           appID,
		TelegramBaseURL: "http://" + e.telegramAddr,
		PollTimeout:     2 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	e.connectorCancel = cancel
	go service.Run(ctx)
}

func (e *localEnvironment) Shutdown() {
	if e.connectorCancel != nil {
		e.connectorCancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, srv := range e.httpServers {
		_ = srv.Shutdown(ctx)
	}
	for _, srv := range e.grpcServers {
		srv.Stop()
	}
}

func startGRPCServer(register func(*grpc.Server)) (string, *grpc.Server, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	server := grpc.NewServer()
	register(server)
	go server.Serve(listener)
	return listener.Addr().String(), server, nil
}

func startGatewayServer(state *testState) (string, *http.Server, error) {
	mux := http.NewServeMux()
	appsPath, appsHandler := gatewayv1connect.NewAppsGatewayHandler(&appsGatewayServer{state: state})
	mux.Handle(appsPath, appsHandler)
	threadsPath, threadsHandler := gatewayv1connect.NewThreadsGatewayHandler(&threadsGatewayServer{state: state})
	mux.Handle(threadsPath, threadsHandler)
	filesPath, filesHandler := gatewayv1connect.NewFilesGatewayHandler(&filesGatewayServer{state: state})
	mux.Handle(filesPath, filesHandler)
	notifyPath, notifyHandler := gatewayv1connect.NewNotificationsGatewayHandler(&notificationsGatewayServer{state: state})
	mux.Handle(notifyPath, notifyHandler)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	return listener.Addr().String(), server, nil
}

func startHealthServer() (string, *http.Server, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	return listener.Addr().String(), server, nil
}
