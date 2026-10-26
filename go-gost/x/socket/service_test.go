package socket

import (
	"crypto/tls"
	"net"
	"testing"

	"github.com/go-gost/core/logger"
	"github.com/go-gost/x/config"
	"github.com/go-gost/x/config/parsing"
	parser "github.com/go-gost/x/config/parsing/service"
	_ "github.com/go-gost/x/handler/forward/local"
	_ "github.com/go-gost/x/listener/tcp"
	_ "github.com/go-gost/x/listener/udp"
	"github.com/go-gost/x/registry"
)

func prepareServiceTest(t *testing.T) {
	t.Helper()
	logger.SetDefault(logger.LoggerGroup())
	parsing.SetDefaultTLSConfig(&tls.Config{})
	config.Set(&config.Config{})
	t.Cleanup(func() {
		for name := range registry.ServiceRegistry().GetAll() {
			registry.ServiceRegistry().Unregister(name)
		}
		config.Set(&config.Config{})
	})
}

func freeUDPAddress(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := conn.LocalAddr().String()
	conn.Close()
	return addr
}

func udpService(name, addr string) config.ServiceConfig {
	return config.ServiceConfig{Name: name, Addr: addr,
		Listener: &config.ListenerConfig{Type: "udp"}, Handler: &config.HandlerConfig{Type: "udp"}}
}

func assertUDPReleased(t *testing.T, addr string) {
	t.Helper()
	conn, err := net.ListenPacket("udp4", addr)
	if err != nil {
		t.Fatalf("listener leaked at %s: %v", addr, err)
	}
	conn.Close()
}

func TestCreateBatchFailureClosesEarlierUDPListener(t *testing.T) {
	prepareServiceTest(t)
	addr := freeUDPAddress(t)
	bad := udpService("bad", "127.0.0.1:0")
	bad.Listener.Type = "audit-unknown-listener"
	err := createServices(createServicesRequest{Data: []config.ServiceConfig{udpService("first", addr), bad}})
	if err == nil {
		t.Fatal("invalid second listener accepted")
	}
	if len(registry.ServiceRegistry().GetAll()) != 0 {
		t.Fatal("failed batch registered services")
	}
	assertUDPReleased(t, addr)
}

func TestParseFailureClosesInitializedUDPListener(t *testing.T) {
	prepareServiceTest(t)
	addr := freeUDPAddress(t)
	bad := udpService("bad-handler", addr)
	bad.Handler.Type = "audit-unknown-handler"
	if _, err := parser.ParseService(&bad); err == nil {
		t.Fatal("invalid handler accepted")
	}
	assertUDPReleased(t, addr)
}

func TestUpdateSamePortAndRollbackRestoresWholeBatch(t *testing.T) {
	prepareServiceTest(t)
	first := udpService("first", freeUDPAddress(t))
	second := udpService("second", freeUDPAddress(t))
	if err := createServices(createServicesRequest{Data: []config.ServiceConfig{first, second}}); err != nil {
		t.Fatal(err)
	}
	if err := updateServices(updateServicesRequest{Data: []config.ServiceConfig{first, second}}); err != nil {
		t.Fatalf("same-port update failed: %v", err)
	}
	oldFirst := registry.ServiceRegistry().Get(first.Name)
	oldSecond := registry.ServiceRegistry().Get(second.Name)
	bad := second
	bad.Handler = &config.HandlerConfig{Type: "audit-unknown-handler"}
	if err := updateServices(updateServicesRequest{Data: []config.ServiceConfig{first, bad}}); err == nil {
		t.Fatal("invalid update accepted")
	}
	for _, cfg := range []config.ServiceConfig{first, second} {
		svc := registry.ServiceRegistry().Get(cfg.Name)
		if svc == nil || svc.Addr().String() != cfg.Addr {
			t.Fatalf("old %s was not restored", cfg.Name)
		}
		conn, err := net.ListenPacket("udp4", cfg.Addr)
		if err == nil {
			conn.Close()
			t.Fatalf("restored %s did not bind old port", cfg.Name)
		}
	}
	if registry.ServiceRegistry().Get(first.Name) == oldFirst || registry.ServiceRegistry().Get(second.Name) == oldSecond {
		t.Fatal("rollback reused a closed service")
	}
	if len(config.Global().Services) != 2 {
		t.Fatal("rollback changed configured service count")
	}
	if err := deleteServices(deleteServicesRequest{Services: []string{first.Name, second.Name}}); err != nil {
		t.Fatal(err)
	}
	assertUDPReleased(t, first.Addr)
	assertUDPReleased(t, second.Addr)
}

func TestDuplicateBatchNamesAreRejectedBeforeOpeningPorts(t *testing.T) {
	prepareServiceTest(t)
	addr := freeUDPAddress(t)
	cfg := udpService("duplicate", addr)
	if err := createServices(createServicesRequest{Data: []config.ServiceConfig{cfg, cfg}}); err == nil {
		t.Fatal("duplicate batch accepted")
	}
	assertUDPReleased(t, addr)
}

func TestPauseResumeReleasesAndReopensUDPListener(t *testing.T) {
	prepareServiceTest(t)
	cfg := udpService("pause-resume", freeUDPAddress(t))
	if err := createServices(createServicesRequest{Data: []config.ServiceConfig{cfg}}); err != nil {
		t.Fatal(err)
	}
	if err := pauseServices(pauseServicesRequest{Services: []string{cfg.Name}}); err != nil {
		t.Fatal(err)
	}
	assertUDPReleased(t, cfg.Addr)
	if err := resumeServices(resumeServicesRequest{Services: []string{cfg.Name}}); err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenPacket("udp4", cfg.Addr)
	if err == nil {
		conn.Close()
		t.Fatal("resumed service did not bind its port")
	}
	if paused, _ := config.Global().Services[0].Metadata["paused"].(bool); paused {
		t.Fatal("resume left configuration paused")
	}
}
