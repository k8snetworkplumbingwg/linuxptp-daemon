package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/golang/glog"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/controller"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/daemon"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/features"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpclient "github.com/k8snetworkplumbingwg/ptp-operator/pkg/client/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Git commit of current build set at build time
var GitCommit = "Undefined"

type cliParams struct {
	updateInterval  int
	profileDir      string
	pmcPollInterval int
	useController   bool
}

var (
	scheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(ptpv1.AddToScheme(scheme))
}

// Parse Command line flags
func flagInit(cp *cliParams) {
	flag.IntVar(&cp.updateInterval, "update-interval", config.DefaultUpdateInterval,
		"Interval to update PTP status")
	flag.StringVar(&cp.profileDir, "linuxptp-profile-path", config.DefaultProfilePath,
		"profile to start linuxptp processes")
	flag.IntVar(&cp.pmcPollInterval, "pmc-poll-interval", config.DefaultPmcPollInterval,
		"Interval for periodical PMC poll")
	flag.BoolVar(&cp.useController, "use-controller", false,
		"Use Kubernetes controller to watch PtpConfig resources (default: false)")
}

func main() {

	fmt.Printf("Git commit: %s\n", GitCommit)
	cp := &cliParams{}
	flag.Parse()
	flagInit(cp)

	glog.Infof("resync period set to: %d [s]", cp.updateInterval)
	glog.Infof("linuxptp profile path set to: %s", cp.profileDir)
	glog.Infof("pmc poll interval set to: %d [s]", cp.pmcPollInterval)

	cfg, err := config.GetKubeConfig()
	if err != nil {
		glog.Errorf("get kubeconfig failed: %v", err)
		return
	}
	glog.Infof("successfully get kubeconfig")

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		glog.Errorf("cannot create new config for kubeClient: %v", err)
		return
	}

	ptpClient, err := ptpclient.NewForConfig(cfg)
	if err != nil {
		glog.Errorf("cannot create new config for ptpClient: %v", err)
		return
	}

	// The name of NodePtpDevice CR for this node is equal to the node name
	nodeName := os.Getenv("NODE_NAME")
	podName := os.Getenv("POD_NAME")
	if nodeName == "" {
		glog.Error("cannot find NODE_NAME environment variable")
		return
	}

	// The name of NodePtpDevice CR for this node is equal to the node name
	var stdoutToSocket = false
	if val, ok := os.LookupEnv("LOGS_TO_SOCKET"); ok && val != "" {
		if ret, err := strconv.ParseBool(val); err == nil {
			stdoutToSocket = ret
		}
	}

	plugins := make([]string, 0)

	if val, ok := os.LookupEnv("PLUGINS"); ok && val != "" {
		plugins = strings.Split(val, ",")
	}

	stopCh := make(chan struct{})
	defer close(stopCh)

	ptpConfUpdate, err := daemon.NewLinuxPTPConfUpdate()
	if err != nil {
		glog.Errorf("failed to create a ptp config update: %v", err)
		return
	}

	// label the current linux-ptp-daemon pod with a nodeName label
	err = labelPod(kubeClient, nodeName, podName)
	if err != nil {
		glog.Errorf("failed to label linuxptp-daemon with node name, err: %v", err)
		return
	}

	hwconfigs := []ptpv1.HwConfig{}
	refreshNodePtpDevice := true
	closeProcessManager := make(chan bool)
	lm, err := leap.New(kubeClient, daemon.PtpNamespace)
	if err != nil {
		glog.Error("failed to initialize Leap manager, ", err)
		return
	}
	go lm.Run()

	defer close(lm.Close)

	tracker := &daemon.ReadyTracker{}

	version := features.GetLinuxPTPPackageVersion()
	ocpVersion := features.GetOCPVersion()
	// TODO: version needs to be sent to cloud event proxy when we intergrate feature flags there
	features.SetFlags(version, ocpVersion)
	features.Flags.Print()

	go daemon.New(
		nodeName,
		daemon.PtpNamespace,
		stdoutToSocket,
		kubeClient,
		ptpConfUpdate,
		stopCh,
		plugins,
		&hwconfigs,
		&refreshNodePtpDevice,
		closeProcessManager,
		cp.pmcPollInterval,
		tracker,
	).Run()

	tickerPull := time.NewTicker(time.Second * time.Duration(cp.updateInterval))
	defer tickerPull.Stop()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	// by default metrics is hosted here,if LOGS_TO_SOCKET variable is set then metrics are disabled
	if !stdoutToSocket { // if not sending metrics (log) out to a socket then host metrics here
		daemon.StartMetricsServer("0.0.0.0:9091")
	}

	daemon.StartReadyServer("0.0.0.0:8081", tracker)

	// Setup controller manager if enabled
	var mgr ctrl.Manager
	var mgrCtx context.Context
	var mgrCancel context.CancelFunc
	if cp.useController {
		glog.Info("Setting up controller manager for PtpConfig resources")

		// Create manager
		var err1 error
		mgr, err1 = ctrl.NewManager(cfg, ctrl.Options{
			Scheme:                 scheme,
			HealthProbeBindAddress: ":8082",
			LeaderElection:         false, // Disable leader election for daemon
			// PtpConfig is cluster-scoped, so don't restrict cache by namespace
		})
		if err1 != nil {
			glog.Errorf("unable to start controller manager: %v", err1)
			return
		}

		// Add health checks
		if err1 = mgr.AddHealthzCheck("healthz", healthz.Ping); err1 != nil {
			glog.Errorf("unable to set up health check: %v", err1)
			return
		}
		if err1 = mgr.AddReadyzCheck("readyz", healthz.Ping); err1 != nil {
			glog.Errorf("unable to set up ready check: %v", err1)
			return
		}

		// Setup PtpConfig controller
		ptpConfigReconciler := &controller.PtpConfigReconciler{
			Client:       mgr.GetClient(),
			Scheme:       mgr.GetScheme(),
			NodeName:     nodeName,
			ConfigUpdate: ptpConfUpdate,
		}

		if err1 = ptpConfigReconciler.SetupWithManager(mgr); err1 != nil {
			glog.Errorf("unable to create controller for PtpConfig: %v", err1)
			return
		}

		// Start the manager in a goroutine
		mgrCtx, mgrCancel = context.WithCancel(context.Background())
		go func() {
			glog.Info("Starting controller manager")
			if err1 = mgr.Start(mgrCtx); err1 != nil {
				glog.Errorf("problem running controller manager: %v", err1)
			}
		}()

		// We'll cancel the manager context when we get a shutdown signal
		defer mgrCancel()

		glog.Info("Controller manager started successfully")
	}

	// Wait for one ticker interval before loading the profile
	// This allows linuxptp-daemon connection to the cloud-event-proxy container to
	// be up and running before PTP state logs are printed.
	time.Sleep(time.Second * time.Duration(cp.updateInterval/2))

	if cp.useController {
		// When using controller mode, we only need to handle device status updates and shutdown signals
		// The controller will handle configuration updates automatically via PtpConfig watches
		glog.Info("Running in controller mode - PtpConfig resources will be watched automatically")

		// Trigger initial reconciliation by listing all PtpConfigs
		go func() {
			time.Sleep(2 * time.Second) // Give controller time to start
			glog.Info("Triggering initial PtpConfig reconciliation")

			// Force initial reconciliation by calling the controller directly
			ptpConfigReconciler := &controller.PtpConfigReconciler{
				Client:       mgr.GetClient(),
				Scheme:       mgr.GetScheme(),
				NodeName:     nodeName,
				ConfigUpdate: ptpConfUpdate,
			}

			// Trigger reconciliation for any existing PtpConfigs
			_, err1 := ptpConfigReconciler.Reconcile(context.Background(), ctrl.Request{})
			if err1 != nil {
				glog.Errorf("Initial reconciliation failed: %v", err1)
			}
		}()

		for {
			select {
			case <-tickerPull.C:
				glog.Infof("ticker pull (controller mode)")
				// Run a loop to update the device status
				if refreshNodePtpDevice {
					go daemon.RunDeviceStatusUpdate(ptpClient, nodeName, &hwconfigs)
					refreshNodePtpDevice = false
				}
			case sig := <-sigCh:
				glog.Info("signal received, shutting down", sig)
				closeProcessManager <- true
				if mgrCancel != nil {
					mgrCancel() // Stop the controller manager
				}
				return
			}
		}
	} else {
		// Legacy file-based mode
		glog.Info("Running in legacy file-based mode")
		for {
			select {
			case <-tickerPull.C:
				glog.Infof("ticker pull (file mode)")
				// Run a loop to update the device status
				if refreshNodePtpDevice {
					go daemon.RunDeviceStatusUpdate(ptpClient, nodeName, &hwconfigs)
					refreshNodePtpDevice = false
				}

				nodeProfile := filepath.Join(cp.profileDir, nodeName)
				if _, err1 := os.Stat(nodeProfile); err1 != nil {
					glog.Errorf("error stating node profile %v: %v", nodeName, err1)
					continue
				}
				nodeProfilesJSON, err1 := os.ReadFile(nodeProfile)
				if err1 != nil {
					glog.Errorf("error reading node profile: %v", nodeProfile)
					continue
				}

				err = ptpConfUpdate.UpdateConfig(nodeProfilesJSON)
				if err != nil {
					glog.Errorf("error updating the node configuration using the profiles loaded: %v", err)
				}
			case sig := <-sigCh:
				glog.Info("signal received, shutting down", sig)
				closeProcessManager <- true
				return
			}
		}
	}
}

type patchStringValue struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value string `json:"value"`
}

func labelPod(kubeClient *kubernetes.Clientset, nodeName, podName string) (err error) {
	pod, err := kubeClient.CoreV1().Pods(daemon.PtpNamespace).Get(context.TODO(), podName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("error getting linuxptp-daemon pod, err=%s", err)
	}
	if pod == nil {
		return fmt.Errorf("could not find linux-ptp-daemon pod to label")
	}
	if nodeName != "" && strings.Contains(nodeName, ".") {
		nodeName = strings.Split(nodeName, ".")[0]
	}

	payload := []patchStringValue{{
		Op:    "replace",
		Path:  "/metadata/labels/nodeName",
		Value: nodeName,
	}}
	payloadBytes, _ := json.Marshal(payload)

	_, err = kubeClient.CoreV1().Pods(pod.GetNamespace()).Patch(context.TODO(), pod.GetName(), types.JSONPatchType, payloadBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("could not label ns=%s pod %s, err=%s", pod.GetName(), pod.GetNamespace(), err)
	}
	glog.Infof("Pod %s labelled successfully.", pod.GetName())
	return nil
}
