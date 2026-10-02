package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"net/smtp"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const deliveryTimeout = 20 * time.Second

// Validate checks configuration and resolves enabled destinations using the
// same address policy that is enforced again when making a connection.
func Validate(cfg Config) error {
	if err := validateConfig(cfg); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if cfg.Webhook.Enabled {
		u, _ := url.Parse(cfg.Webhook.URL)
		addresses, err := resolveAllowed(ctx, u.Hostname())
		if err != nil {
			return errors.New("webhook host must resolve to an allowed public address")
		}
		if u.Scheme == "http" && !onlyPrivateAddresses(addresses) {
			return errors.New("HTTP webhooks are allowed only for private development destinations")
		}
	}
	if cfg.SMTP.Enabled {
		if _, err := resolveAllowed(ctx, cfg.SMTP.Host); err != nil {
			return errors.New("SMTP host must resolve to an allowed public address")
		}
	}
	return nil
}

func validateConfig(cfg Config) error {
	if cfg.Webhook.Enabled {
		if _, err := parseWebhookURL(cfg.Webhook.URL); err != nil {
			return err
		}
		if cfg.Webhook.Secret == "" || len(cfg.Webhook.Secret) > 4096 {
			return errors.New("webhook signing secret is required and must be at most 4096 bytes")
		}
	}
	if cfg.SMTP.Enabled {
		s := cfg.SMTP
		if s.Host == "" || len(s.Host) > 253 || strings.ContainsAny(s.Host, "/\\@?#\r\n\t ") {
			return errors.New("SMTP host is invalid")
		}
		if strings.Contains(s.Host, ":") {
			if _, err := netip.ParseAddr(s.Host); err != nil {
				return errors.New("SMTP host must not include a port")
			}
		}
		if s.Port < 1 || s.Port > 65535 {
			return errors.New("SMTP port must be between 1 and 65535")
		}
		if len(s.Username) > 4096 || len(s.Password) > 4096 || strings.ContainsAny(s.Username, "\r\n") {
			return errors.New("SMTP credentials are invalid")
		}
		if (s.Username == "") != (s.Password == "") {
			return errors.New("SMTP username and password must both be set or both be empty")
		}
		for _, address := range []string{s.From, s.To} {
			if address == "" || len(address) > 512 || strings.ContainsAny(address, "\r\n") {
				return errors.New("SMTP From and To must be single valid email addresses")
			}
			if _, err := mail.ParseAddress(address); err != nil {
				return errors.New("SMTP From and To must be single valid email addresses")
			}
		}
	}
	return nil
}

func allowPrivate() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("DONATE_ALLOW_PRIVATE_WEBHOOKS")), "true")
}

func parseWebhookURL(raw string) (*url.URL, error) {
	if len(raw) == 0 || len(raw) > 2048 {
		return nil, errors.New("webhook URL is invalid")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" {
		return nil, errors.New("webhook URL is invalid")
	}
	if u.Scheme != "https" && !(allowPrivate() && u.Scheme == "http") {
		return nil, errors.New("webhook URL must use HTTPS")
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("webhook URL port is invalid")
		}
	}
	return u, nil
}

// Some addresses report IsGlobalUnicast while belonging to reserved ranges.
// Reject them explicitly, along with IPv6 tunneling and translation ranges
// that can route a seemingly public address into a private IPv4 network.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

func publicAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.Zone() != "" || !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return false
	}
	if addr.Is6() && !netip.MustParsePrefix("2000::/3").Contains(addr) {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

func allowedAddress(addr netip.Addr) bool {
	if publicAddress(addr) {
		return true
	}
	addr = addr.Unmap()
	return allowPrivate() && addr.IsValid() && addr.Zone() == "" &&
		(addr.IsGlobalUnicast() || addr.IsLoopback() || addr.IsLinkLocalUnicast())
}

func resolveAllowed(ctx context.Context, host string) ([]netip.Addr, error) {
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("destination resolution failed")
	}
	// Reject mixed public/private DNS answers instead of selecting only one.
	for _, addr := range addresses {
		if !allowedAddress(addr) {
			return nil, errors.New("destination address is blocked")
		}
	}
	return addresses, nil
}

func dialAllowed(ctx context.Context, network, address string) (net.Conn, error) {
	return dialDestination(ctx, network, address, false)
}

func onlyPrivateAddresses(addresses []netip.Addr) bool {
	for _, address := range addresses {
		if publicAddress(address) {
			return false
		}
	}
	return len(addresses) > 0
}

func dialDestination(ctx context.Context, network, address string, privateOnly bool) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("destination address is invalid")
	}
	addresses, err := resolveAllowed(ctx, host)
	if err != nil {
		return nil, err
	}
	if privateOnly && !onlyPrivateAddresses(addresses) {
		return nil, errors.New("plaintext destination must be private")
	}
	dialer := net.Dialer{Timeout: 8 * time.Second}
	for _, ip := range addresses {
		// Pin the connection to the IP that was validated. TLS still validates
		// the original URL hostname, preventing DNS rebinding between checks.
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.New("destination connection failed")
}

