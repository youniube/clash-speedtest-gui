package shareurl

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/common/convert"
	"github.com/metacubex/mihomo/component/ca"
	C "github.com/metacubex/mihomo/constant"
	anytlslistener "github.com/metacubex/mihomo/listener/anytls"
	listenerconfig "github.com/metacubex/mihomo/listener/config"
)

// TestGenerateAnyTLSRoundTripCanProxyHTTPRequest is intentionally stronger than
// a parser test: it starts a real AnyTLS listener, imports the generated URL
// through Mihomo's production converter, builds the resulting proxy, and sends
// an HTTP request through it.
func TestGenerateAnyTLSRoundTripCanProxyHTTPRequest(t *testing.T) {
	const password = "round-trip:p@ss/word"
	certificate, privateKey, fingerprint, err := ca.NewRandomTLSKeyPair(ca.KeyPairTypeP256)
	if err != nil {
		t.Fatal(err)
	}

	listener, err := anytlslistener.New(listenerconfig.AnyTLSServer{
		Enable:      true,
		Listen:      "127.0.0.1:0",
		Users:       map[string]string{"test": password},
		Certificate: certificate,
		PrivateKey:  privateKey,
	}, tcpRelayTunnel{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	addresses := listener.AddrList()
	if len(addresses) != 1 {
		t.Fatalf("expected one AnyTLS listener address, got %d", len(addresses))
	}
	serverAddress, err := netip.ParseAddrPort(addresses[0].String())
	if err != nil {
		t.Fatal(err)
	}
	link, err := Generate(map[string]any{
		"name":        "AnyTLS live round trip",
		"type":        "anytls",
		"server":      serverAddress.Addr().String(),
		"port":        int(serverAddress.Port()),
		"password":    password,
		"fingerprint": fingerprint,
		"udp":         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := convert.ConvertsV2Ray([]byte(link))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 1 {
		t.Fatalf("expected one imported AnyTLS proxy, got %d", len(parsed))
	}
	proxy, err := adapter.ParseProxy(parsed[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "anytls-round-trip-ok")
	}))
	defer target.Close()
	targetAddress, err := netip.ParseAddrPort(target.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return proxy.DialContext(ctx, &C.Metadata{
				NetWork: C.TCP,
				DstIP:   targetAddress.Addr(),
				DstPort: targetAddress.Port(),
			})
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	response, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("generated AnyTLS URL imported but could not proxy traffic: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "anytls-round-trip-ok" {
		t.Fatalf("unexpected proxied response: status=%d body=%q", response.StatusCode, body)
	}
}

type tcpRelayTunnel struct{}

func (tcpRelayTunnel) HandleTCPConn(client net.Conn, metadata *C.Metadata) {
	defer client.Close()
	upstream, err := net.DialTimeout("tcp", metadata.RemoteAddress(), 5*time.Second)
	if err != nil {
		return
	}
	defer upstream.Close()

	clientDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(upstream, client)
		if connection, ok := upstream.(*net.TCPConn); ok {
			_ = connection.CloseWrite()
		}
		close(clientDone)
	}()
	_, _ = io.Copy(client, upstream)
	if connection, ok := client.(*net.TCPConn); ok {
		_ = connection.CloseWrite()
	}
	<-clientDone
}

func (tcpRelayTunnel) HandleUDPPacket(C.UDPPacket, *C.Metadata) {}

func (tcpRelayTunnel) NatTable() C.NatTable { return nil }
