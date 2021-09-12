package advisor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	prommodel "github.com/prometheus/common/model"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	promOperatorClusterURL = "/api/v1/namespaces/monitoring/services/prometheus-operated:web/proxy/"
	podCPURequest          = `quantile_over_time(%s, node_namespace_pod_container:container_cpu_usage_seconds_total:sum_irate{pod="%s", container!=""}[1w])`
	podCPULimit            = `max_over_time(node_namespace_pod_container:container_cpu_usage_seconds_total:sum_irate{pod="%s", container!=""}[1w]) * %s`
	podMemoryRequest       = `quantile_over_time(%s, container_memory_working_set_bytes{pod="%s", container!=""}[1w]) / 1024 / 1024`
	podMemoryLimit         = `(max_over_time(container_memory_working_set_bytes{pod="%s", container!=""}[1w]) / 1024 / 1024) * %s`
	deploymentRevision     = "deployment.kubernetes.io/revision"
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

func queryStatistic(ctx context.Context, client *promClient, request string, now time.Time) (map[string]float64, error) {
	output := make(map[string]float64)
	response, _, err := queryPrometheus(ctx, client, request, now)
	if err != nil {
		return output, fmt.Errorf("Error querying statistic %v", err)
	}
	asSamples := response.(prommodel.Vector)

	sampleArray := []*prommodel.Sample{}
	for _, sample := range asSamples {
		sampleArray = append(sampleArray, sample)
	}

	for _, item := range sampleArray {
		containerName := ""
		for k, v := range item.Metric {
			if k == "container" {
				containerName = string(v)
				break
			}
		}
		output[containerName] = float64(item.Value)
	}

	return output, nil
}

func (o *Options) queryPrometheusForPod(ctx context.Context, client *promClient, pod v1.Pod) (prometheusMetrics, error) {
	now := time.Now()
	var err error

	output := prometheusMetrics{}
	output.RequestCPU, err = queryStatistic(ctx, client, fmt.Sprintf(podCPURequest, o.Quantile, pod.Name), now)
	if err != nil {
		return output, err
	}

	output.LimitCPU, err = queryStatistic(ctx, client, fmt.Sprintf(podCPULimit, pod.Name, o.LimitMargin), now)
	if err != nil {
		return output, err
	}

	output.RequestMem, err = queryStatistic(ctx, client, fmt.Sprintf(podMemoryRequest, o.Quantile, pod.Name), now)
	if err != nil {
		return output, err
	}

	output.LimitMem, err = queryStatistic(ctx, client, fmt.Sprintf(podMemoryLimit, pod.Name, o.LimitMargin), now)
	if err != nil {
		return output, err
	}

	return output, nil
}

func float64Average(input []float64) float64 {
	var sum float64
	for _, value := range input {
		sum += value
	}
	return sum / float64(len(input))
}

func findReplicaset(replicasets *appsv1.ReplicaSetList, dep appsv1.Deployment) (*appsv1.ReplicaSet, error) {
	generation, ok := dep.Annotations[deploymentRevision]
	if !ok {
		return nil, fmt.Errorf("could not find label %s for deployment '%s'", deploymentRevision, dep.Name)
	}
	for _, replicaset := range replicasets.Items {
		val, ok := replicaset.Annotations[deploymentRevision]
		if ok && val == generation {
			return &replicaset, nil
		}
	}
	return nil, fmt.Errorf("could not find replicaset for deployment '%s' gen '%v'", dep.Name, generation)
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