func sendWebhook(ctx context.Context, deliveryID string, body []byte, cfg WebhookConfig) error {
	if err := validateConfig(Config{Webhook: cfg}); err != nil {
		return errors.New("saved webhook configuration is invalid")
	}
	u, err := parseWebhookURL(cfg.URL)
	if err != nil {
		return errors.New("saved webhook configuration is invalid")
	}
	var event Event
	if len(body) > maxPayload || json.Unmarshal(body, &event) != nil || strings.ContainsAny(event.Type, "\r\n") {
		return errors.New("saved notification payload is invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return errors.New("webhook request could not be constructed")
	}
	mac := hmac.New(sha256.New, []byte(cfg.Secret))
	_, _ = mac.Write(body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "TOKEN-Donate/1")
	request.Header.Set("X-Donate-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	request.Header.Set("X-Donate-Event", event.Type)
	request.Header.Set("X-Donate-Delivery", deliveryID)
	transport := &http.Transport{
		// No environment proxies: every destination connection goes through
		// the resolver and IP policy above, including HTTPS connections.
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialDestination(ctx, network, address, u.Scheme == "http")
		},
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    8 * time.Second,
		ResponseHeaderTimeout:  8 * time.Second,
		MaxResponseHeaderBytes: 16 * 1024,
		DisableKeepAlives:      true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   deliveryTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		// url.Error can contain query tokens and connection/auth errors can
		// contain credentials. Never store the underlying error text.
		return errors.New("webhook delivery failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("webhook responded HTTP %d", response.StatusCode)
	}
	return nil
}

func sendSMTP(ctx context.Context, body []byte, cfg SMTPConfig) error {
	if err := validateConfig(Config{SMTP: cfg}); err != nil {
		return errors.New("saved SMTP configuration is invalid")
	}
	var event Event
	if len(body) > maxPayload || json.Unmarshal(body, &event) != nil || strings.ContainsAny(event.Type, "\r\n") {
		return errors.New("saved notification payload is invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	conn, err := dialAllowed(ctx, "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		return errors.New("SMTP connection failed")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	client, err := newSMTPClient(ctx, conn, cfg, &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	defer client.Close()
	return sendSMTPMessage(client, body, event, cfg)
}

// newSMTPClient negotiates implicit TLS on the standard submissions port 465
// before waiting for the SMTP greeting. Other ports use STARTTLS. The supplied
// connection has already passed the destination policy and is pinned to its IP.
func newSMTPClient(ctx context.Context, conn net.Conn, cfg SMTPConfig, tlsConfig *tls.Config) (*smtp.Client, error) {
	implicitTLS := cfg.Port == 465
	if implicitTLS {
		secureConn := tls.Client(conn, tlsConfig)
		if err := secureConn.HandshakeContext(ctx); err != nil {
			return nil, errors.New("SMTP secure connection failed")
		}
		conn = secureConn
	}
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return nil, errors.New("SMTP handshake failed")
	}
	ready := false
	defer func() {
		if !ready {
			_ = client.Close()
		}
	}()
	if err := client.Hello("donate.local"); err != nil {
		return nil, errors.New("SMTP handshake failed")
	}
	if !implicitTLS {
		if supported, _ := client.Extension("STARTTLS"); supported {
			if err := client.StartTLS(tlsConfig); err != nil {
				return nil, errors.New("SMTP secure connection failed")
			}
		} else {
			remote, _, err := net.SplitHostPort(conn.RemoteAddr().String())
			ip, parseErr := netip.ParseAddr(remote)
			if !allowPrivate() || err != nil || parseErr != nil || publicAddress(ip) {
				return nil, errors.New("SMTP server must support STARTTLS")
			}
		}
	}
	if cfg.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return nil, errors.New("SMTP authentication failed")
		}
	}
	ready = true
	return client, nil
}

func sendSMTPMessage(client *smtp.Client, body []byte, event Event, cfg SMTPConfig) error {
	from, _ := mail.ParseAddress(cfg.From)
	to, _ := mail.ParseAddress(cfg.To)
	if err := client.Mail(from.Address); err != nil {
		return errors.New("SMTP sender was rejected")
	}
	if err := client.Rcpt(to.Address); err != nil {
		return errors.New("SMTP recipient was rejected")
	}
	writer, err := client.Data()
	if err != nil {
		return errors.New("SMTP message was rejected")
	}
	message := "From: " + from.String() + "\r\nTo: " + to.String() + "\r\n" +
		"Subject: " + mime.QEncoding.Encode("UTF-8", "TOKEN donation: "+event.Type) + "\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n\r\nA donation event was recorded.\r\n\r\n" +
		strings.ReplaceAll(string(body), "\n", "\r\n") + "\r\n"
	if _, err := io.WriteString(writer, message); err != nil {
		_ = writer.Close()
		return errors.New("SMTP message delivery failed")
	}
	if err := writer.Close(); err != nil {
		return errors.New("SMTP message delivery failed")
	}
	// DATA acceptance means delivered. QUIT failures must not cause duplicates.
	_ = client.Quit()
	return nil
}
