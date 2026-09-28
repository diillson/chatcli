/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"
)

// The standard gRPC health service, next to ChatCLIService/Health.
//
// Kubelet gRPC probes, grpc-health-probe (shipped in the server image and
// used by its HEALTHCHECK) and most load balancers speak only
// grpc.health.v1.Health. Without it they get Unimplemented and report the
// server as down no matter how healthy it is.
//
// The service reports SERVING for the empty name (the whole server) and
// for chatcli.v1.ChatCLIService once the listener is up, and NOT_SERVING
// when shutdown begins, so a draining pod leaves the Service endpoints
// before its connections are cut.

// grpcHealthServicePrefix is the method prefix of grpc.health.v1.Health.
var grpcHealthServicePrefix = "/" + healthpb.Health_ServiceDesc.ServiceName + "/"

// isHealthMethod reports whether a method is a liveness or readiness
// probe, which every deployment must answer without credentials.
func isHealthMethod(method string) bool {
	return strings.HasSuffix(method, "/Health") || strings.HasPrefix(method, grpcHealthServicePrefix)
}

// registerHealthService registers grpc.health.v1.Health on the server.
// Everything starts NOT_SERVING until markServing is called.
func registerHealthService(gs *grpc.Server) *health.Server {
	hs := health.NewServer()
	for _, name := range healthServiceNames() {
		hs.SetServingStatus(name, healthpb.HealthCheckResponse_NOT_SERVING)
	}
	healthpb.RegisterHealthServer(gs, hs)
	return hs
}

// healthServiceNames are the names the health service answers for.
func healthServiceNames() []string {
	return []string{"", pb.ChatCLIService_ServiceDesc.ServiceName}
}

// setHealth moves every reported service to the given status.
func setHealth(hs *health.Server, st healthpb.HealthCheckResponse_ServingStatus) {
	if hs == nil {
		return
	}
	for _, name := range healthServiceNames() {
		hs.SetServingStatus(name, st)
	}
}
