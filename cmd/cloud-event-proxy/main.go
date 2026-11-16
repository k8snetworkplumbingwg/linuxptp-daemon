package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/auth"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/cep"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"k8s.io/client-go/kubernetes"
)

var GitCommit = "Undefined"

var (
	socket         string
	port           int
	storePath      string
	nodeName       string
	apiHost        string
	tlsCert        string
	tlsKey         string
	authConfigPath string
)

func main() {
	flag.StringVar(&authConfigPath, "auth-config", "", "Path to CEP authentication JSON configuration.")
	flag.StringVar(&socket, "socket", "/var/run/ptp/ipc.sock", "Path to daemon IPC Unix socket.")
	flag.IntVar(&port, "api-port", 9043, "The port the REST API endpoint binds to.")
	flag.StringVar(&storePath, "store-path", "", "Directory for persistent subscription storage."+
		"This will cause subscriptions to be persisted to a disk backend, instead of the default ConfigMap backend.")
	flag.StringVar(&tlsCert, "tls-cert", "", "Path to TLS certificate file. If set with --tls-key, the server uses TLS.")
	flag.StringVar(&tlsKey, "tls-key", "", "Path to TLS private key file. If set with --tls-cert, the server uses TLS.")
	flag.StringVar(&apiHost, "api-host", "", "Host used to build subscription UriLocation values in API responses. "+
		"Defaults to the node name.")
	flag.Parse()

	if tlsKey != "" || tlsCert != "" {
		if tlsKey == "" || tlsCert == "" {
			glog.Fatal("if one of the two is provided, both --tls-key and --tls-cert must be set")
		}
	}

	nodeName = os.Getenv("NODE_NAME")
	if nodeName == "" {
		var err error
		if nodeName, err = os.Hostname(); err != nil {
			glog.Fatalf("NODE_NAME not set and failed to get hostname: %v", err)
		}
	}

	if apiHost == "" {
		apiHost = nodeName
	}
	var security *cep.Security
	writer := cep.NewHTTPWriterFunc(2 * time.Second)
	if authConfigPath != "" {
		cfg, err := auth.LoadAuthConfig(authConfigPath)
		if err != nil {
			glog.Fatalf("invalid auth configuration: %v", err)
		}
		var validator auth.TokenValidator
		if cfg.EnableOAuth {
			validator, err = auth.NewTokenReviewValidator()
			if err != nil {
				glog.Fatalf("TokenReview setup failed: %v", err)
			}
		}
		security, writer, err = cep.NewSecurity(cfg, validator)
		if err != nil {
			glog.Fatalf("security setup failed: %v", err)
		}
	}
	scheme := "http"
	if tlsCert != "" {
		scheme = "https"
	}
	if security != nil && security.TLS != nil {
		scheme = "https"
	}
	// baseURL is the stable, server-determined root used to build subscription UriLocation values.
	baseURL, err := makeBaseURL(apiHost, nodeName, port, scheme)
	if err != nil {
		glog.Fatalf("invalid API host: %v", err)
	}

	glog.Infof("cloud-event-proxy starting, node=%s, socket=%s, baseURL=%s, commit=%s", nodeName, socket, baseURL, GitCommit)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer stop()

	cache := cep.NewEventCache(nodeName)
	var storage cep.Store
	if storePath != "" {
		path := filepath.Join(storePath, "subscriptions.json")
		storage, err = cep.NewDiskStore(path)
		if err != nil {
			glog.Fatalf("failed to create disk store at path %s: %v", path, err)
		}
	} else {
		storage, err = initConfigMapStore(nodeName)
		if err != nil {
			glog.Fatalf("failed to init ConfigMap subscription store: %v", err)
		}
	}
	ps := cep.NewPubSub(storage, writer)
	if err = ps.LoadFromStore(ctx); err != nil {
		glog.Fatalf("failed to load persisted subscriptions: %v", err)
	}
	proxy := cep.NewCloudEventProxy(cache, ps, nodeName, baseURL)
	proxy.Security = security

	if err = os.MkdirAll(filepath.Dir(socket), 0755); err != nil {
		glog.Fatalf("failed to create socket directory: %v", err)
	}
	// Remove any socket left behind by a previous unclean exit
	if err = os.Remove(socket); err != nil && !os.IsNotExist(err) {
		glog.Fatalf("failed to remove stale socket %s: %v", socket, err)
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		glog.Fatalf("failed to listen on %s: %v", socket, err)
	}
	defer ln.Close()

	go proxy.AcceptDaemonConnections(ctx, ln)

	if err = proxy.ListenAndServe(ctx, port, tlsCert, tlsKey); err != nil {
		glog.Fatalf("API server error: %v", err)
	}
}

// The operator's compatibility transportHost may be a full URL with a
// NODE_NAME placeholder; the listener's actual TLS mode determines the scheme.
func makeBaseURL(host, node string, port int, scheme string) (string, error) {
	host = strings.ReplaceAll(host, "NODE_NAME", strings.Split(node, ".")[0])
	if !strings.Contains(host, "://") {
		host = "//" + host
	}
	u, err := url.Parse(host)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("expected a host or root URL")
	}
	return (&url.URL{Scheme: scheme, Host: net.JoinHostPort(u.Hostname(), strconv.Itoa(port))}).String(), nil
}

func resolveNamespace() (string, error) {
	if ns := os.Getenv("POD_NAMESPACE"); ns != "" {
		return ns, nil
	}
	return "", fmt.Errorf("POD_NAMESPACE not set")
}

func initConfigMapStore(nodeName string) (cep.Store, error) {
	ns, err := resolveNamespace()
	if err != nil {
		return nil, fmt.Errorf("error resolving namespace for ConfigMap store: %w", err)
	}
	cfg, err := config.GetKubeConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to build kube config: %w", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}
	storage, err := cep.NewConfigMapStore(context.TODO(), clientset, ns, nodeName)
	if err != nil {
		return nil, fmt.Errorf("error creating ConfigMap store: %w", err)
	}
	return storage, nil
}
