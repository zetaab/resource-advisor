package advisor

import (
	"fmt"
	"context"
	"io/ioutil"
	"net/http"
	"time"
	"net/url"
	"path"
	"strings"
	"crypto/tls"
	"crypto/x509"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	prommodel "github.com/prometheus/common/model"
)

const (
	promOperatorClusterURL = "/api/v1/namespaces/monitoring/services/prometheus-operated:web/proxy/"
	podCPURequest = `avg_over_time(node_namespace_pod_container:container_cpu_usage_seconds_total:sum_irate{pod="%s", container="%s"}[1w])`
	podCPULimit = `max_over_time(node_namespace_pod_container:container_cpu_usage_seconds_total:sum_irate{pod="%s", container="%s"}[1w]) * 1.2`
	podMemoryRequest = `avg_over_time(container_memory_working_set_bytes{pod="%s", container="%s"}[1w]) / 1024 / 1024`
	podMemoryLimit = `(max_over_time(container_memory_working_set_bytes{pod="%s", container="%s"}[1w]) / 1024 / 1024) * 1.2`

)

func findConfig() (*rest.Config, error) {
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
}

func newClientSet() (*kubernetes.Clientset, error) {
	config, err := findConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

func getCurrentValue(quantityMap v1.ResourceList, key v1.ResourceName) *float64 {
	if val, ok := quantityMap[key]; ok {
		currentValue := &val
		asApproximateFloat64 := currentValue.AsApproximateFloat64()
		return &asApproximateFloat64
	}
	return nil
}

func queryPrometheusForPod(ctx context.Context, client *promClient, pod v1.Pod) error {
	now := time.Now()
	for _, container := range pod.Spec.Containers {
		//suggestions := []suggestion{}
		respCPURequest, _, err := queryPrometheus(ctx, client, fmt.Sprintf(podCPURequest, pod.Name, container.Name), now)
		if err != nil {
			return fmt.Errorf("Error podCPURequest %v", err)
		}
		asCPUReqSample := respCPURequest.(prommodel.Vector)[0]

		curValue := getCurrentValue(container.Resources.Requests, v1.ResourceCPU)
		fmt.Printf("pod cpu request %s %s %+v %s\n", pod.Name, container.Name, asCPUReqSample.Value, asFloat(curValue))

		respCPULimit, _, err := queryPrometheus(ctx, client, fmt.Sprintf(podCPULimit, pod.Name, container.Name), now)
		if err != nil {
			return fmt.Errorf("Error podCPURequest %v", err)
		}
		asCPULimitSample := respCPULimit.(prommodel.Vector)[0]

		curValue = getCurrentValue(container.Resources.Limits, v1.ResourceCPU)
		fmt.Printf("pod cpu limit %s %s %+v %s\n", pod.Name, container.Name, asCPULimitSample.Value, asFloat(curValue))

		respMemRequest, _, err := queryPrometheus(ctx, client, fmt.Sprintf(podMemoryRequest, pod.Name, container.Name), now)
		if err != nil {
			return fmt.Errorf("Error respMemRequest %v", err)
		}
		asMemReqSample := respMemRequest.(prommodel.Vector)[0]

		curValue = getCurrentValue(container.Resources.Requests, v1.ResourceMemory)
		fmt.Printf("pod mem request %s %s %sMi %s\n", pod.Name, container.Name, asMemReqSample.Value, asFloat(curValue))

		respMemLimit, _, err := queryPrometheus(ctx, client, fmt.Sprintf(podMemoryLimit, pod.Name, container.Name), now)
		if err != nil {
			return fmt.Errorf("Error respMemLimit %v", err)
		}
		asMemLimitSample := respMemLimit.(prommodel.Vector)[0]

		curValue = getCurrentValue(container.Resources.Limits, v1.ResourceMemory)
		fmt.Printf("pod mem limit %s %s %sMi %s\n", pod.Name, container.Name, asMemLimitSample.Value, asFloat(curValue))
		fmt.Printf("\n\n")
	}
	return nil
}

func asFloat(val *float64) string {
	if val == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%f", *val)
}

func makePrometheusClientForCluster() (*promClient, error) {
	config, err := findConfig()
	if err != nil {
		return nil, err
	}

	promurl := fmt.Sprintf("%s%s", config.Host, promOperatorClusterURL)
	tlsConf := config.TLSClientConfig
	cert, err := tls.X509KeyPair(tlsConf.CertData, tlsConf.KeyData)
	if err != nil {
		return nil, err
	}

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(tlsConf.CAData)

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
	}
	tlsConfig.BuildNameToCertificate()
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	client := http.Client{Transport: transport}

	u, err := url.Parse(promurl)
	if err != nil {
		return nil, err
	}
	u.Path = strings.TrimRight(u.Path, "/")

	return &promClient{
		endpoint: u,
		client:   client,
	}, nil
	return nil, nil
}

func queryPrometheus(ctx context.Context, client *promClient, query string, ts time.Time) (interface{}, promv1.Warnings, error) {
	promcli := promv1.NewAPI(client)
	return promcli.Query(ctx, query, ts)
}

func queryRangePrometheus(ctx context.Context, client *promClient, r promv1.Range, query string) (prommodel.Value, promv1.Warnings, error) {
	promcli := promv1.NewAPI(client)
	return promcli.QueryRange(ctx, query, r)
}

func (c *promClient) URL(ep string, args map[string]string) *url.URL {
	p := path.Join(c.endpoint.Path, ep)

	for arg, val := range args {
		arg = ":" + arg
		p = strings.Replace(p, arg, val, -1)
	}

	u := *c.endpoint
	u.Path = p

	return &u
}

func (c *promClient) Do(ctx context.Context, req *http.Request) (*http.Response, []byte, error) {
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	resp, err := c.client.Do(req)
	defer func() {
		if resp != nil {
			resp.Body.Close()
		}
	}()

	if err != nil {
		return nil, nil, err
	}

	var body []byte
	done := make(chan struct{})
	go func() {
		body, err = ioutil.ReadAll(resp.Body)
		close(done)
	}()

	select {
	case <-ctx.Done():
		<-done
		err = resp.Body.Close()
		if err == nil {
			err = ctx.Err()
		}
	case <-done:
	}

	return resp, body, err
}
