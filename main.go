/*
Copyright 2015 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/openshift/origin/pkg/util/proc"
	flag "github.com/spf13/pflag"
	"io/ioutil"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"log"
	"net/http"
	"os"
	"time"
)

const (
	resyncPeriod = 5 * time.Minute
)

var (
	flags = flag.NewFlagSet("", flag.ExitOnError)

	inCluster = flags.Bool("in-cluster", true, `If true, use the built in kubernetes cluster for creating the client`)

	apiserver = flags.String("apiserver", "", `The URL of the apiserver to use as a master`)

	kubeconfig = flags.String("kubeconfig", "./config", "absolute path to the kubeconfig file")

	help = flags.BoolP("help", "h", false, "Print help text")

	port = flags.Int("port", 80, `Port to expose metrics on.`)

	clusterId = flags.Int("clusterId", 0, `The cluster id in DomeOS.`)

	domeosServer = flags.String("domeosServer", "", `The DomeOS server address to report events.`)
)

func main() {
	flags.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		flags.PrintDefaults()
	}

	err := flags.Parse(os.Args)
	if err != nil {
		log.Fatal("Error: %v", err)
	}

	if *help {
		flags.Usage()
		os.Exit(0)
	}

	if *apiserver == "" && !(*inCluster) {
		log.Fatal("--apiserver not set and --in-cluster is false; apiserver must be set to a valid URL")
	}
	log.Println("apiServer set to: %v", *apiserver)

	proc.StartReaper()

	kubeClient, err := createKubeClient()
	if err != nil {
		log.Fatal("Failed to create client: ", err)
	}

	initializeMetricCollection(kubeClient)
	metricsServer()
}

func createKubeClient() (kubeClient clientset.Interface, err error) {
	log.Println("Creating client")
	if *inCluster {
		config, err := restclient.InClusterConfig()
		if err != nil {
			return nil, err
		}
		// Allow overriding of apiserver even if using inClusterConfig
		// (necessary if kube-proxy isn't properly set up).
		if *apiserver != "" {
			config.Host = *apiserver
		}
		tokenPresent := false
		if len(config.BearerToken) > 0 {
			tokenPresent = true
		}
		log.Println("service account token present: %v", tokenPresent)
		log.Println("service host: %s", config.Host)
		if kubeClient, err = clientset.NewForConfig(config); err != nil {
			return nil, err
		}
	} else {
		// loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		// if you want to change the loading rules (which files in which order), you can do so here
		// configOverrides := &clientcmd.ConfigOverrides{}
		// if you want to change override values or bind them to flags, there are methods to help you
		// kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
		// config, err := kubeConfig.ClientConfig()
		// config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
		config, err := clientcmd.DefaultClientConfig.ClientConfig()
		if err != nil {
			return nil, err
		}
		// add host here
		config.Host = *apiserver
		kubeClient, err = clientset.NewForConfig(config)
		if err != nil {
			return nil, err
		}
	}

	// Informers don't seem to do a good job logging error messages when it
	// can't reach the server, making debugging hard. This makes it easier to
	// figure out if apiserver is configured incorrectly.
	log.Println("testing communication with server")
	_, err = kubeClient.Discovery().ServerVersion()
	if err != nil {
		return nil, fmt.Errorf("ERROR communicating with apiserver: %v", err)
	}

	return kubeClient, nil
}

func metricsServer() {
	// Address to listen on for web interface and telemetry
	listenAddress := fmt.Sprintf(":%d", *port)
	log.Println("Starting metrics server: %s", listenAddress)
	// Add healthzPath
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	})
	log.Fatal(http.ListenAndServe(listenAddress, nil))
}

type eventController struct {
}

func (*eventController) addEvent(obj interface{}) {
	if obj != nil {
		event := obj.(*v1.Event)
		reportEvent(*domeosServer, DomeosEvent{
			K8sEvent:   *event,
			ClusterId:  *clusterId,
			ClusterApi: *apiserver,
			Type:       "add",
		})
	}
}

func (*eventController) updateEvent(old, cur interface{}) {
	if cur != nil {
		event := cur.(*v1.Event)
		reportEvent(*domeosServer, DomeosEvent{
			K8sEvent:   *event,
			ClusterId:  *clusterId,
			ClusterApi: *apiserver,
			Type:       "update",
		})
	}
}

func (*eventController) deleteEvent(obj interface{}) {
	if obj != nil {
		event := obj.(*v1.Event)
		reportEvent(*domeosServer, DomeosEvent{
			K8sEvent:   *event,
			ClusterId:  *clusterId,
			ClusterApi: *apiserver,
			Type:       "delete",
		})
	}
}

type DomeosEvent struct {
	K8sEvent v1.Event `json:"k8sEvent"`

	ClusterId int `json:"clusterId"`

	ClusterApi string `json:"clusterApi"`

	Type string `json:"eventType"`
}

func reportEvent(url string, de DomeosEvent) {
	eventstr, err := json.Marshal(de)
	if err != nil {
		log.Println("marshal DomeosEvent error: ", err)
		return
	}
	// log.Println("report: %v", string(eventstr))
	request, err := http.NewRequest("POST", url, bytes.NewReader(eventstr))
	if err != nil {
		log.Println("create request error: %v", err)
		return
	}
	request.Header.Set("Content-Type", "application/json;charset=UTF-8")

	// var resp *http.Response
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		log.Println("get response error, %v", err)
	} else {
		_, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Println("http.Do failed,[err=%s][url=%s]", err, url)
		}
		defer resp.Body.Close()
	}
}

// initializeMetricCollection creates and starts informers and initializes and
// registers metrics for collection.
func initializeMetricCollection(kubeClient clientset.Interface) {
	cclient := kubeClient.CoreV1().RESTClient()
	elw := cache.NewListWatchFromClient(cclient, "events", v1.NamespaceAll, fields.Everything())
	ec := &eventController{}
	_, einf := cache.NewInformer(
		elw,
		&v1.Event{},
		resyncPeriod,
		cache.ResourceEventHandlerFuncs{
			AddFunc:    ec.addEvent,
			DeleteFunc: ec.deleteEvent,
		})

	go einf.Run(wait.NeverStop)
}
