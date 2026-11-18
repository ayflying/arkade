package dbdiag_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"arkade/hub/internal/dbdiag"
)

func TestParseEndpointFromURL(t *testing.T) {
	ep := dbdiag.ParseEndpoint("postgres://arkade:P%40ss@10.7.122.184:5433/arkade?sslmode=disable")
	if ep.Host != "10.7.122.184" || ep.Port != "5433" || ep.Database != "arkade" || ep.User != "arkade" || ep.SSLMode != "disable" {
		t.Fatalf("unexpected endpoint %+v", ep)
	}
	if got := ep.String(); got != "postgres://arkade@10.7.122.184:5433/arkade?sslmode=disable" {
		t.Fatalf("endpoint string leaks or differs: %s", got)
	}
	if strings.Contains(ep.String(), "P%40ss") || strings.Contains(ep.String(), "P@ss") {
		t.Fatal("password must never appear in endpoint")
	}
}

func TestParseEndpointFromKeywordDSN(t *testing.T) {
	ep := dbdiag.ParseEndpoint("host = 10.7.122.184\nport = 5433\ndatabase = arkade\nuser = arkade\npassword = Ark@9fQ2mX7#Lp4\nsslmode = disable")
	if ep.Host != "10.7.122.184" || ep.Port != "5433" || ep.Database != "arkade" || ep.User != "arkade" {
		t.Fatalf("unexpected endpoint %+v", ep)
	}
	if strings.Contains(ep.String(), "9fQ2mX7") {
		t.Fatal("password leaked into endpoint string")
	}
}

// 端口不可达时必须明确告诉运维是网络问题，而不是密码问题。
func TestProbeReportsNetworkFailureWithoutCredentials(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := dbdiag.Probe(ctx, "postgres://arkade:TOP_SECRET_VALUE@127.0.0.1:1/arkade?sslmode=disable&connect_timeout=2")
	if err == nil {
		t.Fatal("expected failure on closed port")
	}
	message := err.Error()
	if strings.Contains(message, "TOP_SECRET_VALUE") {
		t.Fatalf("credential leaked: %s", message)
	}
	if !strings.Contains(message, "与用户名密码无关") {
		t.Fatalf("network failure must be distinguished from auth failure: %s", message)
	}
}
