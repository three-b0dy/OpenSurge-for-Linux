package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	mode := flag.String("mode", "origin", "origin or proxy")
	listen := flag.String("listen", "127.0.0.1:18443", "listen address")
	certFile := flag.String("tls-cert", "", "TLS certificate for origin mode")
	keyFile := flag.String("tls-key", "", "TLS private key for origin mode")
	logFile := flag.String("log", "", "optional append-only log file")
	flag.Parse()

	logger := log.New(os.Stderr, "linux-lab: ", log.LstdFlags)
	if *logFile != "" {
		file, err := os.OpenFile(*logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
		if err != nil {
			logger.Fatalf("open log: %v", err)
		}
		defer file.Close()
		logger.SetOutput(io.MultiWriter(os.Stderr, file))
	}

	switch *mode {
	case "origin":
		if err := serveOrigin(*listen, *certFile, *keyFile, logger); err != nil {
			logger.Fatal(err)
		}
	case "proxy":
		if err := serveProxy(*listen, logger); err != nil {
			logger.Fatal(err)
		}
	default:
		logger.Fatalf("unsupported mode %q", *mode)
	}
}

func serveOrigin(listen, certFile, keyFile string, logger *log.Logger) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "OK\n")
	})
	mux.HandleFunc("/nat", func(writer http.ResponseWriter, request *http.Request) {
		remoteHost, _, err := net.SplitHostPort(request.RemoteAddr)
		if err != nil {
			remoteHost = request.RemoteAddr
		}
		logger.Printf("origin %s %s remote=%s", request.Method, request.URL.Path, remoteHost)
		_, _ = fmt.Fprintf(writer, "remote=%s\n", remoteHost)
	})
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		remoteHost, _, _ := net.SplitHostPort(request.RemoteAddr)
		_, _ = fmt.Fprintf(writer, "OpenSurge Linux lab origin\nremote=%s\n", remoteHost)
	})

	server := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if certFile == "" || keyFile == "" {
		return server.ListenAndServe()
	}
	return server.ListenAndServeTLS(certFile, keyFile)
}

func serveProxy(listen string, logger *log.Logger) error {
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	for {
		connection, err := listener.Accept()
		if err != nil {
			return err
		}
		go handleProxyConnection(connection, logger)
	}
}

func handleProxyConnection(connection net.Conn, logger *log.Logger) {
	defer connection.Close()
	reader := bufio.NewReader(connection)
	request, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	if request.Method != http.MethodConnect || strings.TrimSpace(request.Host) == "" {
		_, _ = io.WriteString(connection, "HTTP/1.1 405 Method Not Allowed\r\nConnection: close\r\n\r\n")
		return
	}

	logger.Printf("CONNECT %s", request.Host)
	target, err := net.DialTimeout("tcp", request.Host, 5*time.Second)
	if err != nil {
		_, _ = fmt.Fprintf(connection, "HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n")
		return
	}
	defer target.Close()
	if _, err := io.WriteString(connection, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}

	left := make(chan struct{})
	go func() {
		_, _ = io.Copy(target, reader)
		_ = target.SetDeadline(time.Now())
		close(left)
	}()
	_, _ = io.Copy(connection, target)
	<-left
}
